//go:build unix

package cmd

import (
	"os"
	"strings"
	"syscall"
	"testing"
)

// TestAdminCreatesPrivateDatabase: the Finding 2 probe of the Milestone 5
// review. `server admin create-identity` on a missing file under umask 022
// creates it 0600; a 0644 file is refused (exit 1) with stderr naming the path
// and mode (post-build decision 8).
func TestAdminCreatesPrivateDatabase(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	e := newAdminEnv(t)
	if code, _, errOut := e.run("create-identity", "pat"); code != 0 {
		t.Fatalf("create-identity: code %d stderr %q", code, errOut)
	}
	fi, err := os.Stat(e.file)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("database mode = %#o, want 0600", got)
	}
	if err := os.Chmod(e.file, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := e.run("create-identity", "kim")
	if code != 1 {
		t.Fatalf("create-identity on a 0644 file: code %d, want 1; stderr %q", code, errOut)
	}
	if !strings.Contains(errOut, e.file) || !strings.Contains(errOut, "0644") || !strings.Contains(errOut, "chmod 600") {
		t.Fatalf("stderr lacks the refusal naming file, mode and fix: %q", errOut)
	}
}
