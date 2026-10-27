// Package storetest holds helpers for tests that write to a SQLite store file
// past the Bus. It is imported by test code only.
package storetest

import (
	"context"
	"database/sql"
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

// NormalizationError is the start of the error the Store logs when a read
// finds an administrator or a human holding a scope (spec 4.3, 4.5).
const NormalizationError = "identity holds a scope it cannot hold"

// ScopedAdminRaw writes identity as a scoped agent of scope (creating the
// scope) into the SQLite file at path, then sets its administrator flag with
// raw SQL: the row the Store's read normalization logs and reads as unscoped.
// No Store path writes it (SetAdmin clears the scope).
func ScopedAdminRaw(t *testing.T, path, identity, scope string) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, path, sqlite.Options{})
	if err != nil {
		t.Fatalf("ScopedAdminRaw open: %v", err)
	}
	err = st.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.CreateScope(ctx, scope, 1); err != nil {
			return err
		}
		return tx.CreateScopedIdentity(ctx, identity, scope, 1)
	})
	_ = st.Close()
	if err != nil {
		t.Fatalf("ScopedAdminRaw %s %s: %v", identity, scope, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, "UPDATE identities SET is_admin = TRUE WHERE name = ?", identity); err != nil {
		t.Fatalf("ScopedAdminRaw set the flag: %v", err)
	}
}
