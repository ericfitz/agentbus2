//go:build windows

package tui

import (
	"strings"
	"testing"
)

// Windows Terminal advances two cells for the gear, as lipgloss counts, so
// a padded gear (v1's CSI 1C, Terminal.app's U+200B) overflows it: on
// Windows the icon is the plain emoji and a space.
func TestGearIconHasNoEscapesWindows(t *testing.T) {
	if strings.Contains(gearIcon, "\x1b") || gearIcon != "\u2699\uFE0F " {
		t.Fatalf("gearIcon = %q", gearIcon)
	}
}
