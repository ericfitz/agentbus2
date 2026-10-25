package tui

import (
	"fmt"
	"io"
	"maps"
	"strings"

	"charm.land/lipgloss/v2"
)

// Rail icons. Each carries U+FE0F so the terminal draws the color emoji
// even when its monospace font has its own glyph at that codepoint. These
// are vars, not consts: LoadIcons overwrites them per Settings.Icons.
var (
	iconChat  = "\U0001F4AC️ " // speech balloon
	iconMem   = "\U0001F4BE️ " // floppy disk
	iconAgent = gearIcon       // gear; Terminal.app gets appleTerminalIcons' variant
	iconUser  = "\U0001F9D1️ " // adult
	iconIdle  = "\U0001F4A4️ " // sleeping sign
	iconTasks = "\U0001F4CB️ " // clipboard; Wide like chat and memory, so no CSI trick
	iconArrow = " → "          // rightwards arrow; ambiguous width, 1 column in Western setups
	iconError = "✗ "           // ballot X, the toast and overlay-footer error prefix
)

// Task status marks. The stopwatch U+23F1 is text-presentation by default
// (like the gear) and carries U+FE0F for the color glyph; Terminal.app gets
// appleTerminalIcons' variant for the same reason as the gear.
var (
	taskPending    = "❎ "          // negative squared cross mark
	taskInProgress = stopwatchIcon // stopwatch
	taskCompleted  = "✅ "          // white heavy check mark
)

// The gear and the stopwatch are Emoji_Presentation=No with U+FE0F: lipgloss
// and Bubble Tea v2's renderer (grapheme width, mode 2027 forced in run.go)
// count them two cells, as Windows Terminal, iTerm2 and Ghostty draw them.
// Terminal.app draws the color glyph two cells wide but advances the cursor
// one (CPR-measured 2026-10-25, with and without mode 2027), so every cell
// after the glyph lands a column left: the rail border broke on gear rows.
// v1 padded the advance with CSI 1C inside the string; the v2 renderer
// keeps only SGR and OSC 8 from the view, so an escape cannot do it. The
// Terminal.app variant pads with U+200B instead: lipgloss and the renderer
// count the format character zero and fold it into the glyph's cell, and
// Terminal.app advances one for it (measured), so all three agree on three
// columns and the glyph's second cell stays blank, not clipped. The pad is
// only sent to Terminal.app (TERM_PROGRAM=Apple_Terminal): on a terminal
// that counts U+200B zero the plain string is right, and one that counts
// both it and the glyph would overflow the row.
const (
	gearIcon      = "\u2699\uFE0F "
	stopwatchIcon = "\u23F1\uFE0F "
	narrowVS16Pad = "\u200B"
)

// appleTerminalIcons is the emoji set's Terminal.app variant: the two icons
// Terminal.app advances one column for, padded to the three it counts.
var appleTerminalIcons = map[string]string{
	"agent":            "\u2699\uFE0F" + narrowVS16Pad + " ",
	"task_in_progress": "\u23F1\uFE0F" + narrowVS16Pad + " ",
}

// isAppleTerminal reports whether env (KEY=VALUE pairs, os.Environ's form)
// names Terminal.app. TERM_PROGRAM does not cross ssh, so a TUI on a remote
// host gets the plain set; icon_map can set the padded strings there.
func isAppleTerminal(env []string) bool {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "TERM_PROGRAM="); ok {
			return v == "Apple_Terminal"
		}
	}
	return false
}

// markSel marks the selected list item and a message row with something
// hidden (its body, its replies, or both); markOpen marks a row whose body
// is open, or whose replies are shown with nothing left hidden.
var (
	markSel  = "▶" // ▶
	markOpen = "▼" // ▼
)

// iconVars maps each icon name an icon_map may use to the variable it sets.
var iconVars = map[string]*string{
	"chat": &iconChat, "memory": &iconMem, "agent": &iconAgent, "user": &iconUser,
	"idle": &iconIdle, "tasks": &iconTasks, "arrow": &iconArrow, "error": &iconError,
	"task_pending": &taskPending, "task_in_progress": &taskInProgress, "task_completed": &taskCompleted,
	"collapsed": &markSel, "expanded": &markOpen,
}

// emojiIcons is the default set, captured from the variables' initial values.
var emojiIcons = func() map[string]string {
	s := map[string]string{}
	for k, p := range iconVars {
		s[k] = *p
	}
	return s
}()

// nerdIcons is the Font Awesome iconography at the codepoints Nerd Fonts 3.x
// gives it. Nerd Fonts carries Font Awesome Free in its "regular" (FA4 _o)
// or solid form; the comment names the Nerd Font glyph where it differs
// from the requested FA6 name.
var nerdIcons = map[string]string{
	"chat":             "\uf27a",     // fa-message
	"memory":           "\uf0c7",     // fa-floppy-disk (nf fa-save)
	"tasks":            "\uf0ae",     // fa-list-check (nf fa-tasks, its FA5 name)
	"agent":            "\uee0d",     // fa-robot
	"user":             "\uf007",     // fa-user (solid; no regular user in Nerd Fonts)
	"idle":             "\U000f04b2", // md-sleep "zZ"; fa-face-sleeping is Pro-only
	"task_pending":     "\uf096",     // fa-square regular (nf fa-square_o)
	"task_in_progress": "\uf152",     // fa-square-caret-right regular (nf fa-toggle_right)
	"task_completed":   "\uf14a",     // fa-square-check solid (nf fa-square_check); FA4's check_square_o U+F046 is drawn 20% smaller in Mono patches
	"collapsed":        "\uf0da",     // fa-caret-right
	"expanded":         "\uf0d7",     // fa-caret-down
	"error":            "\uf06a",     // fa-circle-exclamation (nf fa-exclamation_circle)
	"arrow":            "\uf061",     // fa-arrow-right
}

// padIcon pads a glyph to its emoji counterpart's layout: the wide icons
// take three columns (two for the emoji, one space) so rails and task rows
// line up whichever set is active; the arrow gets a space on each side, the
// error prefix one trailing space, and the thread marks stay bare.
func padIcon(name, g string) string {
	switch name {
	case "arrow":
		return " " + g + " "
	case "error":
		return g + " "
	case "collapsed", "expanded":
		return g
	}
	return g + strings.Repeat(" ", max(3-lipgloss.Width(g), 1))
}

func setIcons(set map[string]string) {
	for k, p := range iconVars {
		*p = set[k]
	}
}

// LoadIcons applies s.Icons: emoji (default), nerdfont, or custom (a
// backward-compatible alias for emoji), then applies s.IconMap on top of
// whichever base set was chosen, overriding any subset of it. An unknown
// set, an unknown icon_map name, or an empty value warns one line and skips
// that entry (leaving the base set's icon in place), like LoadTheme. It is
// LoadIconsFor with no environment: the plain emoji set, whatever terminal
// the process runs in.
func LoadIcons(s Settings, warn io.Writer) string {
	return LoadIconsFor(s, nil, warn)
}

// LoadIconsFor is LoadIcons for the terminal env describes: in Terminal.app
// (TERM_PROGRAM=Apple_Terminal) the emoji set takes appleTerminalIcons'
// variants before s.IconMap applies, so an icon_map entry still wins.
func LoadIconsFor(s Settings, env []string, warn io.Writer) string {
	name := strings.ToLower(strings.TrimSpace(s.Icons))
	set := maps.Clone(emojiIcons)
	switch name {
	case "", "emoji", "custom":
		if name == "" {
			name = "emoji"
		}
		if isAppleTerminal(env) {
			maps.Copy(set, appleTerminalIcons)
		}
	case "nerdfont":
		set = map[string]string{}
		for k, g := range nerdIcons {
			set[k] = padIcon(k, g)
		}
	default:
		_, _ = fmt.Fprintf(warn, "agentbus2 tui: icons %q is not emoji, nerdfont, or custom; using emoji\n", s.Icons)
		name = "emoji"
	}
	for k, g := range s.IconMap {
		if _, ok := emojiIcons[k]; !ok || g == "" {
			_, _ = fmt.Fprintf(warn, "agentbus2 tui: icon_map %q=%q is not a known icon with a value; keeping the current icon\n", k, g)
			continue
		}
		set[k] = padIcon(k, g)
	}
	setIcons(set)
	return name
}
