package mcpcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isAbs(p string) bool { return filepath.IsAbs(p) }

// open note: mcp-only-client 13
func TestEnrollmentTokenSources(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tok")
	write := func(body string) string {
		t.Helper()
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	env := []string{"AGENTMESH_ENROLLMENT_TOKEN=abe_env"}

	if got, err := enrollmentToken("abe_flag", "", env); err != nil || got != "abe_flag" {
		t.Errorf("flag over variable: %q, %v", got, err)
	}
	if got, err := enrollmentToken("", write("abe_file\n"), env); err != nil || got != "abe_file" {
		t.Errorf("file over variable: %q, %v", got, err)
	}
	if got, err := enrollmentToken("", write("  abe_x\n"), nil); err != nil || got != "abe_x" {
		t.Errorf("trimmed file: %q, %v", got, err)
	}
	if got, err := enrollmentToken("", "", env); err != nil || got != "abe_env" {
		t.Errorf("variable: %q, %v", got, err)
	}
	if got, err := enrollmentToken("", "", nil); err != nil || got != "" {
		t.Errorf("none: %q, %v", got, err)
	}
	if _, err := enrollmentToken("abe_flag", write("abe_file"), env); err == nil {
		t.Error("both flags: want an error")
	}
	// write reuses one path, so the empty case gets a file of its own.
	emptyFile := filepath.Join(dir, "empty")
	if err := os.WriteFile(emptyFile, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ path, msg string }{
		"empty":     {emptyFile, "is empty"},
		"directory": {dir, ""},
		"too big":   {write(strings.Repeat("A", 4097)), ""},
		"missing":   {filepath.Join(dir, "nope"), ""},
	} {
		_, err := enrollmentToken("", c.path, env)
		if err == nil || !strings.Contains(err.Error(), c.path) || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: err %v, want one naming %s containing %q", name, err, c.path, c.msg)
		}
	}
	if got, err := enrollmentToken("", write(strings.Repeat("A", 4096)), nil); err != nil || len(got) != 4096 {
		t.Errorf("4096-byte file: len %d, %v", len(got), err)
	}
}
