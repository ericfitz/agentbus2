package core

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// LiveRevision returns the live revision of a memory and its channel (v1
// liveRevision).
func (c *Core) LiveRevision(ctx context.Context, tx store.Tx, memoryID int64) (store.Message, store.Channel, error) {
	m, err := tx.LiveMemory(ctx, memoryID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Message{}, store.Channel{}, E(bus.CodeNotFound, "memory %d has no live revision", memoryID)
	}
	if err != nil {
		return store.Message{}, store.Channel{}, FromStore(err)
	}
	ch, err := tx.Channel(ctx, m.ChannelID)
	if err != nil {
		return store.Message{}, store.Channel{}, FromStore(err)
	}
	return m, ch, nil
}

// TombstoneMemory tombstones the revision at seq and drops its embedding and
// the memory's access row (v1 tombstoneMemoryRow). It returns false, changing
// nothing else, when the row was already tombstoned or is missing.
func (c *Core) TombstoneMemory(ctx context.Context, tx store.Tx, memoryID, seq, at int64) (bool, error) {
	ok, err := tx.TombstoneMessage(ctx, seq, at)
	if err != nil {
		return false, FromStore(err)
	}
	if !ok {
		return false, nil
	}
	if err := tx.DeleteEmbedding(ctx, seq); err != nil {
		return false, FromStore(err)
	}
	if err := tx.DeleteMemoryAccess(ctx, memoryID); err != nil {
		return false, FromStore(err)
	}
	return true, nil
}
