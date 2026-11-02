// Package osuser names the OS login name, the human's identity (Decision 16).
package osuser

import (
	"os/user"
	"strings"
)

// NameFrom is the OS login name from current. Ported from v1
// internal/config/config.go defaultTUIName, returning "" (no fallback)
// instead of "human" when the user is unknown. On Windows user.Current
// returns DOMAIN\name; keep the part after the last backslash. A '/' would
// fail the identity name rule, so it is replaced.
func NameFrom(current func() (*user.User, error)) string {
	u, err := current()
	if err != nil || u == nil || u.Username == "" {
		return ""
	}
	name := u.Username
	if i := strings.LastIndex(name, `\`); i >= 0 {
		name = name[i+1:]
	}
	return strings.ReplaceAll(name, "/", "-")
}
