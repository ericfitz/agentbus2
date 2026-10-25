package cli

import (
	"context"
	"testing"

	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// subscribeRaw writes a subscription straight into the default profile's
// SQLite file, past the Bus. The Bus refuses an agent's subscription to a
// memory channel (accesscheck spec 3.9); a row written before that rule, or by
// another path, is what these tests exercise delivery against.
func (e *testEnv) subscribeRaw(t *testing.T, identity, channel string) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, profile.SQLitePath(e.Config.Profiles["default"], e.DataDir), sqlite.Options{})
	if err != nil {
		t.Fatalf("subscribeRaw open: %v", err)
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
		t.Fatalf("subscribeRaw %s %s: %v", identity, channel, err)
	}
}
