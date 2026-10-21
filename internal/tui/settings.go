package tui

import (
	"errors"
	"strconv"
	"strings"
)

// Settings is the tui object of config.json, already loaded. It carries no
// JSON tags: internal/profile owns the file shape (Milestone 8 Task 8), and
// the entry fills this struct from it.
type Settings struct {
	Theme     string            // name of the entry in Themes to apply; "" and "default" mean the built-in default
	Themes    []ColorTheme      // named color sets
	Icons     string            // emoji (default), nerdfont or custom
	IconMap   map[string]string // icon name to glyph, applied over the base set
	RailWidth int               // 0 means the built-in width
}

// ColorTheme is one named set of TUI colors. Values are not validated here:
// LoadTheme substitutes DefaultTheme's value for any that is missing or not a
// color (see ColorIndex).
type ColorTheme struct {
	Name              string
	Background        string
	Text              string
	Dim               string
	Timestamp         string
	Tag               string
	Agent             string
	User              string
	Memory            string
	Tasks             string
	Health            string
	Warn              string
	Error             string
	Selection         string
	SelectionInactive string
	SelectionText     string
	Unsaved           string
}

// DefaultTheme is the built-in "default" theme and the per-value fallback
// (v1 config.DefaultTheme).
func DefaultTheme() ColorTheme {
	return ColorTheme{Name: "default", Background: "default", Text: "default", Dim: "brightblack", Timestamp: "brightblack", Tag: "brightblack", Agent: "cyan", User: "yellow", Memory: "magenta", Tasks: "yellow", Health: "green", Warn: "yellow", Error: "red", Selection: "blue", SelectionInactive: "brightblack", SelectionText: "black", Unsaved: "yellow"}
}

// Colors lists the theme's values as (key, value) pairs in a fixed order,
// keyed by the JSON names of the config file.
func (t ColorTheme) Colors() [][2]string {
	return [][2]string{
		{"background", t.Background},
		{"text", t.Text},
		{"dim", t.Dim},
		{"timestamp", t.Timestamp},
		{"tag", t.Tag},
		{"agent", t.Agent},
		{"user", t.User},
		{"memory", t.Memory},
		{"tasks", t.Tasks},
		{"health", t.Health},
		{"warn", t.Warn},
		{"error", t.Error},
		{"selection", t.Selection},
		{"selection_inactive", t.SelectionInactive},
		{"selection_text", t.SelectionText},
		{"unsaved", t.Unsaved},
	}
}

// FindTheme returns the entry of Themes named name, or false.
func (s Settings) FindTheme(name string) (ColorTheme, bool) {
	for _, t := range s.Themes {
		if t.Name == name {
			return t, true
		}
	}
	return ColorTheme{}, false
}

var ansiNames = []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}

// ColorForms is the accepted-values sentence for the TUI's theme warning.
const ColorForms = "black, red, green, yellow, blue, magenta, cyan, white, their bright forms such as brightblack, 0-15, or default"

// ColorIndex parses a color setting: the sixteen ANSI names (bright forms
// prefixed with "bright"), the integers 0-15, or "default" (-1);
// case-insensitive, surrounding whitespace ignored. Only ANSI colors are
// accepted on purpose: #RGB is rejected so a later version can add it
// without silently changing behavior.
func ColorIndex(s string) (int, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "default" {
		return -1, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 || n > 15 {
			return 0, errors.New("index out of range")
		}
		return n, nil
	}
	base, bright := strings.CutPrefix(v, "bright")
	for i, name := range ansiNames {
		if base == name {
			if bright {
				i += 8
			}
			return i, nil
		}
	}
	return 0, errors.New("unknown color")
}
