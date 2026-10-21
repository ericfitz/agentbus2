package store

// Shared aggregate fixtures, written once by Task 3 and never edited by a
// parallel task (a gap is reported to the integrator). Every file of this
// package imports the contract as
//
//	ab2store "github.com/ericfitz/agentbus2/internal/store"
//
// The aggregate fixtures compile against the Store interfaces; a failing Store
// call fails the calling test with that error.

import (
	"context"
	"testing"

	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// fixedNow is every fixture's timestamp.
const fixedNow int64 = 1_700_000_000_000

type txFunc = func(ctx context.Context, tx ab2store.Tx) error

// mustTx runs fn in Store.Tx and fails the test on error.
//
//nolint:unused // used by the aggregate tasks (4 to 11)
func mustTx(t *testing.T, s ab2store.Store, fn txFunc) {
	t.Helper()
	if err := s.Tx(context.Background(), fn); err != nil {
		t.Fatalf("Tx: %v", err)
	}
}

// mustAppendTx runs fn in Store.Append and fails the test on error.
//
//nolint:unused // used by the aggregate tasks (4 to 11)
func mustAppendTx(t *testing.T, s ab2store.Store, fn txFunc) {
	t.Helper()
	if err := s.Append(context.Background(), fn); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// mustRead runs fn in Store.Read and fails the test on error.
//
//nolint:unused // used by the aggregate tasks (4 to 11)
func mustRead(t *testing.T, s ab2store.Store, fn txFunc) {
	t.Helper()
	if err := s.Read(context.Background(), fn); err != nil {
		t.Fatalf("Read: %v", err)
	}
}

// mustIdentity creates the identity name at fixedNow.
//
//nolint:unused // used by the aggregate tasks (4 to 11)
func mustIdentity(t *testing.T, s ab2store.Store, name string) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		return tx.CreateIdentity(ctx, name, fixedNow)
	})
}

// mustAdmin creates the identity name and grants it the admin flag.
//
//nolint:unused // used by the aggregate tasks (4 to 11)
func mustAdmin(t *testing.T, s ab2store.Store, name string) {
	t.Helper()
	mustIdentity(t, s, name)
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		return tx.SetAdmin(ctx, name, true)
	})
}

// mustChannel creates a channel with CreatedSeq = MaxSeq() and CreatedAt =
// fixedNow. A nil acl means "without an ACL" (the Store substitutes the
// default, plan choice 9).
//
//nolint:unused // used by the aggregate tasks (4 to 11)
func mustChannel(t *testing.T, s ab2store.Store, name string, kind ab2store.Kind, creator string, acl []ab2store.ACLEntry) ab2store.Channel {
	t.Helper()
	var ch ab2store.Channel
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		ch, err = tx.CreateChannel(ctx, ab2store.ChannelCreate{
			Name: name, Kind: kind, Creator: creator, ACL: acl,
			CreatedSeq: seq, CreatedAt: fixedNow,
		})
		return err
	})
	return ch
}

// mustAppend appends m inside Store.Append and returns its seq; CreatedAt
// defaults to fixedNow when 0.
//
//nolint:unused // used by the aggregate tasks (4 to 11)
func mustAppend(t *testing.T, s ab2store.Store, m ab2store.NewMessage) int64 {
	t.Helper()
	if m.CreatedAt == 0 {
		m.CreatedAt = fixedNow
	}
	var seq int64
	mustAppendTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		seq, err = tx.AppendMessage(ctx, m)
		return err
	})
	return seq
}

// mustTombstone tombstones seq at fixedNow; the row must have been live.
//
//nolint:unused // used by the aggregate tasks (4 to 11)
func mustTombstone(t *testing.T, s ab2store.Store, seq int64) {
	t.Helper()
	var did bool
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		did, err = tx.TombstoneMessage(ctx, seq, fixedNow)
		return err
	})
	if !did {
		t.Fatalf("TombstoneMessage(%d) = false, want true", seq)
	}
}
