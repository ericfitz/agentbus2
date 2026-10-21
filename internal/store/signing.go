package store

import "context"

// SigningKey is the server's token signing key (Decision (Eric, 2026-10-21),
// Milestone 5 item 2): one row, shared by replicas.
type SigningKey struct {
	KID       string
	Algorithm string // EdDSA or ES256, the identity_keys CHECK values
	// PrivateKeyPEM is one PKCS#8 "PRIVATE KEY" block, stored in clear:
	// database read access equals admin.
	PrivateKeyPEM string
	CreatedAt     int64
}

type SigningKeys interface {
	// PutSigningKey: ErrInvalid on an empty KID or PrivateKeyPEM, an Algorithm
	// other than EdDSA or ES256, or CreatedAt <= 0; ErrConflict when a row
	// already exists (one key in 2.0.0; rotation is issue #6), enforced by the
	// singleton column's unique constraint so concurrent inserters are
	// serialized by the database on both engines.
	PutSigningKey(ctx context.Context, k SigningKey) error
	// SigningKey: ErrNotFound when no row exists.
	SigningKey(ctx context.Context) (SigningKey, error)
}
