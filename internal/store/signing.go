package store

import "context"

// SigningKey is one row of the server's token signing key set (Eric,
// 2026-10-27, issue #6): at most one active key signs, retiring keys stay
// published until RemoveAfter, and every replica reads the same set.
type SigningKey struct {
	KID      string
	State    string // SigningKeyActive or SigningKeyRetiring
	Provider string // one of SigningProviders
	// ProviderRef names the key inside an external provider (a KMS key URI);
	// empty for the store provider, required for every other.
	ProviderRef string
	Algorithm   string // EdDSA or ES256, the identity_keys CHECK values
	PublicKey   []byte // SPKI DER
	// PrivateKeyPEM is one PKCS#8 "PRIVATE KEY" block, stored in clear
	// (database read access equals admin); store provider only.
	PrivateKeyPEM string
	// CreatedAt, RetiredAt and RemoveAfter are Unix milliseconds. RetiredAt
	// and RemoveAfter are 0 while the key is active.
	CreatedAt   int64
	RetiredAt   int64
	RemoveAfter int64
}

const (
	SigningKeyActive   = "active"
	SigningKeyRetiring = "retiring"

	// SigningProviderStore keeps the private key in the database.
	SigningProviderStore = "store"
)

// SigningProviders are the provider names the signing_keys CHECK accepts.
var SigningProviders = []string{"store", "awskms", "gcpkms", "azurekms", "hashivault"}

type SigningKeys interface {
	// PutSigningKey inserts one row. ErrInvalid: an empty KID or PublicKey, a
	// State or Provider outside the sets, an Algorithm other than EdDSA or
	// ES256, CreatedAt <= 0, an active row with a non-zero RetiredAt or
	// RemoveAfter, a retiring row with RetiredAt <= 0 or RemoveAfter <
	// RetiredAt, a store row with an empty PrivateKeyPEM or a non-empty
	// ProviderRef, any other provider with a PrivateKeyPEM or an empty
	// ProviderRef. ErrConflict: a duplicate KID or a second active row (the
	// partial unique index serializes concurrent inserters on both engines).
	PutSigningKey(ctx context.Context, k SigningKey) error
	// ListSigningKeys returns the active row first, then the retiring rows by
	// RetiredAt descending, then KID ascending; an empty non-nil slice when
	// there are none.
	ListSigningKeys(ctx context.Context) ([]SigningKey, error)
	// RetireSigningKey is a compare-and-swap from active to retiring: it
	// updates the row only while it is active. ErrNotFound when no active row
	// has that kid (absent, or already retiring); ErrInvalid for retiredAt <= 0
	// or removeAfter < retiredAt.
	RetireSigningKey(ctx context.Context, kid string, retiredAt, removeAfter int64) error
	// DeleteSigningKey removes the row; ErrNotFound when absent.
	DeleteSigningKey(ctx context.Context, kid string) error
	// DeleteSigningKeysBefore deletes the retiring rows whose RemoveAfter is
	// below now and returns how many; it never deletes an active row.
	DeleteSigningKeysBefore(ctx context.Context, now int64) (int, error)
}
