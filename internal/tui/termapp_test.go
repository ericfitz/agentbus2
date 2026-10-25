package tui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// fakeTerm is a cell-grid emulator for the byte stream run writes, with a
// pluggable width rule, so a test can check which column a terminal with
// that rule puts each glyph in. It handles the sequences Bubble Tea v2's
// renderer emits and fails the test on any other, so an unhandled sequence
// is a loud gap, not a silent misplacement.
type fakeTerm struct {
	t     *testing.T
	w, h  int
	cells [][]string // "" is a blank or a wide glyph's continuation cell
	x, y  int
	wrap  bool // pending wrap: a glyph filled the last column
	top   int  // scroll region, 0-based inclusive
	bot   int
	sx    int // saved cursor (ESC 7 / ESC 8)
	sy    int
	width func(cluster string) int
}

func newFakeTerm(t *testing.T, w, h int, width func(string) int) *fakeTerm {
	f := &fakeTerm{t: t, w: w, h: h, bot: h - 1, width: width}
	f.cells = make([][]string, h)
	for i := range f.cells {
		f.cells[i] = make([]string, w)
	}
	return f
}

// appleTerminalWidth is Terminal.app's advance for the clusters the TUI
// draws, measured with CPR (CSI 6n) on 2026-10-25 (macOS 27, Terminal.app,
// TERM_PROGRAM=Apple_Terminal; the same with mode 2027 set, which it
// ignores): a rune with U+FE0F advances like the rune alone (⚙️ and ⏱️ one
// column, 💬️ two), Emoji_Presentation emoji two (🧑, ✅, ❎), a combining
// mark zero, and the format characters U+200B and U+200D one each.
func appleTerminalWidth(cluster string) int {
	w := 0
	for _, r := range cluster {
		switch r {
		case 0xFE0F:
		case 0x200B, 0x200D:
			w++
		default:
			w += ansi.WcWidth.StringWidth(string(r))
		}
	}
	return w
}

// graphemeTerminalWidth is a terminal that measures like lipgloss and the
// renderer (mode 2027 semantics: iTerm2, Ghostty, Windows Terminal): VS16
// widens, format characters are zero.
func graphemeTerminalWidth(cluster string) int {
	return ansi.GraphemeWidth.StringWidth(cluster)
}

func (f *fakeTerm) feed(stream string) {
	p := ansi.NewParser()
	p.SetParamsSize(32)
	p.SetDataSize(1024)
	var state byte
	for len(stream) > 0 {
		seq, width, n, newState := ansi.DecodeSequence(stream, state, p)
		stream, state = stream[n:], newState
		switch {
		case width > 0:
			f.put(seq, f.width(seq))
		case ansi.HasCsiPrefix(seq):
			f.csi(seq, p)
		case ansi.HasOscPrefix(seq), ansi.HasDcsPrefix(seq), ansi.HasApcPrefix(seq), ansi.HasPmPrefix(seq), ansi.HasSosPrefix(seq):
		case ansi.HasEscPrefix(seq):
			switch seq {
			case "\x1b7":
				f.sx, f.sy = f.x, f.y
			case "\x1b8":
				f.x, f.y, f.wrap = f.sx, f.sy, false
			case "\x1bD": // IND
				f.wrap = false
				f.lineFeed()
			case "\x1bM": // RI
				f.wrap = false
				if f.y == f.top {
					f.scrollDown(1)
				} else if f.y > 0 {
					f.y--
				}
			case "\x1b=", "\x1b>":
			default:
				f.t.Fatalf("fakeTerm: unhandled escape %q", seq)
			}
		case len(seq) == 1 && seq[0] < 0x20:
			switch seq[0] {
			case '\r':
				f.x, f.wrap = 0, false
			case '\n':
				// The pty's ONLCR maps a bare LF to CR LF, and the
				// renderer relies on it (SetMapNewline) when the input
				// is no tty.
				f.x, f.wrap = 0, false
				f.lineFeed()
			case '\b':
				f.x, f.wrap = max(f.x-1, 0), false
			case '\t':
				f.x, f.wrap = min((f.x/8+1)*8, f.w-1), false
			default:
				f.t.Fatalf("fakeTerm: unhandled control %q", seq)
			}
		case len(seq) > 0 && seq[0] >= 0xc0:
			// A zero-width cluster by the decoder's measure (a format
			// character, a combining mark after an escape): the terminal's
			// rule decides whether it takes a cell.
			if w := f.width(seq); w > 0 {
				f.put(seq, w)
			} else if f.x > 0 {
				f.cells[f.y][f.x-1] += seq
			}
		default:
			f.t.Fatalf("fakeTerm: unhandled sequence %q", seq)
		}
	}
}

func (f *fakeTerm) put(cluster string, w int) {
	if w <= 0 {
		if f.x > 0 {
			f.cells[f.y][f.x-1] += cluster
		}
		return
	}
	if f.wrap {
		f.x, f.wrap = 0, false
		f.lineFeed()
	}
	if f.x+w > f.w {
		// A wide glyph that does not fit: wrap it whole.
		f.x = 0
		f.lineFeed()
	}
	f.cells[f.y][f.x] = cluster
	for i := 1; i < w && f.x+i < f.w; i++ {
		f.cells[f.y][f.x+i] = ""
	}
	f.x += w
	if f.x >= f.w {
		f.x, f.wrap = f.w-1, true
	}
}

func (f *fakeTerm) lineFeed() {
	if f.y == f.bot {
		f.scrollUp(1)
		return
	}
	if f.y < f.h-1 {
		f.y++
	}
}

func (f *fakeTerm) blankRow() []string { return make([]string, f.w) }

func (f *fakeTerm) scrollUp(n int) {
	for ; n > 0; n-- {
		copy(f.cells[f.top:f.bot], f.cells[f.top+1:f.bot+1])
		f.cells[f.bot] = f.blankRow()
	}
}

func (f *fakeTerm) scrollDown(n int) {
	for ; n > 0; n-- {
		copy(f.cells[f.top+1:f.bot+1], f.cells[f.top:f.bot])
		f.cells[f.top] = f.blankRow()
	}
}

func (f *fakeTerm) csi(seq string, p *ansi.Parser) {
	cmd := ansi.Cmd(p.Command())
	param := func(i, def int) int {
		v, _ := p.Param(i, def)
		if v == 0 && cmd.Final() != 'J' && cmd.Final() != 'K' && cmd.Final() != 'm' {
			return def
		}
		return v
	}
	if cmd.Prefix() != 0 || cmd.Intermediate() != 0 {
		// Private modes (DECSET, DECRST, DECRQM), keyboard protocols, the
		// mode reports: nothing a cell grid shows.
		return
	}
	f.wrap = false
	clampX := func(x int) int { return min(max(x, 0), f.w-1) }
	clampY := func(y int) int { return min(max(y, 0), f.h-1) }
	switch cmd.Final() {
	case 'H', 'f':
		f.y, f.x = clampY(param(0, 1)-1), clampX(param(1, 1)-1)
	case 'G':
		f.x = clampX(param(0, 1) - 1)
	case 'd':
		f.y = clampY(param(0, 1) - 1)
	case 'A':
		f.y = clampY(f.y - param(0, 1))
	case 'B':
		f.y = clampY(f.y + param(0, 1))
	case 'C':
		f.x = clampX(f.x + param(0, 1))
	case 'D':
		f.x = clampX(f.x - param(0, 1))
	case 'K':
		switch param(0, 0) {
		case 0:
			f.eraseCells(f.y, f.x, f.w)
		case 1:
			f.eraseCells(f.y, 0, f.x+1)
		case 2:
			f.eraseCells(f.y, 0, f.w)
		}
	case 'J':
		switch param(0, 0) {
		case 0:
			f.eraseCells(f.y, f.x, f.w)
			for y := f.y + 1; y < f.h; y++ {
				f.cells[y] = f.blankRow()
			}
		case 1:
			for y := 0; y < f.y; y++ {
				f.cells[y] = f.blankRow()
			}
			f.eraseCells(f.y, 0, f.x+1)
		case 2, 3:
			for y := range f.cells {
				f.cells[y] = f.blankRow()
			}
		}
	case 'X':
		f.eraseCells(f.y, f.x, f.x+param(0, 1))
	case '@':
		n := param(0, 1)
		row := f.cells[f.y]
		copy(row[f.x+n:], row[f.x:])
		for i := f.x; i < min(f.x+n, f.w); i++ {
			row[i] = ""
		}
	case 'P':
		n := param(0, 1)
		row := f.cells[f.y]
		copy(row[f.x:], row[min(f.x+n, f.w):])
		for i := max(f.w-n, f.x); i < f.w; i++ {
			row[i] = ""
		}
	case 'L':
		if f.y >= f.top && f.y <= f.bot {
			saved := f.top
			f.top = f.y
			f.scrollDown(param(0, 1))
			f.top = saved
		}
	case 'M':
		if f.y >= f.top && f.y <= f.bot {
			saved := f.top
			f.top = f.y
			f.scrollUp(param(0, 1))
			f.top = saved
		}
	case 'S':
		f.scrollUp(param(0, 1))
	case 'T':
		f.scrollDown(param(0, 1))
	case 'r':
		f.top, f.bot = clampY(param(0, 1)-1), clampY(param(1, f.h)-1)
		f.x, f.y = 0, 0
	case 'm', 'n', 'c', 't', 'q':
	default:
		f.t.Fatalf("fakeTerm: unhandled CSI %q", seq)
	}
}

func (f *fakeTerm) eraseCells(y, from, to int) {
	for x := max(from, 0); x < min(to, f.w); x++ {
		f.cells[y][x] = ""
	}
}

// column returns the column of the first cell on row y containing glyph, or
// -1.
func (f *fakeTerm) column(y int, glyph string) int {
	for x, c := range f.cells[y] {
		if strings.Contains(c, glyph) {
			return x
		}
	}
	return -1
}

// rowText is row y's cells as one string, for failure messages.
func (f *fakeTerm) rowText(y int) string {
	var b strings.Builder
	for _, c := range f.cells[y] {
		if c == "" {
			b.WriteByte('.')
		} else {
			b.WriteString(c)
		}
	}
	return b.String()
}

// railStream runs the TUI as pat beside sam's live session with env as the
// environment, under a width x height screen, and returns the byte stream
// up to and including the first frame that holds both the gear (sam's row)
// and the rail border.
func railStream(t *testing.T, env []string, width, height int) string {
	t.Helper()
	c := newCore(t)
	sam := identityBus(t, c, "sam")
	if _, err := sam.Open(context.Background(), bus.OpenSessionInput{Hint: "worker"}); err != nil {
		t.Fatalf("sam Open: %v", err)
	}
	ensureIdentity(t, c, "pat")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	out := &lockedBuf{}
	done := make(chan runResult, 1)
	go func() {
		_, err := run(ctx, Options{Bus: identityBus(t, c, "pat"), Identity: "pat", Input: pr, Output: out, Stderr: io.Discard, Width: width, Height: height, Environ: env})
		done <- runResult{err: err}
	}()
	var snap string
	waitFor(t, 5*time.Second, func() bool {
		snap = out.String()
		return strings.Contains(snap, "⚙") && strings.Contains(snap, "│") && strings.Contains(snap, "\U0001F9D1")
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	return snap
}

// borderColumns emulates stream on a terminal with the width rule and
// returns the border column of the gear row and of the user row.
func borderColumns(t *testing.T, stream string, width, height int, rule func(string) int) (gear, user int, f *fakeTerm) {
	t.Helper()
	f = newFakeTerm(t, width, height, rule)
	f.feed(stream)
	gearRow, userRow := -1, -1
	for y := 0; y < height; y++ {
		if f.column(y, "⚙") >= 0 {
			gearRow = y
		}
		if f.column(y, "\U0001F9D1") >= 0 {
			userRow = y
		}
	}
	if gearRow < 0 || userRow < 0 {
		t.Fatalf("gear row %d, user row %d not both on screen:\n%s", gearRow, userRow, f.dump())
	}
	gear, user = f.column(gearRow, "│"), f.column(userRow, "│")
	if gear < 0 || user < 0 {
		t.Fatalf("no border on gear row %q or user row %q", f.rowText(gearRow), f.rowText(userRow))
	}
	return gear, user, f
}

func (f *fakeTerm) dump() string {
	var b strings.Builder
	for y := range f.cells {
		b.WriteString(f.rowText(y))
		b.WriteByte('\n')
	}
	return b.String()
}

// TestRailBorderAlignsInTerminalApp: in Terminal.app the gear advances one
// column, not the two lipgloss and the renderer count, so without a
// correction every cell after it, the rail border included, lands one column
// left (Eric's screenshot, 2026-10-25). The gear's icon string carries its
// own one-column advance there, and the cell after the glyph stays blank, so
// the glyph is not clipped.
func TestRailBorderAlignsInTerminalApp(t *testing.T) {
	const w, h = 72, 18
	stream := railStream(t, []string{"TERM_PROGRAM=Apple_Terminal", "TERM=xterm-256color"}, w, h)
	gear, user, f := borderColumns(t, stream, w, h, appleTerminalWidth)
	if gear != user {
		t.Fatalf("border at column %d on the gear row, %d on the user row:\n%s", gear, user, f.dump())
	}
	for y := 0; y < h; y++ {
		if x := f.column(y, "⚙"); x >= 0 {
			if next := f.cells[y][x+1]; strings.TrimSpace(strings.ReplaceAll(next, "\u200b", "")) != "" {
				t.Fatalf("glyph %q in the cell after the gear would clip it: %q", next, f.rowText(y))
			}
		}
	}
}

// TestRailBorderAlignsOnGraphemeTerminals: a terminal that measures like the
// renderer (VS16 widens, format characters are zero) gets the plain icon
// and lines up too; the Terminal.app pad is never sent to it.
func TestRailBorderAlignsOnGraphemeTerminals(t *testing.T) {
	const w, h = 72, 18
	for _, env := range [][]string{
		{"TERM_PROGRAM=iTerm.app", "TERM=xterm-256color"},
		{"TERM=xterm-ghostty"},
	} {
		stream := railStream(t, env, w, h)
		if strings.Contains(stream, "\u200b") {
			t.Fatalf("%v: the Terminal.app pad U+200B reached a grapheme-width terminal", env)
		}
		gear, user, f := borderColumns(t, stream, w, h, graphemeTerminalWidth)
		if gear != user {
			t.Fatalf("%v: border at column %d on the gear row, %d on the user row:\n%s", env, gear, user, f.dump())
		}
	}
}
