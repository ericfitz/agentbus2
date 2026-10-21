//go:build !windows

package credential

// ownerOnly is a no-op off Windows: keys.WriteFile creates the file at mode 0600.
func ownerOnly(string) error { return nil }
