package tui

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestColorIndexForms(t *testing.T) {
	ok := map[string]int{"cyan": 6, "BrightBlack": 8, " 15 ": 15, "default": -1, "0": 0, "brightwhite": 15}
	for in, want := range ok {
		if got, err := ColorIndex(in); err != nil || got != want {
			t.Errorf("ColorIndex(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	bad := map[string]string{"16": "index out of range", "-1": "index out of range", "#fff": "unknown color", "orange": "unknown color", "": "unknown color", "brightdefault": "unknown color"}
	for in, want := range bad {
		if _, err := ColorIndex(in); err == nil || err.Error() != want {
			t.Errorf("ColorIndex(%q) error = %v; want %q", in, err, want)
		}
	}
}

// The two keys default to brightblack and yellow, override like any other,
// and reach the Health overlay's Sources.
func TestThemeSelectionInactiveAndUnsavedKeys(t *testing.T) {
	th := LoadTheme(Settings{}, io.Discard)
	if th.SelInactive != lipgloss.Color("8") || th.Unsaved != lipgloss.Color("3") {
		t.Fatalf("defaults: selection_inactive=%v unsaved=%v", th.SelInactive, th.Unsaved)
	}
	if th.SelBG(true) != th.Sel || th.SelBG(false) != th.SelInactive {
		t.Fatalf("SelBG: focused=%v unfocused=%v", th.SelBG(true), th.SelBG(false))
	}
	d := DefaultTheme()
	d.SelectionInactive, d.Unsaved = "green", "default"
	th = LoadTheme(Settings{Themes: []ColorTheme{d}}, io.Discard)
	if th.SelInactive != lipgloss.Color("2") || th.Unsaved != (lipgloss.NoColor{}) || th.Sources["selection_inactive"] != "green" || th.Sources["unsaved"] != "default" {
		t.Fatalf("overrides: %+v", th)
	}
}

// selection_text defaults to black and overrides like any other key.
func TestThemeSelectionTextKey(t *testing.T) {
	th := LoadTheme(Settings{}, io.Discard)
	if th.SelText != lipgloss.Color("0") {
		t.Fatalf("default: selection_text=%v", th.SelText)
	}
	d := DefaultTheme()
	d.SelectionText = "white"
	th = LoadTheme(Settings{Themes: []ColorTheme{d}}, io.Discard)
	if th.SelText != lipgloss.Color("7") || th.Sources["selection_text"] != "white" {
		t.Fatalf("override: %+v", th)
	}
}

func TestParseColorAcceptsNamesIndicesAndDefault(t *testing.T) {
	cases := map[string]lipgloss.TerminalColor{
		"cyan":        lipgloss.Color("6"),
		"CYAN":        lipgloss.Color("6"),
		"brightblack": lipgloss.Color("8"),
		"BrightWhite": lipgloss.Color("15"),
		"0":           lipgloss.Color("0"),
		"15":          lipgloss.Color("15"),
		"default":     lipgloss.NoColor{},
		" yellow ":    lipgloss.Color("3"),
	}
	for in, want := range cases {
		got, err := ParseColor(in)
		if err != nil || got != want {
			t.Errorf("ParseColor(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}

func TestParseColorRejectsHexAndJunk(t *testing.T) {
	for _, in := range []string{"#f00", "#ff0000", "16", "-1", "orange", ""} {
		if _, err := ParseColor(in); err == nil {
			t.Errorf("ParseColor(%q) accepted", in)
		}
	}
}

func TestLoadThemeFallsBackPerValue(t *testing.T) {
	s := Settings{Theme: "night", Themes: []ColorTheme{{Name: "night", User: "Blue", Agent: "#00f", Memory: "#ff00ff"}}}
	var warn bytes.Buffer
	th := LoadTheme(s, &warn)
	if th.Name != "night" || th.Agent != lipgloss.Color("6") || th.User != lipgloss.Color("4") {
		t.Fatalf("named theme not applied: %+v", th)
	}
	if th.Sources["agent"] != "cyan" || th.Sources["memory"] != "magenta" || th.Sources["user"] != "blue" || th.Sources["dim"] != "brightblack" {
		t.Fatalf("sources wrong: %v", th.Sources)
	}
	if th.Dim != lipgloss.Color("8") || th.BG != (lipgloss.NoColor{}) {
		t.Fatalf("missing values must fall back to the defaults: %+v", th)
	}
	out := warn.String()
	if strings.Count(out, "\n") != 2 {
		t.Fatalf("exactly one line per bad value, none for missing ones, got %q", out)
	}
	for _, want := range []string{`agentbus2 tui: theme "night" agent="#00f" is not a color (unknown color); accepted: ` + ColorForms + "; using cyan\n", `memory="#ff00ff"`, "using magenta"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning lacks %q: %q", want, out)
		}
	}
}

func TestLoadThemeUnknownName(t *testing.T) {
	var warn bytes.Buffer
	th := LoadTheme(Settings{Theme: "nope", Themes: []ColorTheme{{Name: "night"}}}, &warn)
	if th.Name != "default" || th.Agent != lipgloss.Color("6") {
		t.Fatalf("unknown theme name must use the default theme: %+v", th)
	}
	want := `agentbus2 tui: theme "nope" is not in themes; using default` + "\n"
	if warn.String() != want {
		t.Fatalf("warning = %q, want %q", warn.String(), want)
	}
}

func TestLoadThemeEmptyIsDefault(t *testing.T) {
	for _, name := range []string{"", "default"} {
		var warn bytes.Buffer
		th := LoadTheme(Settings{Theme: name}, &warn)
		if warn.Len() != 0 || th.Name != "default" || th.Sources["agent"] != "cyan" {
			t.Errorf("theme %q: name=%q warn=%q", name, th.Name, warn.String())
		}
	}
}

// A themes entry named default overrides the built-in one.
func TestLoadThemeDefaultEntryOverrides(t *testing.T) {
	d := DefaultTheme()
	d.Agent = "green"
	th := LoadTheme(Settings{Themes: []ColorTheme{d}}, io.Discard)
	if th.Agent != lipgloss.Color("2") {
		t.Fatalf("agent = %v", th.Agent)
	}
}

func TestRailSelNeverWraps(t *testing.T) {
	prev := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	th := LoadTheme(Settings{}, io.Discard)
	line := strings.Repeat("x", 40)
	for _, focused := range []bool{true, false} {
		if got := th.RailSel(focused, cut(line, 16, "…"), 16); strings.Contains(got, "\n") {
			t.Errorf("focused=%v wrapped: %q", focused, got)
		}
		if got := th.RailSel(focused, line, 16); strings.Contains(got, "\n") {
			t.Errorf("focused=%v uncut line wrapped: %q", focused, got)
		}
	}
}

func TestChipTextContrastsWithTagColor(t *testing.T) {
	for bg, want := range map[string]string{"7": "0", "11": "0", "15": "0", "3": "0", "4": "15", "8": "15", "1": "15"} {
		if got := chipText(lipgloss.Color(bg)); got != lipgloss.Color(want) {
			t.Errorf("tag %s: chip text %v, want %s", bg, got, want)
		}
	}
}

// RailSel on a focused row strips the row's colors to the selection text
// color; unfocused keeps the original colors.
func TestRailSelFocusedStripsColors(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(2) // termenv.ANSI
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	th := LoadTheme(Settings{}, io.Discard)
	colored := th.Style(th.Agent).Render("name")
	if got := ansi.Strip(th.RailSel(true, colored, 10)); strings.TrimSpace(got) != "name" {
		t.Fatalf("focused text = %q", got)
	}
	if got := th.RailSel(false, colored, 10); !strings.Contains(got, th.Style(th.Agent).Render("name")[:5]) {
		t.Fatalf("unfocused row lost its agent color: %q", got)
	}
}

func TestThemeTimestampAndTagKeys(t *testing.T) {
	th := LoadTheme(Settings{}, io.Discard)
	if th.Stamp != lipgloss.Color("8") || th.Tag != lipgloss.Color("8") {
		t.Fatalf("defaults: stamp=%v tag=%v", th.Stamp, th.Tag)
	}
	d := DefaultTheme()
	d.Timestamp, d.Tag = "green", "default"
	th = LoadTheme(Settings{Themes: []ColorTheme{d}}, io.Discard)
	if th.Stamp != lipgloss.Color("2") || th.Tag != (lipgloss.NoColor{}) || th.Sources["tag"] != "default" {
		t.Fatalf("overrides: %+v", th)
	}
}
