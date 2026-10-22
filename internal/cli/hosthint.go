package cli

import (
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// osHostname is os.Hostname; tests replace it.
var osHostname = os.Hostname

// hostHint is the TUI session's hint (TUI decision 12): the host's short name,
// lowercased and cut at the first '.', so one human on two hosts shows as
// <identity>/macmini and <identity>/laptop. It returns "" (the TUI then uses
// "tui") when the hostname is unavailable or the short name fails the name
// rule the server applies to a hint.
func hostHint() string { return hostHintFrom(osHostname) }

func hostHintFrom(hostname func() (string, error)) string {
	h, err := hostname()
	if err != nil || !utf8.ValidString(h) { // ToLower would turn invalid bytes into U+FFFD
		return ""
	}
	h, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(h)), ".")
	if !validHint(h) {
		return ""
	}
	return h
}

// validHint mirrors store.NameRule (internal/cli may not import
// internal/store): 1-128 bytes, valid UTF-8, no NUL, no control characters, no
// '/'. TestValidHintMatchesNameRule keeps the two equal.
func validHint(s string) bool {
	if len(s) == 0 || len(s) > 128 || !utf8.ValidString(s) || strings.ContainsRune(s, '/') {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
