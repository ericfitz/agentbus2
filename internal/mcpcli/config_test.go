package mcpcli

import (
	"testing"
)

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"a-b_c.d/e:f": "a-b_c.d/e:f",
		"my key":      "'my key'",
		"it's":        `'it'\''s'`,
		"":            "''",
		"a$b":         "'a$b'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
