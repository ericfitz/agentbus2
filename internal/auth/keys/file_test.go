package keys

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func noTemps(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestWriteFileAtomicAndMode(t *testing.T) {
	// Windows has no mode bits (a file reads as 0666); WriteFile's callers in
	// internal/credential set the owner-only ACL and test it there.
	unix := runtime.GOOS != "windows"
	dir := t.TempDir()
	path := filepath.Join(dir, "key.pem")
	if err := WriteFile(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if unix && st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	if err := WriteFile(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "two" {
		t.Errorf("content = %q", b)
	}
	st, _ = os.Stat(path)
	if unix && st.Mode().Perm() != 0o600 {
		t.Errorf("replaced mode = %v", st.Mode().Perm())
	}
	noTemps(t, dir)

	if err := WriteFile(filepath.Join(dir, "missing", "key.pem"), []byte("x")); err == nil {
		t.Error("write into a missing directory succeeded")
	}
	noTemps(t, dir)
	// A rename that fails (target is a non-empty directory) leaves no temp file.
	target := filepath.Join(dir, "d")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "f"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(target, []byte("x")); err == nil {
		t.Error("rename over a directory succeeded")
	}
	noTemps(t, dir)
}
