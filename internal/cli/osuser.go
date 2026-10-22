package cli

import (
	"os/user"
	"strings"
)

// currentUser is user.Current; tests replace it.
var currentUser = user.Current

// osUserName is the OS login name, the TUI's identity when none is set.
// Ported from v1 internal/config/config.go defaultTUIName, returning "" (no
// fallback) instead of "human" when the user is unknown. On Windows
// user.Current returns DOMAIN\name; keep the part after the last backslash. A
// '/' would fail the identity name rule, so it is replaced.
func osUserName() string { return osUserNameFrom(currentUser) }

func osUserNameFrom(current func() (*user.User, error)) string {
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
