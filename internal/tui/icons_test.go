package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestLoadIconsEmojiIsDefaultAndByteIdentical(t *testing.T) {
	var warn bytes.Buffer
	if got := LoadIcons(Settings{}, &warn); got != "emoji" {
		t.Fatalf("name = %q, want emoji", got)
	}
	if warn.Len() != 0 {
		t.Fatalf("unexpected warning: %q", warn.String())
	}
	for k, p := range iconVars {
		if *p != emojiIcons[k] {
			t.Fatalf("%s = %q, want emoji value %q", k, *p, emojiIcons[k])
		}
	}
}

// TestLoadIconsNerdfontWidthParity checks that every nerdfont glyph is
// padded to the same rail/task-row width as its emoji counterpart: the wide
// icons to 3 columns, the thread marks to 1. (The half that renders a task
// list and the rail is Task 5's TestNerdfontTaskRowWidthParity.)
func TestLoadIconsNerdfontWidthParity(t *testing.T) {
	t.Cleanup(func() { setIcons(emojiIcons) })
	var warn bytes.Buffer
	if got := LoadIcons(Settings{Icons: "nerdfont"}, &warn); got != "nerdfont" || warn.Len() != 0 {
		t.Fatalf("name = %q, warn = %q", got, warn.String())
	}
	for name, p := range iconVars {
		switch name {
		case "arrow", "error", "collapsed", "expanded":
			continue
		}
		if w := lipgloss.Width(*p); w != 3 {
			t.Errorf("%s width = %d, want 3 (%q)", name, w, *p)
		}
	}
	if w := lipgloss.Width(markSel); w != 1 {
		t.Errorf("collapsed width = %d, want 1", w)
	}
	if w := lipgloss.Width(markOpen); w != 1 {
		t.Errorf("expanded width = %d, want 1", w)
	}
	if iconArrow != " \uf061 " {
		t.Errorf("nerdfont iconArrow = %q, want %q", iconArrow, " \uf061 ")
	}
	if iconError != "\uf06a " {
		t.Errorf("nerdfont iconError = %q, want %q", iconError, "\uf06a ")
	}
}

// widths is each line's display width; Task 5's TestNerdfontTaskRowWidthParity
// uses it.
func widths(s string) []int {
	lines := strings.Split(s, "\n")
	out := make([]int, len(lines))
	for i, l := range lines {
		out[i] = lipgloss.Width(l)
	}
	return out
}

func TestWidths(t *testing.T) {
	got := widths("ab\n⚙️ x\n")
	if len(got) != 3 || got[0] != 2 || got[2] != 0 {
		t.Fatalf("widths = %v", got)
	}
}

// icon_map applies on top of whichever base set is chosen, not only custom.
func TestLoadIconsNerdfontWithIconMapOverridesOneIcon(t *testing.T) {
	t.Cleanup(func() { setIcons(emojiIcons) })
	var warn bytes.Buffer
	s := Settings{Icons: "nerdfont", IconMap: map[string]string{"chat": "X"}}
	if got := LoadIcons(s, &warn); got != "nerdfont" || warn.Len() != 0 {
		t.Fatalf("name = %q, warn = %q", got, warn.String())
	}
	if iconChat != "X  " {
		t.Fatalf("iconChat = %q, want padded %q", iconChat, "X  ")
	}
	wantMem := padIcon("memory", nerdIcons["memory"])
	if iconMem != wantMem {
		t.Fatalf("iconMem = %q, want nerdfont %q (icon_map must not touch names it doesn't set)", iconMem, wantMem)
	}
}

func TestLoadIconsCustomOverridesSubset(t *testing.T) {
	t.Cleanup(func() { setIcons(emojiIcons) })
	var warn bytes.Buffer
	s := Settings{Icons: "custom", IconMap: map[string]string{"chat": "X", "bogus": "Y", "user": ""}}
	if got := LoadIcons(s, &warn); got != "custom" {
		t.Fatalf("name = %q, want custom", got)
	}
	if iconChat != "X  " {
		t.Fatalf("iconChat = %q, want padded %q", iconChat, "X  ")
	}
	if iconUser != emojiIcons["user"] {
		t.Fatalf("iconUser = %q, want emoji fallback %q", iconUser, emojiIcons["user"])
	}
	if n := strings.Count(warn.String(), "\n"); n != 2 {
		t.Fatalf("want exactly two warning lines, got %d: %q", n, warn.String())
	}
	if !strings.Contains(warn.String(), "agentbus2 tui: icon_map ") {
		t.Fatalf("warning prefix: %q", warn.String())
	}
}

// The completed glyph is U+F14A (fa-square-check), drawn 600x600 in the Mono
// patches like the pending (U+F096) and in-progress (U+F152) glyphs. FA4's
// U+F046 check_square_o is 480x480 there and looked a size smaller.
func TestNerdfontCompletedIconFillsTheCell(t *testing.T) {
	if nerdIcons["task_completed"] != "\uf14a" {
		t.Fatalf("task_completed = %q, want U+F14A", nerdIcons["task_completed"])
	}
}

func TestLoadIconsUnknownSetWarnsAndFallsBackToEmoji(t *testing.T) {
	var warn bytes.Buffer
	if got := LoadIcons(Settings{Icons: "fancy"}, &warn); got != "emoji" {
		t.Fatalf("name = %q, want emoji", got)
	}
	if n := strings.Count(warn.String(), "\n"); n != 1 {
		t.Fatalf("want exactly one warning line, got %d: %q", n, warn.String())
	}
	if !strings.Contains(warn.String(), `"fancy"`) || !strings.HasPrefix(warn.String(), "agentbus2 tui: ") {
		t.Fatalf("warning must name the bad value: %q", warn.String())
	}
	for k, p := range iconVars {
		if *p != emojiIcons[k] {
			t.Fatalf("%s = %q, want emoji value %q", k, *p, emojiIcons[k])
		}
	}
}

// Every icon name has an emoji value and a nerdfont value, and the two maps
// have the same keys: a name in one and not the other would load as "".
func TestIconSetsCoverTheSameNames(t *testing.T) {
	for k := range iconVars {
		if emojiIcons[k] == "" {
			t.Errorf("emoji set lacks %q", k)
		}
		if nerdIcons[k] == "" {
			t.Errorf("nerdfont set lacks %q", k)
		}
	}
	if len(nerdIcons) != len(iconVars) || len(emojiIcons) != len(iconVars) {
		t.Errorf("sizes: nerd %d emoji %d vars %d", len(nerdIcons), len(emojiIcons), len(iconVars))
	}
}
