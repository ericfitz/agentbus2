package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	default:
		return fmt.Sprintf("%d KiB", n>>10)
	}
}

// clock renders a message time as HH:MM for today, else "Sep 7".
func clock(ms int64) string {
	t := time.UnixMilli(ms).Local()
	if y, m, d := t.Date(); y == time.Now().Year() && m == time.Now().Month() && d == time.Now().Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2")
}

// stamp renders a message time as "YYYY-MM-DD HH:MM:SS"; today's date is
// replaced by "(today)" padded to the same ten columns so times line up.
func stamp(ms int64) string {
	t := time.UnixMilli(ms).Local()
	date := t.Format("2006-01-02")
	if now := time.Now(); t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		date = "(today)   "
	}
	return date + " " + t.Format("15:04:05")
}

// cut is ansi.Truncate(s, w, tail) for one line, except that past the cut
// only SGR escapes are kept. ansi.Truncate keeps the escapes of every glyph
// it drops, and a cursor escape (v1's gear carried a CSI 1C) left past the
// cut would move the cursor a column past w, which lipgloss counts as zero
// width. SGR stays so styles still close.
func cut(s string, w int, tail string) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	room := w - ansi.StringWidth(tail)
	var b strings.Builder
	done := false
	state := byte(ansi.NormalState)
	for rest := s; rest != ""; {
		seq, width, n, next := ansi.DecodeSequence(rest, state, nil)
		state, rest = next, rest[n:]
		switch {
		case strings.HasPrefix(seq, "\x1b"):
			if !done || strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				b.WriteString(seq)
			}
		case done:
		case width <= room:
			b.WriteString(seq)
			room -= width
		default:
			done = true
			if room >= 0 {
				b.WriteString(tail)
			}
		}
	}
	return b.String()
}

func shortDur(d time.Duration) string {
	if d < time.Hour {
		return strconv.Itoa(int(d.Minutes())) + "m"
	}
	return strconv.Itoa(int(d.Hours())) + "h"
}

// window returns the [start, end) slice bounds that keep cursor visible
// among n items when only visible of them fit, centering cursor when the
// list is longer than that.
func window(cursor, n, visible int) (start, end int) {
	start = 0
	if n > visible {
		start = max(0, min(cursor-visible/2, n-visible))
	}
	return start, min(start+visible, n)
}
