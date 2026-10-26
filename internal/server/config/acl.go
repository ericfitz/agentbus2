package config

import (
	"fmt"
	"slices"

	"github.com/ericfitz/agentbus2/internal/store"
)

// CreatorPlaceholder stands for the creating identity in default_acl.
const CreatorPlaceholder = "<creator>"

// ACLEntry is one default_acl entry.
type ACLEntry struct {
	Identity    string   `json:"identity"`
	Permissions []string `json:"permissions"`
}

// shippedDefault is the Decision 88 literal.
func shippedDefault() []ACLEntry {
	return []ACLEntry{
		{store.Everyone, []string{"read", "write"}},
		{CreatorPlaceholder, []string{"read", "write", "admin"}},
		{store.Admins, []string{"admin"}},
	}
}

// IsShippedDefault reports whether entries equal the Decision 88 literal,
// order included.
func IsShippedDefault(entries []ACLEntry) bool {
	return slices.EqualFunc(entries, shippedDefault(), func(a, b ACLEntry) bool {
		return a.Identity == b.Identity && slices.Equal(a.Permissions, b.Permissions)
	})
}

// ValidateDefaultACL checks a default_acl list. Only everyone, admins, humans
// (the Store exempts the three groups from the registry check) and the
// creator placeholder are allowed as identities: the Store enforces
// Decision 96 on every ACL write and general is created before any identity
// row exists, so a concrete name could never start a server.
func ValidateDefaultACL(entries []ACLEntry) error {
	for i, e := range entries {
		switch e.Identity {
		case store.Everyone, store.Admins, store.Humans, CreatorPlaceholder:
		default:
			return fmt.Errorf("default_acl: entry %d: identity %q is not everyone, admins, humans or %s", i, e.Identity, CreatorPlaceholder)
		}
		if len(e.Permissions) == 0 {
			return fmt.Errorf("default_acl: entry %d (%s): permissions must not be empty", i, e.Identity)
		}
		seen := map[string]bool{}
		for _, p := range e.Permissions {
			if !store.ValidPermission(store.Permission(p)) {
				return fmt.Errorf("default_acl: entry %d (%s): permission %q is not read, write or admin", i, e.Identity, p)
			}
			if seen[p] {
				return fmt.Errorf("default_acl: entry %d (%s): duplicate permission %q", i, e.Identity, p)
			}
			seen[p] = true
		}
	}
	return nil
}

// DefaultACLFunc returns the builder core.Config.DefaultACL expects. It
// substitutes creator for the placeholder and drops that entry when creator
// is empty (Decision 92). The entries must have passed ValidateDefaultACL.
func DefaultACLFunc(entries []ACLEntry) func(creator string) []store.ACLEntry {
	entries = slices.Clone(entries)
	return func(creator string) []store.ACLEntry {
		out := make([]store.ACLEntry, 0, len(entries))
		for _, e := range entries {
			id := e.Identity
			if id == CreatorPlaceholder {
				if creator == "" {
					continue
				}
				id = creator
			}
			perms := make([]store.Permission, len(e.Permissions))
			for i, p := range e.Permissions {
				perms[i] = store.Permission(p)
			}
			out = append(out, store.ACLEntry{Identity: id, Permissions: perms})
		}
		return out
	}
}
