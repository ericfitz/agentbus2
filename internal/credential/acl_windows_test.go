//go:build windows

package credential

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

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
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	everyone, err := windows.StringToSid("S-1-1-0")
	if err != nil {
		t.Fatal(err)
	}
	entry := func(sid *windows.SID) windows.EXPLICIT_ACCESS {
		return windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_READ,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee:           windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)},
		}
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry(user.User.Sid), entry(everyone)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	_, _, err = LoadFile(path)
	if !errors.Is(err, ErrNotOwnerOnly) || !strings.Contains(err.Error(), "S-1-1-0") {
		t.Fatalf("with Everyone: %v", err)
	}
}
