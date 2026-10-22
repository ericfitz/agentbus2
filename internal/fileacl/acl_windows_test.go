//go:build windows

package fileacl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ericfitz/agentbus2/internal/fileacl/acltest"
)

// OwnerOnly replaces the DACL of an existing file, and again on a second call.
func TestOwnerOnlySetsOwnerOnlyACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := OwnerOnly(path); err != nil {
			t.Fatal(err)
		}
		acltest.AssertOwnerOnly(t, path)
	}
}

func TestOwnerOnlyMissingFile(t *testing.T) {
	if err := OwnerOnly(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("OwnerOnly on a missing file succeeded")
	}
}
