package core

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// ToBusMessages converts stored messages. Channel names and display names
// are resolved once per distinct id; a message whose session row is gone
// shows the sender as its display name, and one with no session shows none.
func (c *Core) ToBusMessages(ctx context.Context, tx store.Tx, ms []store.Message) ([]bus.Message, error) {
	names := map[int64]string{}
	display := map[string]string{}
	out := make([]bus.Message, 0, len(ms))
	for _, m := range ms {
		name, ok := names[m.ChannelID]
		if !ok {
			ch, err := tx.Channel(ctx, m.ChannelID)
			if err != nil {
				return nil, FromStore(err)
			}
			name = ch.Name
			names[m.ChannelID] = name
		}
		d := ""
		if m.SessionID != "" {
			var seen bool
			if d, seen = display[m.SessionID]; !seen {
				s, err := tx.Session(ctx, m.SessionID)
				switch {
				case err == nil:
					d = s.DisplayName
				case errors.Is(err, store.ErrNotFound):
					d = m.Sender
				default:
					return nil, FromStore(err)
				}
				display[m.SessionID] = d
			}
		}
		out = append(out, bus.Message{
			Seq: m.Seq, Channel: name, Sender: m.Sender, SenderKind: bus.IdentityKind(m.SenderKind), Session: m.SessionID, Display: d,
			CreatedAt: m.CreatedAt, Type: m.Type, Subject: m.Subject, Content: m.Content,
			ReplyTo: m.ReplyTo, Metadata: m.Metadata, MemoryID: m.MemoryID, Revision: m.Revision,
			Tags: m.Tags,
		})
	}
	return out, nil
}

// KindFromBus maps a bus kind; "" is ordinary and an unknown kind is a
// validation error.
func KindFromBus(k bus.Kind) (store.Kind, error) {
	switch k {
	case "", bus.KindOrdinary:
		return store.KindOrdinary, nil
	case bus.KindMemory:
		return store.KindMemory, nil
	case bus.KindTasks:
		return store.KindTasks, nil
	}
	return "", E(bus.CodeValidation, "unknown channel kind %q", string(k))
}

// KindToBus maps a stored kind; "" is ordinary.
func KindToBus(k store.Kind) bus.Kind {
	if k == "" {
		return bus.KindOrdinary
	}
	return bus.Kind(k)
}

// ACLFromBus converts ACL entries, rejecting an unknown permission. A nil
// slice stays nil (the configured default); an empty one stays empty.
func ACLFromBus(in []bus.ACLEntry) ([]store.ACLEntry, error) {
	if in == nil {
		return nil, nil
	}
	out := make([]store.ACLEntry, 0, len(in))
	for _, e := range in {
		var perms []store.Permission
		for _, p := range e.Permissions {
			sp := store.Permission(p)
			if !store.ValidPermission(sp) {
				return nil, E(bus.CodeValidation, "unknown permission %q", string(p))
			}
			perms = append(perms, sp)
		}
		out = append(out, store.ACLEntry{Identity: e.Identity, Permissions: perms})
	}
	return out, nil
}

// ACLToBus converts stored ACL entries.
func ACLToBus(in []store.ACLEntry) []bus.ACLEntry {
	if in == nil {
		return nil
	}
	out := make([]bus.ACLEntry, 0, len(in))
	for _, e := range in {
		var perms []bus.Permission
		for _, p := range e.Permissions {
			perms = append(perms, bus.Permission(p))
		}
		out = append(out, bus.ACLEntry{Identity: e.Identity, Permissions: perms})
	}
	return out
}

// ChannelToBus is the name and kind a caller that may list the channel sees
// (a stored ACL entry, or the administrator flag), with an empty capability
// set; List and Status set the capabilities from the bridge state.
func ChannelToBus(c store.Channel) bus.Channel {
	return bus.Channel{Name: c.Name, Kind: KindToBus(c.Kind), Capabilities: []bus.ChannelCapability{}}
}

// ChannelInfoToBus adds the creator and creation time, for CreateChannel and
// Status.
func ChannelInfoToBus(c store.Channel) bus.ChannelInfo {
	return bus.ChannelInfo{Channel: ChannelToBus(c), Creator: c.Creator, Scope: c.Scope, CreatedAt: c.CreatedAt}
}

// ScopeToBus converts a stored scope. Channels and Identities stay nil; Get
// fills them.
func ScopeToBus(s store.Scope) bus.Scope {
	return bus.Scope{
		Name: s.Name, State: bus.ScopeState(s.State), Retire: bus.RetireMode(s.Retire),
		CreatedAt: s.CreatedAt, DoneAt: s.DoneAt, LastActivity: s.LastActivity,
	}
}
