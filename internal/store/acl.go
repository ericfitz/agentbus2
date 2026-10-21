package store

import (
	"fmt"
	"slices"
)

// Permission is one ACL permission (Decision 80).
type Permission string

const (
	PermRead  Permission = "read"
	PermWrite Permission = "write"
	PermAdmin Permission = "admin"
)

// ACLEntry is one stored ACL entry (plan choice 2).
type ACLEntry struct {
	Identity    string
	Permissions []Permission
}

// Permissions is the evaluated result of a union of entries.
type Permissions struct{ Read, Write, Admin bool }

// Union returns the permissions granted by either p or q.
func (p Permissions) Union(q Permissions) Permissions {
	return Permissions{Read: p.Read || q.Read, Write: p.Write || q.Write, Admin: p.Admin || q.Admin}
}

// ValidPermission reports whether p is one of the three known permissions.
func ValidPermission(p Permission) bool {
	return p == PermRead || p == PermWrite || p == PermAdmin
}

// ValidateEntry rejects (ErrInvalid) an empty permission list, an unknown
// permission, and an identity failing NameRule. everyone and admins pass.
func ValidateEntry(e ACLEntry) error {
	if err := NameRule(e.Identity); err != nil {
		return fmt.Errorf("%w: ACL identity %q %s", ErrInvalid, e.Identity, err)
	}
	if len(e.Permissions) == 0 {
		return fmt.Errorf("%w: ACL entry for %q has no permissions", ErrInvalid, e.Identity)
	}
	for _, p := range e.Permissions {
		if !ValidPermission(p) {
			return fmt.Errorf("%w: unknown permission %q", ErrInvalid, p)
		}
	}
	return nil
}

// DefaultACL is the Decision 88 literal with the creator substituted: everyone
// read+write, the creator read+write+admin (omitted when creator is empty,
// Decision 92), admins admin.
func DefaultACL(creator string) []ACLEntry {
	acl := []ACLEntry{{Everyone, []Permission{PermRead, PermWrite}}}
	if creator != "" {
		acl = append(acl, ACLEntry{creator, []Permission{PermRead, PermWrite, PermAdmin}})
	}
	return append(acl, ACLEntry{Admins, slices.Clone([]Permission{PermAdmin})})
}
