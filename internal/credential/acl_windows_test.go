//go:build windows

package credential

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/fileacl"
	"github.com/ericfitz/agentbus2/internal/fileacl/acltest"
)

// TestSaveSetsOwnerOnlyACL is the Windows counterpart of the Unix mode-0600
// checks (Decision 55, Milestone 6 choice 5): a saved credential file has a
// protected DACL with one entry, which allows the current user.
func TestSaveSetsOwnerOnlyACL(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // the first write and a replacement
		if err := s.Save("pat", k); err != nil {
			t.Fatal(err)
		}
		acltest.AssertOwnerOnly(t, s.Path("pat"))
	}
}

// The token cache holds a bearer token and gets the same owner-only DACL.
func TestCacheSaveSetsOwnerOnlyACL(t *testing.T) {
	c := Cache{Dir: filepath.Join(t.TempDir(), "tokens", "lab")}
	for range 2 {
		if err := c.Save("pat", Entry{Server: "http://127.0.0.1:1", AccessToken: "t1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		acltest.AssertOwnerOnly(t, c.path("pat"))
	}
}

// The human session holds a bearer and the provider's refresh token and gets
// the same owner-only DACL.
func TestHumanCacheSaveSetsOwnerOnlyACL(t *testing.T) {
	c := HumanCache{Dir: filepath.Join(t.TempDir(), "tokens", "lab")}
	for range 2 {
		if err := c.Save(HumanSession{Server: "http://127.0.0.1:1", Identity: "eric@corp.example", AccessToken: "t1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		acltest.AssertOwnerOnly(t, filepath.Join(c.Dir, "human", "session.json"))
	}
}

// TestLoadFileRefusesEveryoneACL: a key file that grants Everyone (S-1-1-0)
// access is refused with ErrNotOwnerOnly, and the owner-only one loads.
func TestLoadFileRefusesEveryoneACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.pem")
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	pemb, err := keys.EncodePrivatePEM(k)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.WriteFile(path, pemb); err != nil {
		t.Fatal(err)
	}
	if err := fileacl.OwnerOnly(path); err != nil { // open note: mcp-only-client 5 (Ruling 12)
		t.Fatal(err)
	}
	if _, _, err := LoadFile(path); err != nil {
		t.Fatalf("owner-only: %v", err)
	}
	acltest.GrantEveryone(t, path)
	_, _, err = LoadFile(path)
	if !errors.Is(err, ErrNotOwnerOnly) || !strings.Contains(err.Error(), "S-1-1-0") {
		t.Fatalf("with Everyone: %v", err)
	}
}

// TestLoadFileMissingNamesPath: a missing path and a directory get the Unix
// text (the path is named) and are not ErrNotOwnerOnly.
func TestLoadFileMissingNamesPath(t *testing.T) {
	dir := t.TempDir()
	for name, p := range map[string]string{"missing": filepath.Join(dir, "absent.pem"), "directory": dir} {
		_, _, err := LoadFile(p)
		if err == nil || !strings.Contains(err.Error(), p) || errors.Is(err, ErrNotOwnerOnly) {
			t.Fatalf("%s: err %v, want an error naming %s that is not ErrNotOwnerOnly", name, err, p)
		}
	}
}
