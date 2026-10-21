package store

import "context"

// Receipt is a stored idempotent-send result. Identity and Key are non-empty;
// Result is the JSON the core stores, so it must pass TextRule (valid UTF-8,
// no NUL; the column is TEXT).
type Receipt struct {
	Identity, Key, Fingerprint string
	Result                     []byte
	ExpiresAt                  int64
}

type Receipts interface {
	// Receipt returns an unexpired receipt only (expires_at > now).
	Receipt(ctx context.Context, identity, key string, now int64) (Receipt, bool, error)
	// PutReceipt replaces any receipt for (identity, key); ErrInvalid on an
	// empty Identity or Key and on an Identity, Key, Fingerprint or Result
	// failing TextRule (not valid UTF-8, or a NUL).
	PutReceipt(ctx context.Context, r Receipt) error
	// DeleteExpiredReceipts deletes up to limit receipts with expires_at <= now
	// and returns how many; ErrInvalid on a limit below 1. Any limit is safe:
	// the deletes are chunked.
	DeleteExpiredReceipts(ctx context.Context, now int64, limit int) (int, error)
}
