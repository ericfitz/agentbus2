//go:build !windows

package fileacl

// OwnerOnly is a no-op off Windows: the callers create the file at mode 0600
// (keys.WriteFile) or chmod it to 0600 (the server socket).
func OwnerOnly(string) error { return nil }

// CheckOwnerOnly is a no-op off Windows: the Unix mode check of the callers
// (credential.LoadFile) is the rule there.
func CheckOwnerOnly(string) error { return nil }
