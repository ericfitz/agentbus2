package access

import (
	"fmt"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Reasons name a denial so a caller can map it to a fixed text without
// parsing Text. ReasonNotRegistered (the Anonymous principal) and
// ReasonUndefined (an (object, action) pair the table does not define) are
// additions of this implementation to the spec's list.
const (
	ReasonOutOfScope     = "out_of_scope"
	ReasonNoPermission   = "no_permission"
	ReasonNotAdmin       = "not_admin"
	ReasonNotOwner       = "not_owner"
	ReasonMemoryChannel  = "memory_channel"
	ReasonNotHuman       = "not_human"
	ReasonNoWrite        = "no_write"
	ReasonNotRegistered  = "not_registered"
	ReasonProtectedEntry = "protected_entry"
	ReasonUndefined      = "undefined"
	ReasonNamePrefix     = "name_prefix"
)

// Decision is the answer of the access check. Code and Text are set only when
// Allowed is false; a denial is never retryable except an internal one.
type Decision struct {
	Allowed      bool
	Code         bus.Code
	Text, Reason string
}

// facts is the union of what decide reads about an object.
type facts interface{ factsObject() }

// aclChangeFacts carries what the protected-entry rule reads: the stored rows,
// the proposed ones and the identity row of each distinct stored name that is
// not a group (a name whose row is gone is absent).
type aclChangeFacts struct {
	Channel  store.Channel
	Rows     []store.ACLEntry
	Proposed []store.ACLEntry
	Named    map[string]store.Identity
}

func (aclChangeFacts) factsObject() {}

type channelFacts struct {
	Channel store.Channel
	Rows    []store.ACLEntry // the stored rows of the channel
}

// serverFacts carries the one configuration value the rule reads.
type serverFacts struct{ ChannelCreation string }

type sessionFacts struct{ store.Session }

type identityFacts struct{ store.Identity }

// channelNameFacts carries a proposed channel name, the scope the channel will
// carry and every scope in every state.
type channelNameFacts struct {
	Name, Scope string
	Scopes      []store.Scope
}

// scopeNameFacts carries a proposed scope name, every scope in every state and
// every channel.
type scopeNameFacts struct {
	Name     string
	Scopes   []store.Scope
	Channels []store.Channel
}

func (channelNameFacts) factsObject() {}
func (scopeNameFacts) factsObject()   {}
func (channelFacts) factsObject()     {}
func (serverFacts) factsObject()      {}
func (sessionFacts) factsObject()     {}
func (identityFacts) factsObject()    {}

var allowed = Decision{Allowed: true}

func deny(code bus.Code, reason, format string, args ...any) Decision {
	return Decision{Code: code, Reason: reason, Text: fmt.Sprintf(format, args...)}
}

// actsAsAdmin reports whether p holds the server administrator right: the
// flag. An administrator is never scoped (Eric's decision 2026-10-25, replaces
// 4.1 (a)); the identity read path guarantees it, so the rule does not
// consult the scope.
func actsAsAdmin(p Principal) bool { return p.Admin }

// inScope is the scope gate (spec 3.12): an unscoped principal is never
// restricted; a scoped one reaches only objects of its scope. The server is
// outside every scope for Administer and inside for CreateChannel (the new
// channel takes the principal's scope); a session is always the caller's own.
func inScope(p Principal, f facts) bool {
	if p.Scope == "" {
		return true
	}
	switch f := f.(type) {
	case channelFacts:
		return f.Channel.Scope == p.Scope
	case aclChangeFacts:
		return f.Channel.Scope == p.Scope
	case identityFacts:
		// Every human and every administrator is visible to every agent
		// (Eric 2026-10-25, spec 4.5); any other identity only within the scope.
		return f.Kind == store.IdentityHuman || f.Admin || f.Scope == p.Scope
	case sessionFacts:
		return true
	case serverFacts:
		return true // Administer is refused by its own clause for a scoped principal
	case channelNameFacts, scopeNameFacts:
		return true // the scoped principal is handled by the name rule; ScopeName callers hold Administer first
	}
	return false
}

func objectKind(f facts) string {
	switch f.(type) {
	case channelFacts:
		return "channel"
	case aclChangeFacts:
		return "acl change"
	case serverFacts:
		return "server"
	case sessionFacts:
		return "session"
	case identityFacts:
		return "identity"
	case channelNameFacts:
		return "channel name"
	case scopeNameFacts:
		return "scope name"
	}
	return fmt.Sprintf("%T", f)
}

func undefined(f facts, a Action) Decision {
	return Decision{Code: bus.CodeInternal, Reason: ReasonUndefined,
		Text: fmt.Sprintf("access: action %q is not defined for %s", a, objectKind(f))}
}

// decide is the one access rule (spec 3.2). Nothing else in the server-side
// packages reads a principal's Admin or Scope, a channel's scope or kind, a
// stored row's flags or a session's identity to decide access.
func decide(p Principal, f facts, a Action) Decision {
	if p == Anonymous {
		return deny(bus.CodeNotRegistered, ReasonNotRegistered, "%q is not a registered identity", "")
	}
	switch f := f.(type) {
	case channelFacts:
		return decideChannel(p, f, a)
	case aclChangeFacts:
		return decideACLChange(p, f, a)
	case serverFacts:
		return decideServer(p, f, a)
	case channelNameFacts:
		return decideChannelName(p, f, a)
	case scopeNameFacts:
		return decideScopeName(f, a)
	case sessionFacts:
		if a != Use {
			return undefined(f, a)
		}
		if f.Identity != p.Name {
			return deny(bus.CodeNotFound, ReasonNotOwner, "session %q does not exist", f.ID)
		}
		return allowed
	case identityFacts:
		if a != See {
			return undefined(f, a)
		}
		if !inScope(p, f) {
			return deny(bus.CodeNotFound, ReasonOutOfScope, "identity %q is not registered", f.Name)
		}
		return allowed
	}
	return undefined(f, a)
}

// decideChannelName is the scope-prefixed naming rule (accesscheck spec 4.2,
// Eric's decision 2026-10-25): a scoped principal's channel is named
// <scope>-<rest>, and a name starting with <s>- for an existing scope s (any
// state) belongs to s. It validates the name's shape; names that violate it
// already stored are not converted (pre-release).
func decideChannelName(p Principal, f channelNameFacts, a Action) Decision {
	if a != TakeName {
		return undefined(f, a)
	}
	if p.Scope != "" && !strings.HasPrefix(f.Name, p.Scope+"-") {
		return deny(bus.CodeValidation, ReasonNamePrefix, "a channel of scope %q is named %s-<name>", p.Scope, p.Scope)
	}
	for _, sc := range f.Scopes {
		if strings.HasPrefix(f.Name, sc.Name+"-") && f.Scope != sc.Name {
			return deny(bus.CodeValidation, ReasonNamePrefix, "names starting with %q- belong to scope %q", sc.Name, sc.Name)
		}
	}
	return allowed
}

// decideScopeName is the scope-name rule (spec 4.2): two scopes whose prefixes
// nest would make the longer one unusable, and the prefix <name>- must not
// already name a channel of another scope or of none. The caller holds
// Administer first.
func decideScopeName(f scopeNameFacts, a Action) Decision {
	if a != TakeName {
		return undefined(f, a)
	}
	for _, sc := range f.Scopes {
		if strings.HasPrefix(sc.Name+"-", f.Name+"-") || strings.HasPrefix(f.Name+"-", sc.Name+"-") {
			if sc.Name == f.Name {
				continue // the store's own conflict for an existing name
			}
			return deny(bus.CodeConflict, ReasonNamePrefix, "scope %q: the name prefix %q- overlaps scope %q", f.Name, f.Name, sc.Name)
		}
	}
	for _, c := range f.Channels {
		if strings.HasPrefix(c.Name, f.Name+"-") && c.Scope != f.Name {
			return deny(bus.CodeConflict, ReasonNamePrefix, "scope %q: the name prefix %q- is in use by channel %q", f.Name, f.Name, c.Name)
		}
	}
	return allowed
}

func decideServer(p Principal, f serverFacts, a Action) Decision {
	switch a {
	case Administer:
		if !p.Admin {
			return deny(bus.CodeForbidden, ReasonNotAdmin, "administrator privilege required")
		}
		return allowed
	case CreateChannel:
		switch f.ChannelCreation {
		case "open":
			return allowed
		case "admins":
			if actsAsAdmin(p) {
				return allowed
			}
			return deny(bus.CodeForbidden, ReasonNotAdmin, "administrator privilege required")
		}
		return Decision{Code: bus.CodeInternal, Reason: ReasonUndefined,
			Text: fmt.Sprintf("access: channel_creation %q is not defined", f.ChannelCreation)}
	}
	return undefined(f, a)
}

// appliesTo reports whether the stored row e names p: its name, everyone,
// admins for an administrator, humans for a human.
func appliesTo(p Principal, e store.ACLEntry) bool {
	return e.Identity == p.Name || e.Identity == store.Everyone ||
		(e.Identity == store.Admins && actsAsAdmin(p)) ||
		(e.Identity == store.Humans && p.Kind == store.IdentityHuman)
}

// permsOf is the union of the stored rows for p.Name, everyone, for an
// administrator admins and for a human humans (Decision 80, Eric 2026-10-25).
// The implicit admins entry is applied by the Admin action, not here.
func permsOf(p Principal, rows []store.ACLEntry) store.Permissions {
	var u store.Permissions
	for _, e := range rows {
		if !appliesTo(p, e) {
			continue
		}
		var ep store.Permissions
		for _, perm := range e.Permissions {
			switch perm {
			case store.PermRead:
				ep.Read = true
			case store.PermWrite:
				ep.Write = true
			case store.PermAdmin:
				ep.Admin = true
			}
		}
		u = u.Union(ep)
	}
	return u
}

func needs(p Principal, perm store.Permission, ch store.Channel) Decision {
	return deny(bus.CodeForbidden, ReasonNoPermission, "%s needs %s on channel %q", p.Name, perm, ch.Name)
}

func decideChannel(p Principal, f channelFacts, a Action) Decision {
	switch a {
	case Read, Write, Admin, List, Unsubscribe, FilterReceive, Deliver, Post:
	default:
		return undefined(f, a)
	}
	if !inScope(p, f) {
		return deny(bus.CodeNotFound, ReasonOutOfScope, "channel %q does not exist", f.Channel.Name)
	}
	perms := permsOf(p, f.Rows)
	switch a {
	case Read:
		if !perms.Read {
			return needs(p, store.PermRead, f.Channel)
		}
	case Write:
		if !perms.Write {
			return needs(p, store.PermWrite, f.Channel)
		}
	case Admin:
		if !perms.Admin && !actsAsAdmin(p) {
			return needs(p, store.PermAdmin, f.Channel)
		}
	case List:
		if actsAsAdmin(p) {
			break
		}
		listed := false
		for _, e := range f.Rows {
			if appliesTo(p, e) {
				listed = true
				break
			}
		}
		if !listed {
			return deny(bus.CodeForbidden, ReasonNoPermission, "%s holds no ACL entry on channel %q", p.Name, f.Channel.Name)
		}
	case Unsubscribe, FilterReceive:
		// the scope gate above is the whole rule
	case Deliver:
		if !perms.Read {
			return needs(p, store.PermRead, f.Channel)
		}
		if p.Kind != store.IdentityHuman && f.Channel.Kind == store.KindMemory { // fail closed: only a human is delivered a memory channel
			return deny(bus.CodeValidation, ReasonMemoryChannel,
				"%s is a memory channel: agents do not subscribe to memories (decision 20); use search to find a memory and get_memory to read it", f.Channel.Name)
		}
	case Post:
		if p.Kind != store.IdentityHuman {
			return deny(bus.CodeForbidden, ReasonNotHuman, "%s is not a human", p.Name)
		}
		if !perms.Write {
			return Decision{Code: bus.CodeForbidden, Reason: ReasonNoWrite,
				Text: needs(p, store.PermWrite, f.Channel).Text}
		}
	}
	return allowed
}

// permsUnion is the union of the permissions of every entry named name.
func permsUnion(rows []store.ACLEntry, name string) store.Permissions {
	var u store.Permissions
	for _, e := range rows {
		if e.Identity != name {
			continue
		}
		for _, perm := range e.Permissions {
			switch perm {
			case store.PermRead:
				u.Read = true
			case store.PermWrite:
				u.Write = true
			case store.PermAdmin:
				u.Admin = true
			}
		}
	}
	return u
}

// decideACLChange is ReplaceACL: the scope gate, admin on the channel as
// decideChannel computes it, then, for an agent that is not an administrator,
// the protected-entry rule (Eric 2026-10-25, spec 4.6): no protected name
// (everyone, admins, humans, a human or an administrator) loses a permission.
func decideACLChange(p Principal, f aclChangeFacts, a Action) Decision {
	if a != ReplaceACL {
		return undefined(f, a)
	}
	if !inScope(p, f) {
		return deny(bus.CodeNotFound, ReasonOutOfScope, "channel %q does not exist", f.Channel.Name)
	}
	if d := decideChannel(p, channelFacts{Channel: f.Channel, Rows: f.Rows}, Admin); !d.Allowed {
		return d
	}
	if p.Kind == store.IdentityHuman || p.Admin {
		return allowed
	}
	seen := make(map[string]bool)
	for _, e := range f.Rows {
		name := e.Identity
		if seen[name] {
			continue
		}
		seen[name] = true
		id, named := f.Named[name]
		if name != store.Everyone && name != store.Admins && name != store.Humans &&
			(!named || id.Kind != store.IdentityHuman && !id.Admin) {
			continue
		}
		had, has := permsUnion(f.Rows, name), permsUnion(f.Proposed, name)
		var lost []string
		if had.Read && !has.Read {
			lost = append(lost, string(store.PermRead))
		}
		if had.Write && !has.Write {
			lost = append(lost, string(store.PermWrite))
		}
		if had.Admin && !has.Admin {
			lost = append(lost, string(store.PermAdmin))
		}
		if len(lost) > 0 {
			return deny(bus.CodeForbidden, ReasonProtectedEntry,
				"an agent cannot remove %s from ACL entry %q: humans and administrators keep their access",
				strings.Join(lost, ", "), name)
		}
	}
	return allowed
}
