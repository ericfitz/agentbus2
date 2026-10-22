package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/store"
)

func TestHostHint(t *testing.T) {
	tests := []struct {
		name string
		host string
		err  error
		want string
	}{
		{"fqdn", "MacMini.local", nil, "macmini"},
		{"short", "laptop", nil, "laptop"},
		{"underscore", "host_name", nil, "host_name"},
		{"slash", "a/b", nil, ""},
		{"error", "macmini", errors.New("no hostname"), ""},
		{"empty", "", nil, ""},
		{"leading dot", ".local", nil, ""},
		{"200 bytes", strings.Repeat("a", 200), nil, ""},
		{"128 bytes", strings.Repeat("a", 128), nil, strings.Repeat("a", 128)},
		{"control", "a\x01b", nil, ""},
		{"invalid utf8", "a\xffb", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hostHintFrom(func() (string, error) { return tc.host, tc.err })
			if got != tc.want {
				t.Fatalf("hostHintFrom = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestValidHintMatchesNameRule: the mirror in validHint agrees with
// store.NameRule on every case, so a hint the TUI sends never fails Open.
func TestValidHintMatchesNameRule(t *testing.T) {
	for _, s := range []string{"", "a", "a/b", "a\x00b", "a\tb", "a\xffb", "hé", strings.Repeat("a", 128), strings.Repeat("a", 129), "host_name", " "} {
		if got, want := validHint(s), store.NameRule(s) == nil; got != want {
			t.Errorf("validHint(%q) = %v, store.NameRule ok = %v", s, got, want)
		}
	}
}
