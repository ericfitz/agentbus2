package core

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/store"
)

// RequireServerAdmin resolves identity (not_registered for "", a reserved name
// or an unknown one) and asks the one access rule whether it may administer
// the server (Decision 73): forbidden "administrator privilege required"
// otherwise. It is the single entry the server-administration sites call.
func (c *Core) RequireServerAdmin(ctx context.Context, tx store.Tx, identity string) error {
	p, err := access.Resolve(ctx, tx, identity)
	if err != nil {
		return err
	}
	return c.Access.Require(ctx, tx, p, access.Server{}, access.Administer)
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
