package store

import "context"

// Receipt is a stored idempotent-send result.
type Receipt struct {
	Identity, Key, Fingerprint string
	Result                     []byte
	ExpiresAt                  int64
}

type Receipts interface {
	// Receipt returns an unexpired receipt only (expires_at > now).
	Receipt(ctx context.Context, identity, key string, now int64) (Receipt, bool, error)
	PutReceipt(ctx context.Context, r Receipt) error // replace
	DeleteExpiredReceipts(ctx context.Context, now int64, limit int) (int, error)
}
