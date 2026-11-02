package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/profile"
)

// TestOpenLocalWithoutOpener: a binary that passes no Options.Local has no
// local mode; a local profile is a ResolveError and nothing is created.
func TestOpenLocalWithoutOpener(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		define  bool
	}{
		{"built-in default", "", false},
		{"defined local profile", "lab", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if tc.define {
				e.save(t, profile.Profile{Name: "lab", Mode: profile.ModeLocal})
			}
			_, err := Open(context.Background(), Options{Environ: e.environ, Cwd: t.TempDir(), Profile: tc.profile, Identity: "pat"})
			var re *ResolveError
			if !errors.As(err, &re) || !strings.Contains(err.Error(), "has no local mode") {
				t.Fatalf("Open = %v, want a ResolveError saying the binary has no local mode", err)
			}
			if _, statErr := os.Stat(filepath.Join(e.data, "agentbus2.sqlite")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("the data directory holds agentbus2.sqlite (%v)", statErr)
			}
			matches, _ := filepath.Glob(filepath.Join(e.data, "*.sqlite*"))
			if len(matches) != 0 {
				t.Fatalf("the data directory holds %v", matches)
			}
		})
	}
}

func TestNoLocalModeError(t *testing.T) {
	err := NoLocalModeError("lab")
	var re *ResolveError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), `profile "lab" is a local profile`) || !strings.Contains(err.Error(), "pass -server URL, or use agentbus2") {
		t.Fatalf("NoLocalModeError = %v", err)
	}
}
