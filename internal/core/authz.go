package core

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/store"
)

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
// Resolve, it refuses "" and the reserved names with not_registered.
func (c *Core) ReadableChannelIDs(ctx context.Context, tx store.Tx, identity string) ([]int64, error) {
	if reservedIdentity(identity) {
		return nil, notRegistered(identity)
	}
	ids, err := tx.ReadableChannels(ctx, identity)
	return ids, FromStore(err)
}

// RequireChannelOr is Access.Require on a channel for the by-id operations
// (memories, tasks): a denial for a channel outside the principal's scope is
// missing(), the operation's own not-found answer, so a scoped caller cannot
// tell the id from one that does not exist (accesscheck spec 4.1 (d)). Every
// other denial is the access check's, unchanged.
func (c *Core) RequireChannelOr(ctx context.Context, tx store.Tx, p access.Principal, channelID int64, a access.Action, missing func() error) error {
	d, err := c.Access.Check(ctx, tx, p, access.Channel{ID: channelID}, a)
	if err != nil {
		return err
	}
	if d.Allowed {
		return nil
	}
	if d.Reason == access.ReasonOutOfScope {
		return missing()
	}
	return bus.Errorf(d.Code, false, "%s", d.Text)
}
