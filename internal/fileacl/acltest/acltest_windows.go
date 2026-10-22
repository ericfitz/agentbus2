//go:build windows

// Package acltest is the Windows DACL assertion the owner-only tests share.
package acltest

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// AssertOwnerOnly fails t unless path has a protected DACL with exactly one
// entry, which allows the current user.
func AssertOwnerOnly(t testing.TB, path string) {
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
