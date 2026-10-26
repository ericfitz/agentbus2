package access

import (
	"fmt"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Reasons name a denial so a caller can map it to a fixed text without
// parsing Text. ReasonNotRegistered (the Anonymous principal) and
// ReasonUndefined (an (object, action) pair the table does not define) are
// additions of this implementation to the spec's list.
const (
	ReasonOutOfScope    = "out_of_scope"
	ReasonNoPermission  = "no_permission"
	ReasonNotAdmin      = "not_admin"
	ReasonNotOwner      = "not_owner"
	ReasonMemoryChannel = "memory_channel"
	ReasonNotHuman      = "not_human"
	ReasonNoWrite       = "no_write"
	ReasonNotRegistered = "not_registered"
	ReasonUndefined     = "undefined"
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

type channelFacts struct {
	Channel store.Channel
	Rows    []store.ACLEntry // the stored rows of the channel
}

// serverFacts carries the one configuration value the rule reads.
type serverFacts struct{ ChannelCreation string }

type sessionFacts struct{ store.Session }

type identityFacts struct{ store.Identity }

func (channelFacts) factsObject()  {}
func (serverFacts) factsObject()   {}
func (sessionFacts) factsObject()  {}
func (identityFacts) factsObject() {}

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
	case identityFacts:
		// Every human and every administrator is visible to every agent
		// (Eric 2026-10-25, spec 4.5); any other identity only within the scope.
		return f.Kind == store.IdentityHuman || f.Admin || f.Scope == p.Scope
	case sessionFacts:
		return true
	case serverFacts:
		return true // Administer is refused by its own clause for a scoped principal
	}
	return false
}

func objectKind(f facts) string {
	switch f.(type) {
	case channelFacts:
		return "channel"
	case serverFacts:
		return "server"
	case sessionFacts:
		return "session"
	case identityFacts:
		return "identity"
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
	case serverFacts:
		return decideServer(p, f, a)
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

// permsOf is the union of the stored rows for p.Name, everyone, for an
// administrator admins and for a human humans (Decision 80, Eric 2026-10-25). The implicit admins entry is applied by
// the Admin action, not here.
func permsOf(p Principal, rows []store.ACLEntry) store.Permissions {
	var u store.Permissions
	admin := actsAsAdmin(p)
	human := p.Kind == store.IdentityHuman
	for _, e := range rows {
		if e.Identity != p.Name && e.Identity != store.Everyone &&
			(!admin || e.Identity != store.Admins) && (!human || e.Identity != store.Humans) {
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
	case Read, Write, Admin, List, Deliver, Post:
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
		// interim (spec 3.2): every channel in scope, until the channel-visibility rebuild
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
