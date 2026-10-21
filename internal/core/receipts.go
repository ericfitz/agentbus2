package core

import (
	"context"
	"encoding/json"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// WithReceipt makes a mutating operation idempotent (Decisions 46, 67; v1
// internal/bus/receipts.go checkReceipt and storeReceipt), keyed per
// identity. key "" runs fn and stores nothing. A stored receipt with an equal
// payload fingerprint replays: its result is unmarshaled into result, fn does
// not run and replayed is true. A receipt with another fingerprint is a
// conflict. On a miss fn runs and, when it succeeds, result is stored with
// an expiry of ReceiptRetentionMinutes. result must be a pointer. Receipts
// are checked before any mutable lookup, so a replay survives the deletion of
// what the first call created.
func (c *Core) WithReceipt(ctx context.Context, tx store.Tx, identity, key string, payload any, result any, fn func() error) (bool, error) {
	if key == "" {
		return false, fn()
	}
	now := c.NowMs()
	fp, err := rules.Fingerprint(payload)
	if err != nil {
		return false, bus.Internal(err)
	}
	r, ok, err := tx.Receipt(ctx, identity, key, now)
	if err != nil {
		return false, FromStore(err)
	}
	if ok {
		if r.Fingerprint != fp {
			return false, E(bus.CodeConflict, "idempotency_key %q was used with a different payload", key)
		}
		if err := json.Unmarshal(r.Result, result); err != nil {
			return false, bus.Internal(err)
		}
		return true, nil
	}
	if err := fn(); err != nil {
		return false, err
	}
	rj, err := json.Marshal(result)
	if err != nil {
		return false, bus.Internal(err)
	}
	exp := now + int64(c.Cfg.ReceiptRetentionMinutes)*60_000
	if err := tx.PutReceipt(ctx, store.Receipt{Identity: identity, Key: key, Fingerprint: fp, Result: rj, ExpiresAt: exp}); err != nil {
		return false, FromStore(err)
	}
	return false, nil
}
