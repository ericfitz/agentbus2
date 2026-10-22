package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Tag chips are " tag " on the tag color; on a terminal that shows no color
// (Bubble Tea's ColorProfileMsg reports NoTTY or Ascii) a chip would be
// invisible, so they fall back to dim "#tag" words, as Lip Gloss v1 did on
// its Ascii profile. Any color profile brings the chips back.
func TestTagChipsFallBackWithoutColor(t *testing.T) {
	f := newFixture(t)
	chips := f.m.tagChips([]string{"a", "b"})
	chip := lipgloss.NewStyle().Background(f.m.theme.Tag).Foreground(chipText(f.m.theme.Tag))
	if stripANSI(chips) != " a   b " || !strings.HasPrefix(chips, sgrPrefix(chip)) {
		t.Fatalf("colored chips = %q", chips)
	}
	for _, p := range []colorprofile.Profile{colorprofile.NoTTY, colorprofile.Ascii} {
		f.send(tea.ColorProfileMsg{Profile: p})
		if got := stripANSI(f.m.tagChips([]string{"a", "b"})); got != "#a #b" {
			t.Errorf("profile %v: chips = %q, want the #tag words", p, got)
		}
	}
	for _, p := range []colorprofile.Profile{colorprofile.ANSI, colorprofile.ANSI256, colorprofile.TrueColor} {
		f.send(tea.ColorProfileMsg{Profile: p})
		if got := f.m.tagChips([]string{"a", "b"}); got != chips {
			t.Errorf("profile %v: chips = %q, want %q", p, got, chips)
		}
	}
	// A "default" tag color draws no background on any profile.
	f.m.theme.Tag = lipgloss.NoColor{}
	if got := stripANSI(f.m.tagChips([]string{"a"})); got != "#a" {
		t.Errorf("default tag color: chips = %q, want #a", got)
	}
}

// The compose line takes Bubbles' light or dark defaults from the
// terminal's answer to Init's background query (dark until it answers, as
// Bubbles v1 assumed), and its cursor never blinks.
func TestComposeStylesFollowBackground(t *testing.T) {
	f := newFixture(t)
	if got, want := f.m.compose.Styles(), composeStyles(true); got.Focused.CursorLine.GetBackground() != want.Focused.CursorLine.GetBackground() {
		t.Fatalf("initial compose styles are not the dark defaults")
	}
	f.send(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	if got, want := f.m.compose.Styles(), composeStyles(false); got.Focused.CursorLine.GetBackground() != want.Focused.CursorLine.GetBackground() {
		t.Fatalf("a light background did not switch the compose styles")
	}
	f.send(tea.BackgroundColorMsg{Color: lipgloss.Color("#000000")})
	if got, want := f.m.compose.Styles(), composeStyles(true); got.Focused.CursorLine.GetBackground() != want.Focused.CursorLine.GetBackground() {
		t.Fatalf("a dark background did not switch the compose styles back")
	}
	for _, dark := range []bool{true, false} {
		if s := composeStyles(dark); s.Cursor.Blink {
			t.Errorf("dark=%v: the compose cursor blinks", dark)
		}
	}
	if promptStyles().Cursor.Blink {
		t.Error("the prompt cursor blinks")
	}
}
