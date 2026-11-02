package fileacl

import (
	"errors"
)

// ErrExposed marks CheckOwnerOnly's policy refusals (a NULL DACL, an entry for
// another principal), as opposed to a failure to read the file's security.
var ErrExposed = errors.New("fileacl: the file is accessible to other principals")

// WindowsFix is the one source of the Windows fix text for an exposed key file:
// the icacls command in both shells, because %USERNAME% expands only in
// cmd.exe and ${env:USERNAME} only in PowerShell (the default Windows shell).
// /inheritance:r drops the inherited entries, /grant:r gives the current user
// full control, and /remove:g drops the explicit entries CheckOwnerOnly does
// not allow and a key file most often has: Everyone (S-1-1-0), Authenticated
// Users (S-1-5-11) and Users (S-1-5-32-545). Another account that check still
// names is removed with /remove:g ACCOUNT. SYSTEM and Administrators stay, as
// CheckOwnerOnly allows them. It is pure text and builds on every OS, so
// check, CheckOwnerOnly and the help share it and a test pins it anywhere.
func WindowsFix(path string) string {
	cmd := func(user string) string {
		return `icacls "` + path + `" /inheritance:r /grant:r "` + user + `:F" /remove:g *S-1-1-0 *S-1-5-11 *S-1-5-32-545`
	}
	return "in PowerShell run " + cmd("${env:USERNAME}") + "; in cmd.exe run " + cmd("%USERNAME%") + "; if check still names another account, add /remove:g ACCOUNT"
}

// aceKind classifies a DACL entry type for CheckOwnerOnly.
type aceKind int

const (
	// aceIgnore never grants access: deny, audit, alarm and label entries.
	aceIgnore aceKind = iota
	// aceSIDGrant grants access and starts with header, mask, SID (types 0, 9),
	// so its trustee can be compared with the owner.
	aceSIDGrant
	// aceOpaqueGrant grants access but has a layout this code does not read
	// (object, compound and callback-object entries): refused as exposed.
	aceOpaqueGrant
	// aceUnknown is a type this code does not know: refused as exposed
	// (reject unrecognized input, DoD B).
	aceUnknown
)

// classifyACE maps an ACE_HEADER.AceType (winnt.h) to its kind. The types
// are written as numbers so the helper builds, and a test runs, on every OS.
func classifyACE(t uint8) aceKind {
	switch t {
	case 0x00, 0x09: // ACCESS_ALLOWED, ACCESS_ALLOWED_CALLBACK
		return aceSIDGrant
	case 0x04, 0x05, 0x0B: // COMPOUND, OBJECT, CALLBACK_OBJECT allowed
		return aceOpaqueGrant
	case 0x01, 0x02, 0x03, 0x06, 0x07, 0x08, 0x0A, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, // deny, audit, alarm
		0x11, 0x12, 0x13, 0x14, 0x15: // label, attribute, policy, trust label, filter
		return aceIgnore
	}
	return aceUnknown
}
