package testkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLockedBufferConcurrent(t *testing.T) {
	var b LockedBuffer
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				if _, err := b.Write([]byte("x")); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if n := len(b.String()); n != 8000 {
		t.Fatalf("len(String()) = %d, want 8000", n)
	}
}

func TestProfileEnvLayout(t *testing.T) {
	e := NewProfileEnv(t, "FOO=bar", "BAZ=qux")
	root := filepath.Dir(e.Home)
	for _, d := range []string{e.Home, e.Config, e.Data} {
		if filepath.Dir(d) != root {
			t.Errorf("%s is not under %s", d, root)
		}
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("%s: not a directory (%v)", d, err)
		}
	}
	for _, want := range []string{"HOME=" + e.Home, "AGENTBUS2_CONFIG_DIR=" + e.Config, "AGENTBUS2_DATA_DIR=" + e.Data, "FOO=bar", "BAZ=qux"} {
		found := false
		for _, kv := range e.Environ {
			if kv == want {
				found = true
			}
		}
		if !found {
			t.Errorf("Environ lacks %q: %v", want, e.Environ)
		}
	}
}

func TestProfileEnvSave(t *testing.T) {
	e := NewProfileEnv(t)
	e.Save(t)
	if _, err := os.Stat(filepath.Join(e.Config, "config.json")); err != nil {
		t.Fatal(err)
	}
}

// TestTestkitTestOnly fails when non-test code imports internal/testkit.
func TestTestkitTestOnly(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "./...")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		for _, imp := range f[1:] {
			if strings.HasSuffix(imp, "/internal/testkit") {
				t.Errorf("non-test code in %s imports %s", f[0], imp)
			}
		}
	}
}
