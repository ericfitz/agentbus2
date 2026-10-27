package tui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestRailWindowScrollsByOneWithArrows(t *testing.T) {
	// 10 items in 5 rows: from the top, 4 items and the down arrow.
	if top, end := railWindow(10, 5, 0, 0, false); top != 0 || end != 4 {
		t.Fatalf("top=%d end=%d, want 0 4", top, end)
	}
	// Stepping past the last shown item scrolls just far enough to show
	// it; the up arrow now takes a row too.
	top, end := railWindow(10, 5, 0, 4, false)
	if top != 2 || end != 5 {
		t.Fatalf("top=%d end=%d, want 2 5", top, end)
	}
	if top, end = railWindow(10, 5, top, 5, false); top != 3 || end != 6 {
		t.Fatalf("top=%d end=%d, want 3 6", top, end)
	}
	// The last item: no down arrow, and no empty rows under it.
	if top, end = railWindow(10, 5, top, 9, false); top != 6 || end != 10 {
		t.Fatalf("top=%d end=%d, want 6 10", top, end)
	}
	// Back up: stepping above the first shown item scrolls by one.
	if top, end = railWindow(10, 5, 6, 5, false); top != 5 || end != 8 {
		t.Fatalf("top=%d end=%d, want 5 8", top, end)
	}
	// Everything fits: no scrolling at all.
	if top, end = railWindow(3, 5, 2, 2, false); top != 0 || end != 3 {
		t.Fatalf("top=%d end=%d, want 0 3", top, end)
	}
	// A selection elsewhere keeps the offset.
	if top, _ = railWindow(10, 5, 3, -1, false); top != 3 {
		t.Fatalf("top=%d, want 3", top)
	}
}

func TestRailLayoutFitsAll(t *testing.T) {
	n := [railSections]int{5, 1, 3, 1, 3}
	b, blanks, compact := railLayout(40, n, [railSections]bool{})
	if b != n || !blanks || compact {
		t.Fatalf("b=%v blanks=%v compact=%v, want every item, blanks, not compact", b, blanks, compact)
	}
}

func TestRailLayoutKeepsFourRowsPerSection(t *testing.T) {
	// Blanks fit, the floors fit: each long section keeps four rows (two
	// items plus both arrows) and the spare row goes round-robin.
	b, blanks, compact := railLayout(railFixed(true)+9, [railSections]int{20, 0, 0, 0, 20}, [railSections]bool{})
	if b != [railSections]int{5, 0, 0, 0, 4} || !blanks || compact {
		t.Fatalf("b=%v blanks=%v compact=%v", b, blanks, compact)
	}
	// A section that fits in fewer rows takes only what it needs.
	b, _, _ = railLayout(railFixed(true)+10, [railSections]int{20, 0, 0, 2, 20}, [railSections]bool{})
	if b != [railSections]int{4, 0, 0, 2, 4} {
		t.Fatalf("b=%v", b)
	}
}

func TestRailLayoutDropsBlanksFirst(t *testing.T) {
	n := [railSections]int{6, 19, 4, 0, 4}
	b, blanks, compact := railLayout(21, n, [railSections]bool{})
	if blanks || compact {
		t.Fatalf("blanks=%v compact=%v, want neither (headings only, four-row floors)", blanks, compact)
	}
	sum := 0
	for i, v := range b {
		if v < min(n[i], 4) {
			t.Fatalf("section %d budget %d, want at least %d", i, v, min(n[i], 4))
		}
		sum += v
	}
	if sum+len(railHeadings) > 21 {
		t.Fatalf("budgets %v need %d rows, have 21", b, sum+len(railHeadings))
	}
}

func TestRailLayoutCompact(t *testing.T) {
	n := [railSections]int{6, 19, 4, 0, 4}
	b, blanks, compact := railLayout(16, n, [railSections]bool{})
	if blanks || !compact {
		t.Fatalf("blanks=%v compact=%v, want compact without blanks", blanks, compact)
	}
	// Two-item floors {2,2,2,0,2} fit (8 + 5 headings <= 16); the three spare
	// rows go round-robin to the sections still short: channels, memories,
	// tasks. Tags is empty and sessions is already full at its floor.
	if want := [railSections]int{3, 3, 3, 0, 2}; b != want {
		t.Fatalf("budgets %v, want %v", b, want)
	}
}

func TestRailLayoutTooShortStillFloors(t *testing.T) {
	// Below the compact floors the layout cannot fit; the rail's MaxHeight
	// clips. The floors are still what is asked for, never less.
	b, blanks, compact := railLayout(8, [railSections]int{20, 0, 0, 0, 20}, [railSections]bool{})
	if b != [railSections]int{2, 0, 0, 0, 2} || blanks || !compact {
		t.Fatalf("b=%v blanks=%v compact=%v", b, blanks, compact)
	}
}

func TestRailLayoutCollapsedGetsNothing(t *testing.T) {
	n := [railSections]int{3, 3, 3, 3, 3}
	b, _, _ := railLayout(40, n, [railSections]bool{secMemories: true})
	if b != [railSections]int{3, 0, 3, 3, 3} {
		t.Fatalf("b=%v", b)
	}
}

func TestRailWindowCompactCombinedArrow(t *testing.T) {
	// Budget 2: one item row and one row for both arrows.
	top, end := railWindow(19, 2, 0, 10, true)
	if top != 10 || end != 11 {
		t.Fatalf("top=%d end=%d, want 10 11", top, end)
	}
	// From the top: one item and a down-only arrow row.
	if top, end = railWindow(19, 2, 0, 0, true); top != 0 || end != 1 {
		t.Fatalf("top=%d end=%d, want 0 1", top, end)
	}
	// The last item: still one item and one (up-only) arrow row.
	if top, end = railWindow(19, 2, 0, 18, true); top != 18 || end != 19 {
		t.Fatalf("top=%d end=%d, want 18 19", top, end)
	}
}

// kindChannels is n channels of kind k named prefix00, prefix01, ...
func kindChannels(prefix string, k bus.Kind, n int) []bus.Channel {
	var out []bus.Channel
	for i := range n {
		out = append(out, bus.Channel{Name: fmt.Sprintf("%s%02d", prefix, i), Kind: k})
	}
	return out
}

func TestRailCompactArrowRow(t *testing.T) {
	f := newFixture(t)
	f.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	f.setRailChannels(kindChannels("m", bus.KindMemory, 19)...)
	f.key("esc")
	f.key("home")
	for range 10 {
		f.key("down") // the channels section is empty: memories heading, then m00..m08
	}
	if got := f.m.selName(); got != "m08" {
		t.Fatalf("setup: selected %q, want m08", got)
	}
	// Too short for four-row floors: compact arrows are forced.
	f.send(tea.WindowSizeMsg{Width: 80, Height: 12})
	if _, blanks, compact := f.m.railSpans(); blanks || !compact {
		t.Fatalf("blanks=%v compact=%v, want compact without blanks", blanks, compact)
	}
	// Literal counts: m08 is the only item shown, so 8 are hidden above it
	// and 10 below.
	lines := f.railLines()
	if want := "▲8 ▼10 more"; !slices.Contains(lines, want) {
		t.Fatalf("want a combined arrow row %q:\n%s", want, strings.Join(lines, "\n"))
	}
}

func TestRail24LinesShowsEverySection(t *testing.T) {
	f := newFixture(t)
	var chans []bus.Channel
	chans = append(chans, kindChannels("c", bus.KindOrdinary, 6)...)
	chans = append(chans, kindChannels("m", bus.KindMemory, 19)...)
	chans = append(chans, kindChannels("t", bus.KindTasks, 4)...)
	f.setRailChannels(chans...)
	for _, n := range []string{"s1", "s2", "s3", "s4", "s5"} {
		f.m.sessionsSeen[n] = time.Now()
	}
	f.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	lines := f.railLines()
	if len(lines) > f.m.stream.Height()+1 {
		t.Fatalf("rail is %d rows, stream+1 is %d", len(lines), f.m.stream.Height()+1)
	}
	all := strings.Join(lines, "\n")
	// Items under each non-empty section: the row after its heading is not
	// another heading or blank.
	for sec, h := range railHeadings {
		i := slices.Index(lines, headLine(h))
		if i < 0 {
			t.Fatalf("heading %q missing:\n%s", h, all)
		}
		if sec == secTags {
			continue
		}
		if i+1 >= len(lines) || lines[i+1] == "" || slices.ContainsFunc(railHeadings[:], func(h string) bool { return lines[i+1] == headLine(h) }) {
			t.Fatalf("no item under %q:\n%s", h, all)
		}
	}
	// Down reaches the last session row and stops there: session rows are
	// stops (TUI decision 14).
	f.key("esc")
	f.key("home")
	for range len(f.m.railPositions()) + 1 {
		f.key("down")
	}
	if p := f.m.railPos(); p.sec != secSessions || p.item != len(f.m.sessionRows())-1 || f.m.pane() != paneChannels {
		t.Fatalf("down must end on the last session row, ended on %+v pane=%v", p, f.m.pane())
	}
	if !slices.ContainsFunc(f.railLines(), func(l string) bool { return strings.Contains(l, "s5") }) {
		t.Fatalf("the sessions rows must still be drawn (the window scrolls to the selected last row):\n%s", strings.Join(f.railLines(), "\n"))
	}
}

// isArrowRow reports whether the trimmed rail row l is a scroll arrow row
// ("▼ 3 more"), not a section heading, which starts with the same glyph.
func isArrowRow(l, arrow string) bool {
	return strings.HasPrefix(l, arrow) && strings.HasSuffix(l, "more")
}

// headLine is the rail row of the expanded, unselected section heading h.
func headLine(h string) string { return markOpen + " " + h }

// railLines is the rendered rail, stripped and trimmed, one entry per row.
func (f *fixture) railLines() []string {
	lines := strings.Split(ansi.Strip(f.m.renderRails()), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return lines
}

func TestRailShowsEverySectionHeading(t *testing.T) {
	f := newFixture(t)
	lines := f.railLines()
	prev := -1
	for _, h := range railHeadings {
		i := slices.Index(lines, headLine(h))
		if i <= prev {
			t.Fatalf("want heading %q after the previous one:\n%s", h, strings.Join(lines, "\n"))
		}
		prev = i
	}
	// An empty tags section is its heading alone, with no hint row.
	if i := slices.Index(lines, headLine("tags")); lines[i+1] != "" || strings.Contains(strings.Join(lines, "\n"), "press t") {
		t.Fatalf("want the tags heading with no rows under it:\n%s", strings.Join(lines, "\n"))
	}
	f.followTags(t, []string{"docs"})
	lines = f.railLines()
	tags := slices.Index(lines, headLine("tags"))
	if tags < 0 || !strings.Contains(lines[tags+1], "docs") || lines[tags+2] != "" {
		t.Fatalf("want the tag set under its heading:\n%s", strings.Join(lines, "\n"))
	}
}

func TestRailSelectedRowStaysOneLine(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", 60)
	if _, err := f.sam.CreateChannel(context.Background(), bus.CreateChannelInput{Name: long}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.key("esc")
	before := len(f.railLines())
	for f.m.selName() != long {
		f.key("down")
	}
	lines := f.railLines()
	if len(lines) != before {
		t.Fatalf("selecting the long channel changed the rail from %d to %d rows:\n%s", before, len(lines), strings.Join(lines, "\n"))
	}
	i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "xxxx") })
	if i < 0 || i+1 >= len(lines) {
		t.Fatalf("want the long channel shown, with a row after it:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.HasSuffix(lines[i], "…") || strings.Contains(lines[i+1], "x") {
		t.Fatalf("want one row ending in an ellipsis:\n%s", strings.Join(lines, "\n"))
	}
	for _, l := range strings.Split(f.m.renderRails(), "\n") {
		if w := ansi.StringWidth(l); w > f.m.railWidth() {
			t.Fatalf("row %q is %d wide, rail is %d", ansi.Strip(l), w, f.m.railWidth())
		}
	}
}

func TestRailScrollsLongChannelList(t *testing.T) {
	f := newFixture(t)
	for i := range 12 {
		if _, err := f.sam.CreateChannel(context.Background(), bus.CreateChannelInput{Name: fmt.Sprintf("ch%02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	f.run(f.m.statusCmd())
	f.send(tea.WindowSizeMsg{Width: 100, Height: 20})
	f.key("esc")
	f.key("home")
	f.key("down") // off the channels heading onto the first channel
	for range len(f.m.channels) {
		if f.m.sel <= 0 {
			break
		}
		f.key("up")
	}
	nc := spans(f)[secChannels]
	if nc.top != 0 || nc.end >= nc.n {
		t.Fatalf("want the list cut from the top, got %+v", nc)
	}
	lines := f.railLines()
	if !slices.ContainsFunc(lines, func(l string) bool { return isArrowRow(l, "▼") }) {
		t.Fatalf("want a down arrow:\n%s", strings.Join(lines, "\n"))
	}
	if slices.ContainsFunc(lines, func(l string) bool { return isArrowRow(l, "▲") }) {
		t.Fatalf("no up arrow at the top:\n%s", strings.Join(lines, "\n"))
	}
	// Down from the last shown channel scrolls and selects the next one.
	for range nc.end - 1 {
		f.key("down")
	}
	f.key("down")
	if f.m.sel != nc.end {
		t.Fatalf("sel=%d, want %d", f.m.sel, nc.end)
	}
	s := spans(f)[secChannels]
	if s.top == 0 || f.m.sel >= s.end || s.end-s.top < 2 {
		t.Fatalf("selection not scrolled into view: %+v sel=%d", s, f.m.sel)
	}
	lines = f.railLines()
	if !slices.ContainsFunc(lines, func(l string) bool { return isArrowRow(l, "▲") }) {
		t.Fatalf("want an up arrow once scrolled:\n%s", strings.Join(lines, "\n"))
	}
	// Down through the rest reaches the last channel, then leaves the section
	// for the first memory channel.
	for f.m.sel < s.n-1 {
		f.key("down")
	}
	if s = spans(f)[secChannels]; s.end != s.n {
		t.Fatalf("last channel not shown: %+v", s)
	}
	if lines = f.railLines(); slices.ContainsFunc(lines, func(l string) bool { return isArrowRow(l, "▼") }) {
		t.Fatalf("no down arrow at the end:\n%s", strings.Join(lines, "\n"))
	}
	f.key("down")
	if f.m.headSel != secMemories || f.m.sel != s.n-1 {
		t.Fatalf("down from the last channel should select the memories heading, headSel=%d sel=%d", f.m.headSel, f.m.sel)
	}
	f.key("down")
	if f.m.selName() != "dev-notes" || f.m.pane() != paneChannels {
		t.Fatalf("down from the memories heading should enter memories, sel=%q pane=%v", f.m.selName(), f.m.pane())
	}
	// Up returns through the heading to the last channel, still shown.
	f.key("up")
	f.key("up")
	if s = spans(f)[secChannels]; f.m.sel != s.n-1 || s.end != s.n {
		t.Fatalf("sel=%d span=%+v", f.m.sel, s)
	}
	// Up scrolls back one at a time to the top.
	for f.m.sel > 0 {
		top := spans(f)[secChannels].top
		f.key("up")
		if s = spans(f)[secChannels]; f.m.sel < s.top || top-s.top > 1 {
			t.Fatalf("sel=%d span=%+v (was top %d)", f.m.sel, s, top)
		}
	}
	if s = spans(f)[secChannels]; s.top != 0 {
		t.Fatalf("back at the first channel, want top 0: %+v", s)
	}
}

func spans(f *fixture) [railSections]railSpan {
	s, _, _ := f.m.railSpans()
	return s
}

// invisibleText returns the printable runs of line drawn in a foreground
// equal to the background in effect, after SGR colors 30-37/90-97 against
// 40-47/100-107 and 38;5;n against 48;5;n; nothing when every cell is
// legible. Only the sixteen ANSI colors and the default are representable
// in a theme, so those forms are the ones a rendered row can carry.
func invisibleText(line string) []string {
	var runs []string
	var run strings.Builder
	fg, bg := "", ""
	state := byte(ansi.NormalState)
	flush := func() {
		if run.Len() > 0 {
			runs = append(runs, run.String())
			run.Reset()
		}
	}
	for rest := line; rest != ""; {
		seq, width, n, next := ansi.DecodeSequence(rest, state, nil)
		state, rest = next, rest[n:]
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m"), ";")
			for i := 0; i < len(params); i++ {
				p := params[i]
				switch {
				case p == "" || p == "0":
					fg, bg = "", ""
				case p == "38" || p == "48":
					if i+2 < len(params) && params[i+1] == "5" {
						if p == "38" {
							fg = "256:" + params[i+2]
						} else {
							bg = "256:" + params[i+2]
						}
						i += 2
					}
				case p == "39":
					fg = ""
				case p == "49":
					bg = ""
				case len(p) == 2 && p[0] == '3':
					fg = "ansi:" + string(p[1])
				case len(p) == 2 && p[0] == '9':
					fg = "ansi:" + string('8'+p[1]-'0')
				case len(p) == 2 && p[0] == '4':
					bg = "ansi:" + string(p[1])
				case len(p) == 3 && strings.HasPrefix(p, "10"):
					bg = "ansi:" + string('8'+p[2]-'0')
				}
			}
			continue
		}
		if width > 0 && strings.TrimSpace(seq) != "" && fg != "" && fg == bg {
			run.WriteString(seq)
		} else if width > 0 {
			flush()
		}
	}
	flush()
	return runs
}

// isHeadingRow reports whether the stripped rail row is a section heading
// (collapsed or expanded). A collapsed heading starts with markSel like a
// selected item row does, so the two are told apart by the heading name.
func isHeadingRow(plain string) bool {
	return slices.ContainsFunc(railHeadings[:], func(h string) bool {
		return strings.HasPrefix(plain, markSel+" "+h) || strings.HasPrefix(plain, markOpen+" "+h)
	})
}

// Every selectable row type (chat, memory, task list, tag set and each
// section heading, expanded and collapsed), under both icon sets, at the
// narrowest and a normal rail width, with and without rail focus: the
// selected row renders as exactly one row of exactly the rail's width, with
// no text drawn in its background color, and reads as its unselected row with
// markSel in place of the leading space. The sessions rows are drawn but are
// not stops. Spec 2026-10-21 section 5.
func TestSelectedRowsStayOneLine(t *testing.T) {
	t.Cleanup(func() { setIcons(emojiIcons) })
	for _, icons := range []string{"emoji", "nerdfont"} {
		for _, rail := range []int{12, 24} {
			for _, collapsed := range [][]int{nil, {secMemories, secTasks}} {
				name := fmt.Sprintf("%s/%d/collapsed=%v", icons, rail, collapsed)
				LoadIcons(Settings{Icons: icons}, io.Discard)
				f := newFixtureWith(t, nil, Host{RailWidth: rail})
				if _, err := f.sam.CreateChannel(context.Background(), bus.CreateChannelInput{Name: "work", Kind: bus.KindTasks}); err != nil {
					t.Fatal(err)
				}
				f.followTags(t, []string{"docs"})
				f.m.sessionsSeen["cli-bot"] = time.Now().Add(-5 * time.Minute) // an idle row beside the live ones
				for _, sec := range collapsed {
					f.m.toggleSection(sec, true)
				}
				f.key("esc")
				f.key("home")
				width := f.m.railWidth()
				plainRows := func(m Model) []string {
					rows := strings.Split(m.renderRails(), "\n")
					for i := range rows {
						rows[i] = ansi.Strip(rows[i])
					}
					return rows
				}
				wantRows := len(plainRows(f.m))
				wantNames := map[railPos]bool{}
				for _, p := range f.m.railPositions() {
					wantNames[p] = false
				}
				for step := 0; ; step++ {
					if step > len(wantNames)+1 {
						t.Fatalf("%s: down never stopped", name)
					}
					pos := f.m.railPos()
					sel := f.m.selName()
					wantNames[pos] = true
					for _, focused := range []bool{true, false} {
						f.m.mode = modeNormal
						if !focused {
							f.m.mode = modeInsert
						}
						raw := strings.Split(f.m.renderRails(), "\n")
						rows := plainRows(f.m)
						if len(rows) != wantRows {
							t.Fatalf("%s/%v/%+v: rail has %d rows selected, %d at the start", name, focused, pos, len(rows), wantRows)
						}
						idx := -1
						n := 0
						for i, r := range rows {
							switch {
							case pos.item < 0 && isHeadingRow(r) && strings.Contains(r, railHeadings[pos.sec]):
								idx, n = i, n+1
							case pos.item >= 0 && strings.HasPrefix(r, markSel) && !isHeadingRow(r):
								idx, n = i, n+1
							}
						}
						if n != 1 {
							t.Fatalf("%s/%+v/%q/focused=%v: %d selected rows:\n%s", name, pos, sel, focused, n, strings.Join(rows, "\n"))
						}
						row := raw[idx]
						if w := ansi.StringWidth(row); w != width {
							t.Errorf("%s/%+v/focused=%v: selected row %q is %d wide, rail is %d", name, pos, focused, rows[idx], w, width)
						}
						if hidden := invisibleText(row); len(hidden) > 0 {
							t.Errorf("%s/%+v/focused=%v: %q drawn in its background color: %q", name, pos, focused, hidden, row)
						}
						// The same row unselected: select the first heading
						// (or, when that is the row, another).
						other := f.m
						other.headSel = secSessions
						if pos.item < 0 && pos.sec == secSessions {
							other.headSel = secChannels
						}
						base := plainRows(other)[idx]
						got := strings.TrimRight(rows[idx], " ")
						want := strings.TrimRight(base, " ")
						if pos.item >= 0 {
							want = markSel + strings.TrimPrefix(want, " ")
						}
						if got != want {
							t.Errorf("%s/%+v/focused=%v: selected row %q, want the unselected row %q with the mark", name, pos, focused, got, want)
						}
					}
					f.m.mode = modeNormal
					f.key("down")
					if f.m.railPos() == pos {
						break
					}
				}
				for p, seen := range wantNames {
					if !seen {
						t.Errorf("%s: never selected %+v", name, p)
					}
				}
			}
		}
	}
}

// channelSection is keyed on Kind, never on the name (ADR 0008).
func TestChannelSection(t *testing.T) {
	for _, c := range []struct {
		name string
		kind bus.Kind
		want int
	}{
		{"general", bus.KindOrdinary, secChannels},
		{"general/x", bus.KindOrdinary, secChannels},
		{"memory", bus.KindOrdinary, secChannels},
		{"memory/tmi", bus.KindOrdinary, secChannels},
		{"tasks/aws", bus.KindOrdinary, secChannels},
		{"tasks", bus.KindOrdinary, secChannels},
		{"tmi", bus.KindMemory, secMemories},
		{"memory/tmi", bus.KindMemory, secMemories},
		{"aws", bus.KindTasks, secTasks},
		{"tasks/aws", bus.KindTasks, secTasks},
		{tagPanePrefix + "a,b", kindTags, secTags},
	} {
		if got := channelSection(bus.Channel{Name: c.name, Kind: c.kind}); got != c.want {
			t.Errorf("channelSection(%q, %s) = %d, want %d", c.name, c.kind, got, c.want)
		}
	}
}

// setRailChannels replaces the rail's channels with chans (name, kind).
func (f *fixture) setRailChannels(chans ...bus.Channel) {
	f.m.setChannels(chans, nil, "")
}

// A channel's name is shown in full whatever it looks like: no prefix is read
// or stripped (ADR 0008).
func TestFullNamesInRail(t *testing.T) {
	f := newFixture(t)
	f.setRailChannels(
		bus.Channel{Name: "memory-plans", Kind: bus.KindOrdinary},
		bus.Channel{Name: "memory/x", Kind: bus.KindOrdinary},
		bus.Channel{Name: "tasks/y", Kind: bus.KindOrdinary},
		bus.Channel{Name: "general/z", Kind: bus.KindOrdinary},
		bus.Channel{Name: "memory/tmi", Kind: bus.KindMemory},
	)
	lines := f.railLines()
	head := slices.Index(lines, headLine("channels"))
	next := slices.Index(lines, headLine("memories"))
	if head < 0 || next < head {
		t.Fatalf("channels and memories headings:\n%s", strings.Join(lines, "\n"))
	}
	under := strings.Join(lines[head+1:next], "\n")
	for _, name := range []string{"memory-plans", "memory/x", "tasks/y", "general/z"} {
		if !strings.Contains(under, name) {
			t.Errorf("%q must sit under channels with its full name:\n%s", name, strings.Join(lines, "\n"))
		}
	}
	mem := strings.Join(lines[next+1:slices.Index(lines, headLine("tasks"))], "\n")
	if !strings.Contains(mem, "memory/tmi") {
		t.Errorf("a memory channel keeps its full name:\n%s", strings.Join(lines, "\n"))
	}
}

func TestRailSectionsOrder(t *testing.T) {
	f := newFixture(t)
	f.setRailChannels(
		bus.Channel{Name: "aws-eip-logging", Kind: bus.KindTasks},
		bus.Channel{Name: "tmi", Kind: bus.KindMemory},
		bus.Channel{Name: "agentbus", Kind: bus.KindOrdinary},
		bus.Channel{Name: "plans", Kind: bus.KindMemory},
		bus.Channel{Name: "general", Kind: bus.KindOrdinary},
	)
	var lines []string
	for _, l := range f.railLines() {
		if l != "" {
			lines = append(lines, l)
		}
	}
	want := []string{"channels", "agentbus", "general", "memories", "plans", "tmi", "tasks", "aws-eip-logging", "tags", "sessions"}
	if len(lines) < len(want) {
		t.Fatalf("rail too short:\n%s", strings.Join(lines, "\n"))
	}
	for i, w := range want {
		heading := slices.Contains(railHeadings[:], w)
		if (heading && lines[i] != headLine(w)) || (!heading && !strings.Contains(lines[i], w)) {
			t.Fatalf("rail line %d = %q, want %q:\n%s", i, lines[i], w, strings.Join(lines, "\n"))
		}
	}
}

func TestEmptySectionHeadingOnly(t *testing.T) {
	f := newFixture(t)
	lines := f.railLines()
	i := slices.Index(lines, headLine("tags"))
	if i < 0 || i+2 >= len(lines) || lines[i+1] != "" || lines[i+2] != headLine("sessions") {
		t.Fatalf("want the tags heading, a blank, then sessions:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(strings.Join(lines, "\n"), "press t to create") {
		t.Fatalf("the tag hint is gone:\n%s", strings.Join(lines, "\n"))
	}
}

// toHeading walks the rail selection onto section sec's heading from the top.
func (f *fixture) toHeading(t *testing.T, sec int) {
	t.Helper()
	f.key("esc")
	f.key("home")
	for range len(f.m.railPositions()) {
		if f.m.headSel == sec {
			return
		}
		f.key("down")
	}
	t.Fatalf("down never reached the %s heading, at %+v", railHeadings[sec], f.m.railPos())
}

// sectionedFixture has an item in every section, rail focused with the
// channels heading selected.
func sectionedFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	if _, err := f.sam.CreateChannel(context.Background(), bus.CreateChannelInput{Name: "work", Kind: bus.KindTasks}); err != nil {
		t.Fatal(err)
	}
	f.followTags(t, []string{"docs"})
	f.key("esc")
	f.key("home")
	return f
}

func TestRailDownWalksHeadings(t *testing.T) {
	f := sectionedFixture(t)
	want := f.m.railPositions()
	// Five headings, every channel (tags included) and every session row.
	if n := 5 + len(f.m.channels) + len(f.m.sessionRows()); len(want) != n {
		t.Fatalf("railPositions has %d stops, want %d: %v", len(want), n, want)
	}
	var got []railPos
	for range len(want) + 2 {
		pos := f.m.railPos()
		if len(got) == 0 || got[len(got)-1] != pos {
			got = append(got, pos)
		}
		shown := f.m.selName()
		f.key("down")
		if pos.item < 0 && f.m.railPos().item < 0 && f.m.selName() != shown {
			t.Fatalf("moving between headings changed the stream: %q -> %q", shown, f.m.selName())
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("down visited %v, want %v", got, want)
	}
	if last := got[len(got)-1]; last.sec != secSessions || last.item != len(f.m.sessionRows())-1 || f.m.pane() != paneChannels {
		t.Fatalf("down ended on %+v pane=%v, want the last session row", last, f.m.pane())
	}
	// A heading is selected without the stream following it: down from the
	// last channel lands on the next heading with the stream unchanged.
	f.key("esc")
	f.moveTo(t, "general")
	f.key("down")
	if f.m.headSel != secMemories || f.m.selName() != "general" {
		t.Fatalf("down from the last channel: headSel=%d sel=%q, want the memories heading with the stream on general", f.m.headSel, f.m.selName())
	}
}

func TestHeadingLeftCollapsesRightExpands(t *testing.T) {
	f := sectionedFixture(t)
	f.toHeading(t, secMemories)
	f.key("left")
	if !f.m.collapsed[secMemories] || f.m.headSel != secMemories || f.m.pane() != paneChannels {
		t.Fatalf("left on a heading collapses it: collapsed=%v headSel=%d pane=%v", f.m.collapsed, f.m.headSel, f.m.pane())
	}
	f.key("left")
	if !f.m.collapsed[secMemories] {
		t.Fatal("left on a collapsed heading stays collapsed")
	}
	f.key("right")
	if f.m.collapsed[secMemories] {
		t.Fatalf("right on a heading expands it: %v", f.m.collapsed)
	}
	f.key("right")
	if f.m.collapsed[secMemories] {
		t.Fatal("right on an expanded heading stays expanded")
	}
	// On an item left keeps its stream meaning: it never collapses a section.
	f.key("down")
	f.key("left")
	if f.m.headSel >= 0 || f.m.collapsed != [railSections]bool{} {
		t.Fatalf("left on an item must not collapse: headSel=%d collapsed=%v", f.m.headSel, f.m.collapsed)
	}
}

func TestHeadingEnterToggles(t *testing.T) {
	f := sectionedFixture(t)
	f.toHeading(t, secTasks)
	f.key("enter")
	if !f.m.collapsed[secTasks] || f.m.mode != modeNormal {
		t.Fatalf("enter collapses an expanded heading and does not compose: collapsed=%v mode=%v", f.m.collapsed, f.m.mode)
	}
	f.key("enter")
	if f.m.collapsed[secTasks] || f.m.mode != modeNormal {
		t.Fatalf("enter expands a collapsed heading: collapsed=%v mode=%v", f.m.collapsed, f.m.mode)
	}
}

func TestCollapsedHeadingSummary(t *testing.T) {
	t.Cleanup(func() { setIcons(emojiIcons) })
	f := newFixtureWith(t, nil, Host{RailWidth: 30})
	f.setRailChannels(kindChannels("m", bus.KindMemory, 19)...)
	f.key("esc")
	if !slices.Contains(f.railLines(), headLine("memories")) {
		t.Fatalf("expanded heading is %q:\n%s", headLine("memories"), strings.Join(f.railLines(), "\n"))
	}
	f.m.toggleSection(secMemories, true)
	if want := markSel + " memories (19)"; !slices.Contains(f.railLines(), want) {
		t.Fatalf("want %q:\n%s", want, strings.Join(f.railLines(), "\n"))
	}
	for seq := int64(1); seq <= 3; seq++ {
		f.msgAt("m04", "sam", seq, 0, "hi")
	}
	if want := markSel + " memories (19, 3 unread)"; !slices.Contains(f.railLines(), want) {
		t.Fatalf("want %q:\n%s", want, strings.Join(f.railLines(), "\n"))
	}
	if slices.ContainsFunc(f.railLines(), func(l string) bool { return strings.Contains(l, "m04") }) {
		t.Fatalf("a collapsed section draws no items:\n%s", strings.Join(f.railLines(), "\n"))
	}
	LoadIcons(Settings{IconMap: map[string]string{"collapsed": "X"}}, io.Discard)
	if want := "X memories (19, 3 unread)"; !slices.Contains(f.railLines(), want) {
		t.Fatalf("icon_map override: want %q:\n%s", want, strings.Join(f.railLines(), "\n"))
	}
}

func TestCollapsedSectionRowsGoElsewhere(t *testing.T) {
	f := newFixture(t)
	var chans []bus.Channel
	chans = append(chans, kindChannels("c", bus.KindOrdinary, 6)...)
	chans = append(chans, kindChannels("m", bus.KindMemory, 19)...)
	chans = append(chans, kindChannels("t", bus.KindTasks, 4)...)
	f.setRailChannels(chans...)
	for _, n := range []string{"s1", "s2", "s3", "s4", "s5"} {
		f.m.sessionsSeen[n] = time.Now()
	}
	shown := func() (n int) {
		for sec, s := range spans(f) {
			if sec != secMemories {
				n += s.end - s.top
			}
		}
		return n
	}
	// At 80x24 the four-row floors fill the rail exactly, so the freed
	// memory rows come back as the blank separators, not as items.
	f.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	before := shown()
	if _, blanks, _ := f.m.railSpans(); blanks {
		t.Fatal("setup: 80x24 with every section expanded has no room for blank rows")
	}
	f.m.toggleSection(secMemories, true)
	if _, blanks, _ := f.m.railSpans(); !blanks || shown() < before {
		t.Fatalf("collapsing memories at 80x24 must restore the blank rows without losing items (was %d, now %d)", before, shown())
	}
	// With a few spare rows they become items of the other sections.
	f.m.toggleSection(secMemories, false)
	f.send(tea.WindowSizeMsg{Width: 80, Height: 30})
	before = shown()
	f.m.toggleSection(secMemories, true)
	if after := shown(); after <= before {
		t.Fatalf("collapsing memories at 80x30 shows %d other items, was %d; want more", after, before)
	}
	if lines := f.railLines(); len(lines) > f.m.stream.Height()+1 {
		t.Fatalf("rail is %d rows, stream+1 is %d", len(lines), f.m.stream.Height()+1)
	}
}

func TestCollapseShownSectionMovesToHeading(t *testing.T) {
	f := newFixture(t)
	f.setRailChannels(
		bus.Channel{Name: "general", Kind: bus.KindOrdinary},
		bus.Channel{Name: "tmi", Kind: bus.KindMemory},
	)
	f.key("esc")
	f.moveTo(t, "tmi")
	f.m.toggleSection(secMemories, true)
	if f.m.headSel != secMemories {
		t.Fatalf("headSel=%d, want the memories heading", f.m.headSel)
	}
	if h := ansi.Strip(f.m.renderHeader()); !strings.Contains(h, "tmi") {
		t.Fatalf("header %q must still name tmi", h)
	}
	if f.m.selName() != "tmi" {
		t.Fatalf("the stream must keep tmi, got %q", f.m.selName())
	}
	// Collapsing a section that does not hold the shown item leaves the
	// selection alone.
	f.moveTo(t, "general")
	f.m.toggleSection(secTasks, true)
	if f.m.headSel >= 0 {
		t.Fatalf("headSel=%d after collapsing an unrelated section", f.m.headSel)
	}
}

func TestHeadingSelectedSingleHighlight(t *testing.T) {
	f := sectionedFixture(t)
	f.toHeading(t, secMemories)
	probe := sgrPrefix(lipgloss.NewStyle().Background(f.m.theme.SelBG(true)))
	if probe == "" {
		t.Skip("theme selection color has no escape under this profile")
	}
	var marked []string
	for _, l := range strings.Split(f.m.renderRails(), "\n") {
		if strings.Contains(l, probe) {
			marked = append(marked, ansi.Strip(l))
		}
	}
	if len(marked) != 1 || !strings.Contains(marked[0], "memories") {
		t.Fatalf("want exactly the memories heading highlighted, got %q", marked)
	}
}

func TestHeadingSurvivesRefresh(t *testing.T) {
	f := newFixture(t)
	chans := []bus.Channel{
		{Name: "general", Kind: bus.KindOrdinary},
		{Name: "tmi", Kind: bus.KindMemory},
		{Name: "a", Kind: bus.KindTasks},
	}
	f.setRailChannels(chans...)
	f.toHeading(t, secTasks)
	f.key("left")
	f.toHeading(t, secMemories)
	f.setRailChannels(append(chans, bus.Channel{Name: "b", Kind: bus.KindTasks})...)
	if !f.m.collapsed[secTasks] || f.m.headSel != secMemories {
		t.Fatalf("refresh must keep collapsed=%v and headSel=%d, got collapsed=%v headSel=%d", [railSections]bool{secTasks: true}, secMemories, f.m.collapsed, f.m.headSel)
	}
	if want := markSel + " tasks (2)"; !slices.Contains(f.railLines(), want) {
		t.Fatalf("a new task list must not expand tasks, want %q:\n%s", want, strings.Join(f.railLines(), "\n"))
	}
}

func TestHomeSelectsChannelsHeading(t *testing.T) {
	f := sectionedFixture(t)
	f.toHeading(t, secTags)
	f.key("down")
	f.key("home")
	if f.m.headSel != secChannels || f.m.pane() != paneChannels {
		t.Fatalf("home: headSel=%d pane=%v, want the channels heading", f.m.headSel, f.m.pane())
	}
	// From the sessions heading the stream stays where it was.
	f.toHeading(t, secSessions)
	shown := f.m.selName()
	f.key("home")
	if f.m.headSel != secChannels || f.m.selName() != shown {
		t.Fatalf("home from the sessions heading: headSel=%d sel=%q (was %q)", f.m.headSel, f.m.selName(), shown)
	}
}

func TestTabReturnsToHeading(t *testing.T) {
	f := sectionedFixture(t)
	f.toHeading(t, secMemories)
	shown := f.m.selName() // the last channel the walk passed
	f.samSend(shown, "hello")
	f.receive(t)
	f.key("tab")
	if f.m.pane() != paneStream || f.m.headSel != secMemories {
		t.Fatalf("tab from a heading goes to the stream: pane=%v headSel=%d", f.m.pane(), f.m.headSel)
	}
	f.key("tab") // compose
	f.key("tab") // rail
	if f.m.pane() != paneChannels || f.m.headSel != secMemories || f.m.selName() != shown {
		t.Fatalf("tab returns to the heading: pane=%v headSel=%d sel=%q, want %q", f.m.pane(), f.m.headSel, f.m.selName(), shown)
	}
}

func TestCollapseNotPersisted(t *testing.T) {
	f := newFixture(t)
	f.toHeading(t, secMemories)
	f.key("left")
	if !f.m.collapsed[secMemories] {
		t.Fatal("setup: memories not collapsed")
	}
	m := New(f.c, f.m.theme, Host{})
	if m.collapsed != [railSections]bool{} || m.headSel != -1 {
		t.Fatalf("a new Model must start expanded with no heading: collapsed=%v headSel=%d", m.collapsed, m.headSel)
	}
}

// A channel with the bridged capability carries the link icon wherever its
// kind icon is drawn; an unknown capability draws the plain chat icon.
func TestBridgedChannelIcon(t *testing.T) {
	f := newFixture(t)
	f.m.setChannels([]bus.Channel{
		{Name: "job1-chat", Kind: bus.KindOrdinary, Capabilities: []bus.ChannelCapability{bus.ChannelBridged}},
		{Name: "odd", Kind: bus.KindOrdinary, Capabilities: []bus.ChannelCapability{"future"}},
	}, nil, "oldest")
	rail := ansi.Strip(f.m.renderRails())
	if !strings.Contains(rail, iconBridged+"job1-chat") || !strings.Contains(rail, iconChat+"odd") {
		t.Fatalf("rail:\n%s", rail)
	}
	if got := ansi.Strip(f.m.channelLabel("job1-chat")); !strings.HasPrefix(got, iconBridged) {
		t.Fatalf("label %q", got)
	}
	if got := ansi.Strip(f.m.channelLabel("odd")); !strings.HasPrefix(got, iconChat) {
		t.Fatalf("label %q", got)
	}
	f.moveTo(t, "job1-chat")
	if got := ansi.Strip(f.m.renderCompose()); !strings.HasPrefix(got, iconBridged+"job1-chat") {
		t.Fatalf("compose %q", got)
	}
	hit := func(ch string) bus.SearchHit {
		return bus.SearchHit{Message: bus.Message{Channel: ch, Seq: 1, Content: "x"}}
	}
	f.m.search.hits = []bus.SearchHit{hit("job1-chat"), hit("odd")}
	rows := strings.Split(ansi.Strip(f.m.viewSearch()), "\n")
	var got []string
	for _, r := range rows {
		if strings.Contains(r, "#1") {
			got = append(got, r)
		}
	}
	if len(got) != 2 || !strings.Contains(got[0], iconBridged+"job1-chat") || !strings.Contains(got[1], iconChat+"odd") {
		t.Fatalf("search rows %q", got)
	}
}
