package localmode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ericfitz/agentbus2/internal/profile"
)

// env builds temp config and data directories and an environ naming them (a
// copy of internal/client's resolve_test.go helper: test files do not export).
type env struct {
	home, config, data string
	environ            []string
}

func newEnv(t *testing.T, extra ...string) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{home: filepath.Join(root, "home"), config: filepath.Join(root, "config"), data: filepath.Join(root, "data")}
	for _, d := range []string{e.home, e.config, e.data} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.environ = append([]string{"HOME=" + e.home, "AGENTBUS2_CONFIG_DIR=" + e.config, "AGENTBUS2_DATA_DIR=" + e.data}, extra...)
	return e
}

func (e *env) save(t *testing.T, ps ...profile.Profile) {
	t.Helper()
	c := profile.Config{Path: filepath.Join(e.config, "config.json"), Profiles: map[string]profile.Profile{}}
	for _, p := range ps {
		c.Profiles[p.Name] = p
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
}
