package core

import (
	"context"
	"encoding/json"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// CheckReceipt looks up the idempotency receipt of (identity, key) (Decisions
// 46, 67). key "" is no key: false, "", nil. A stored receipt with an equal
// payload fingerprint replays: its result is unmarshaled into result (a
// pointer) and replayed is true. A receipt with another fingerprint is a
// conflict. On a miss it returns the payload's fingerprint for StoreReceipt.
// It only reads, so it also runs in Store.Read.
func (c *Core) CheckReceipt(ctx context.Context, tx store.Tx, identity, key string, payload, result any) (replayed bool, fp string, err error) {
	if key == "" {
		return false, "", nil
	}
	fp, err = rules.Fingerprint(payload)
	if err != nil {
		return false, "", bus.Internal(err)
	}
	r, ok, err := tx.Receipt(ctx, identity, key, c.NowMs())
	if err != nil {
		return false, "", FromStore(err)
	}
	if !ok {
		return false, fp, nil
	}
	if r.Fingerprint != fp {
		return false, "", E(bus.CodeConflict, "idempotency_key %q was used with a different payload", key)
	}
	if err := json.Unmarshal(r.Result, result); err != nil {
		return false, "", bus.Internal(err)
	}
	return true, fp, nil
}

// StoreReceipt stores result under (identity, key) with the fingerprint
// CheckReceipt returned, expiring after ReceiptRetentionMinutes. key "" stores
// nothing.
func (c *Core) StoreReceipt(ctx context.Context, tx store.Tx, identity, key, fp string, result any) error {
	if key == "" {
		return nil
	}
	rj, err := json.Marshal(result)
	if err != nil {
		return bus.Internal(err)
	}
	exp := c.NowMs() + int64(c.Cfg.ReceiptRetentionMinutes)*60_000
	if err := tx.PutReceipt(ctx, store.Receipt{Identity: identity, Key: key, Fingerprint: fp, Result: rj, ExpiresAt: exp}); err != nil {
		return FromStore(err)
	}
	return nil
}

// WithReceipt makes a mutating operation idempotent (Decisions 46, 67; v1
// internal/bus/receipts.go checkReceipt and storeReceipt), keyed per
// identity. key "" runs fn and stores nothing. A replay returns true without
// running fn (see CheckReceipt); on a miss fn runs and, when it succeeds,
// result is stored (see StoreReceipt). result must be a pointer. Receipts
// are checked before any mutable lookup, so a replay survives the deletion of
// what the first call created.
func (c *Core) WithReceipt(ctx context.Context, tx store.Tx, identity, key string, payload any, result any, fn func() error) (bool, error) {
	replayed, fp, err := c.CheckReceipt(ctx, tx, identity, key, payload, result)
	if err != nil || replayed {
		return replayed, err
	}
	if err := fn(); err != nil {
		return false, err
	}
	return false, c.StoreReceipt(ctx, tx, identity, key, fp, result)
}
