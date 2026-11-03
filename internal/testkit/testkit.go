// Package testkit holds helpers shared by several packages' tests: a
// mutex-guarded output buffer and the temp config and data directories of a
// profile environment. It is imported by test code only
// (TestTestkitTestOnly), and imports only the standard library and
// internal/profile.
package testkit

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ericfitz/agentmesh/internal/profile"
)

// LockedBuffer is a buffer a goroutine other than the test's can write to
// while the test reads it. The zero value is ready to use.
type LockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write appends p.
func (l *LockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// String returns everything written so far.
func (l *LockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// ProfileEnv is temp home, config and data directories and an environ
// naming them.
type ProfileEnv struct {
	Home, Config, Data string
	Environ            []string
}

// NewProfileEnv creates the three directories under one t.TempDir() and
// returns the environ (HOME, AGENTMESH_CONFIG_DIR, AGENTMESH_DATA_DIR, then
// extra).
func NewProfileEnv(t testing.TB, extra ...string) *ProfileEnv {
	t.Helper()
	root := t.TempDir()
	e := &ProfileEnv{Home: filepath.Join(root, "home"), Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data")}
	for _, d := range []string{e.Home, e.Config, e.Data} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.Environ = append([]string{"HOME=" + e.Home, "AGENTMESH_CONFIG_DIR=" + e.Config, "AGENTMESH_DATA_DIR=" + e.Data}, extra...)
	return e
}

// Save writes config.json in the config directory holding exactly ps.
func (e *ProfileEnv) Save(t testing.TB, ps ...profile.Profile) {
	t.Helper()
	c := profile.Config{Path: filepath.Join(e.Config, "config.json"), Profiles: map[string]profile.Profile{}}
	for _, p := range ps {
		c.Profiles[p.Name] = p
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
}
