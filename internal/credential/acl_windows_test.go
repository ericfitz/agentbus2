//go:build windows

package credential

import (
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"golang.org/x/sys/windows"
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
		assertOwnerOnly(t, s.Path("pat"))
	}
}

// The token cache holds a bearer token and gets the same owner-only DACL.
func TestCacheSaveSetsOwnerOnlyACL(t *testing.T) {
	c := Cache{Dir: filepath.Join(t.TempDir(), "tokens", "lab")}
	for range 2 {
		if err := c.Save("pat", Entry{Server: "http://127.0.0.1:1", AccessToken: "t1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		assertOwnerOnly(t, c.path("pat"))
	}
}

func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("reading the security of %s: %v", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("%s: the DACL inherits from the directory", path)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("%s: no DACL (%v)", path, err)
	}
	if dacl.AceCount != 1 {
		t.Fatalf("%s: %d ACL entries, want 1", path, dacl.AceCount)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		t.Fatalf("%s: entry type %d, want access-allowed", path, ace.Header.AceType)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)); !sid.Equals(user.User.Sid) {
		t.Fatalf("%s: the entry allows %v, want the current user %v", path, sid, user.User.Sid)
	}
}
