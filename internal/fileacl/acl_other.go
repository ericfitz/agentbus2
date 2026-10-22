//go:build !windows

package fileacl

// OwnerOnly is a no-op off Windows: the callers create the file at mode 0600
// (keys.WriteFile) or chmod it to 0600 (the server socket).
func OwnerOnly(string) error { return nil }
