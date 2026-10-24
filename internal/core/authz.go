package core

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Authorize is the only ACL check (Decision 80, spec section 4.6): it reads
// the identity's effective permissions on the channel inside tx and refuses
// with forbidden when p is missing. An unknown p is a programming error.
// identity must be a registered name: "" and the reserved names everyone and
// admins are refused with not_registered, since EffectivePermissions would
// evaluate them literally (everyone and admins hold ACL rows of their own).
// Registration is not checked here; callers run RequireIdentity first.
func (c *Core) Authorize(ctx context.Context, tx store.Tx, identity string, channelID int64, p store.Permission) error {
	if !store.ValidPermission(p) {
		panic("core: Authorize with unknown permission " + string(p))
	}
	if reservedIdentity(identity) {
		return notRegistered(identity)
	}
	perms, err := tx.EffectivePermissions(ctx, channelID, identity)
	if err != nil {
		return FromStore(err)
	}
	var ok bool
	switch p {
	case store.PermRead:
		ok = perms.Read
	case store.PermWrite:
		ok = perms.Write
	case store.PermAdmin:
		ok = perms.Admin
	}
	if ok {
		return nil
	}
	name := ""
	if ch, err := tx.Channel(ctx, channelID); err == nil {
		name = ch.Name
	}
	return E(bus.CodeForbidden, "%s needs %s on channel %q", identity, p, name)
}

// reservedIdentity reports "" and the reserved names (everyone, admins and
// slack-bridge), none of which is an identity that can act through the Bus.
func reservedIdentity(name string) bool {
	return name == "" || store.ReservedIdentity(name)
}

func notRegistered(name string) error {
	return E(bus.CodeNotRegistered, "%q is not a registered identity", name)
}

// RequireIdentity returns the registered identity, refusing "" and the
// reserved names everyone and admins (not_registered).
func (c *Core) RequireIdentity(ctx context.Context, tx store.Tx, identity string) (store.Identity, error) {
	if reservedIdentity(identity) {
		return store.Identity{}, notRegistered(identity)
	}
	id, err := tx.Identity(ctx, identity)
	if errors.Is(err, store.ErrNotFound) {
		return store.Identity{}, notRegistered(identity)
	}
	if err != nil {
		return store.Identity{}, FromStore(err)
	}
	return id, nil
}

// RequireAdmin is RequireIdentity plus the administrator flag (Decision 73).
func (c *Core) RequireAdmin(ctx context.Context, tx store.Tx, identity string) (store.Identity, error) {
	id, err := c.RequireIdentity(ctx, tx, identity)
	if err != nil {
		return store.Identity{}, err
	}
	if !id.Admin {
		return store.Identity{}, E(bus.CodeForbidden, "administrator privilege required")
	}
	return id, nil
}

// ChannelByName looks a channel up, not_found when it does not exist.
func (c *Core) ChannelByName(ctx context.Context, tx store.Tx, name string) (store.Channel, error) {
	ch, err := tx.ChannelByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Channel{}, E(bus.CodeNotFound, "channel %q does not exist", name)
	}
	if err != nil {
		return store.Channel{}, FromStore(err)
	}
	return ch, nil
}

// ReadableChannelIDs lists the ids of the channels identity may read. Like
// Authorize, it refuses "" and the reserved names with not_registered.
func (c *Core) ReadableChannelIDs(ctx context.Context, tx store.Tx, identity string) ([]int64, error) {
	if reservedIdentity(identity) {
		return nil, notRegistered(identity)
	}
	ids, err := tx.ReadableChannels(ctx, identity)
	return ids, FromStore(err)
}
