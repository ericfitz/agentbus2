//go:build windows

package fileacl

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OwnerOnly replaces path's DACL with one entry granting the current user
// full access, protected from inheritance (Decision 55: owner-only ACL on
// Windows). TestSaveSetsOwnerOnlyACL, TestCacheSaveSetsOwnerOnlyACL and
// TestListenUnixSetsOwnerOnlyACL check it on the Windows CI.
func OwnerOnly(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		return err
	}
	// Open the file itself, not what a path lookup follows: a Windows unix
	// socket is a reparse point (IO_REPARSE_TAG_AF_UNIX), and a path-based
	// call fails on it with ERROR_CANT_ACCESS_FILE.
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.WRITE_DAC|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// CheckOwnerOnly refuses a file whose DACL is NULL or holds an access-allowed
// entry (any allow type; types it cannot read fail closed) for a SID other than the current user, LocalSystem (S-1-5-18) and
// BUILTIN\Administrators (S-1-5-32-544), Windows' root, which reads a 0600
// file on Unix too. It follows a symlink to its target. Only those two
// refusals match ErrExposed; any other failure (a missing file, a token or
// security-descriptor read error) is returned as is. The DACL is read by path
// and the caller opens the file afterwards, a check-then-read window that is
// acceptable for a file the user names.
// open note: mcp-only-client 5
func CheckOwnerOnly(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return err
	}
	admins, err := windows.StringToSid("S-1-5-32-544")
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return exposedError(fmt.Sprintf("%s has a NULL DACL, which grants access to everyone; restrict it to the current user: %s", path, WindowsFix(path)))
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		switch classifyACE(ace.Header.AceType) {
		case aceIgnore:
			continue
		case aceOpaqueGrant, aceUnknown:
			return exposedError(fmt.Sprintf("%s has an access entry of type %#x that this check cannot read, which may grant access to other principals; restrict it to the current user: %s", path, ace.Header.AceType, WindowsFix(path)))
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.Equals(user.User.Sid) || sid.Equals(system) || sid.Equals(admins) {
			continue
		}
		return exposedError(fmt.Sprintf("%s grants access to %s; restrict it to the current user: %s", path, sid.String(), WindowsFix(path)))
	}
	return nil
}

// exposedError carries its message unchanged and matches ErrExposed.
type exposedError string

func (e exposedError) Error() string        { return string(e) }
func (e exposedError) Is(target error) bool { return target == ErrExposed }
