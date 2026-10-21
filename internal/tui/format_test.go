package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestFmtBytes(t *testing.T) {
	if got := fmtBytes(412 << 20); got != "412 MiB" {
		t.Fatal(got)
	}
	if got := fmtBytes(264 << 10); got != "264 KiB" {
		t.Fatal(got)
	}
	if got := fmtBytes(2 << 30); got != "2.0 GiB" {
		t.Fatal(got)
	}
}

func TestStampTodayAndPast(t *testing.T) {
	if got := stamp(time.Now().UnixMilli()); !strings.HasPrefix(got, "(today)    ") || len([]rune(got)) != 19 {
		t.Fatalf("today: %q", got)
	}
	past := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local).UnixMilli()
	if got := stamp(past); got != "2026-01-02 03:04:05" {
		t.Fatalf("past: %q", got)
	}
}

func TestClockTodayAndPast(t *testing.T) {
	if got := clock(time.Now().UnixMilli()); len(got) != 5 || got[2] != ':' {
		t.Fatalf("today: %q", got)
	}
	past := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local).UnixMilli()
	if got := clock(past); got != "Jan 2" {
		t.Fatalf("past: %q", got)
	}
}

func TestItoa(t *testing.T) {
	if itoa(-12) != "-12" || itoa(0) != "0" {
		t.Fatal(itoa(-12), itoa(0))
	}
}

func TestShortDur(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "0m", 59 * time.Minute: "59m", time.Hour: "1h", 90 * time.Minute: "1h", 49 * time.Hour: "49h"} {
		if got := shortDur(d); got != want {
			t.Errorf("shortDur(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestWindow(t *testing.T) {
	cases := []struct{ cursor, n, visible, start, end int }{
		{0, 3, 5, 0, 3},   // everything fits
		{0, 10, 4, 0, 4},  // top
		{5, 10, 4, 3, 7},  // centered
		{9, 10, 4, 6, 10}, // bottom
		{0, 0, 4, 0, 0},   // empty
	}
	for _, c := range cases {
		if s, e := window(c.cursor, c.n, c.visible); s != c.start || e != c.end {
			t.Errorf("window(%d, %d, %d) = %d, %d; want %d, %d", c.cursor, c.n, c.visible, s, e, c.start, c.end)
		}
	}
}

func TestCutPlainAndStyled(t *testing.T) {
	if got := cut("hello", 10, "…"); got != "hello" {
		t.Fatalf("fits: %q", got)
	}
	if got := cut("hello world", 6, "…"); got != "hello…" {
		t.Fatalf("plain: %q", got)
	}
	styled := "\x1b[31mhello world\x1b[0m"
	got := cut(styled, 6, "…")
	if ansi.Strip(got) != "hello…" || !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("styled must cut and still close: %q", got)
	}
}

// When the cut lands before the gear, ansi.Truncate keeps the glyph's
// escapes; the gear's CSI 1C past the ellipsis would push the line a column
// wider than counted. Only SGR may follow the ellipsis.
func TestCutDropsCursorEscapesPastEllipsis(t *testing.T) {
	line := "abc" + "\x1b[2X⚙️\x1b[1C " + "tail"
	for w := 2; w < ansi.StringWidth(line); w++ {
		got := cut(line, w, "…")
		i := strings.LastIndex(got, "…")
		if i < 0 {
			t.Fatalf("w=%d: no ellipsis: %q", w, got)
		}
		for rest := got[i+len("…"):]; rest != ""; {
			seq, _, n, _ := ansi.DecodeSequence(rest, ansi.NormalState, nil)
			if !strings.HasPrefix(seq, "\x1b[") || !strings.HasSuffix(seq, "m") {
				t.Fatalf("w=%d: %q after the ellipsis: %q", w, seq, got)
			}
			rest = rest[n:]
		}
		if ansi.StringWidth(got) > w {
			t.Fatalf("w=%d: width %d: %q", w, ansi.StringWidth(got), got)
		}
	}
}
