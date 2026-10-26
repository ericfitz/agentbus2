// Package storetest holds helpers for tests that write to a SQLite store file
// past the Bus. It is imported by test code only.
package storetest

import (
	"context"
	"testing"

	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// SubscribeRaw writes a subscription for identity to the named channel
// straight into the SQLite file at path, from the current high-water mark. The
// Bus refuses an agent's subscription to a memory channel (accesscheck spec
// 3.9); a row written before that rule, or by another path, is what the
// delivery tests exercise.
func SubscribeRaw(t *testing.T, path, identity, channel string) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatalf("SubscribeRaw open: %v", err)
	}
	defer func() { _ = st.Close() }()
	err = st.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		ch, err := tx.ChannelByName(ctx, channel)
		if err != nil {
			return err
		}
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		return tx.Subscribe(ctx, identity, ch.ID, seq, 1)
	})
	if err != nil {
		t.Fatalf("SubscribeRaw %s %s: %v", identity, channel, err)
	}
}
