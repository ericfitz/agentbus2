//go:build windows

package fileacl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

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

// open note: mcp-only-client 5
func TestCheckOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := OwnerOnly(path); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwnerOnly(path); err != nil {
		t.Fatalf("after OwnerOnly: %v", err)
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
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
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
	err = CheckOwnerOnly(path)
	if err == nil || !strings.Contains(err.Error(), "S-1-1-0") || !errors.Is(err, ErrExposed) {
		t.Fatalf("with Everyone: %v", err)
	}
	// A failure to read the security of a missing file is not a policy refusal.
	if err := CheckOwnerOnly(filepath.Join(filepath.Dir(path), "absent")); err == nil || errors.Is(err, ErrExposed) {
		t.Fatalf("missing file: %v", err)
	}
}

// A descriptor with no DACL, and a NULL one, are exposed (they grant access to
// everyone); other errors are not.
func TestDACLAbsentIsExposed(t *testing.T) {
	if !daclAbsent(windows.ERROR_OBJECT_NOT_FOUND) || daclAbsent(windows.ERROR_ACCESS_DENIED) || daclAbsent(nil) {
		t.Fatal("daclAbsent classifies the wrong errors")
	}
	path := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwnerOnly(path); !errors.Is(err, ErrExposed) {
		t.Fatalf("NULL DACL: %v", err)
	}
}
