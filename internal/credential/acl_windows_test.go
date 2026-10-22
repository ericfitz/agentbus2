//go:build windows

package credential

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
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
