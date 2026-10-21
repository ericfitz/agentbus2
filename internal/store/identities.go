package store

import "context"

// Identity is a registered principal (Decisions 55, 80).
type Identity struct {
	Name         string
	Admin        bool
	TagsAckedSeq int64 // per-identity acked mark for tag-match delivery (plan choice 7)
	CreatedAt    int64
}

// PublicKey is a credential of an identity (Decision 55).
type PublicKey struct {
	Identity  string
	KID       string
	Algorithm string // "EdDSA" or "ES256"
	Key       []byte // PKIX DER
	CreatedAt int64
}

// Special identities that ACL entries may name (Decision 80). They are
// reserved as identity names (Gate 0 item 1, option A).
const (
	Everyone = "everyone"
	Admins   = "admins"
)

type Identities interface {
	// CreateIdentity: ErrInvalid (NameRule, reserved name; pending Eric:
	// Gate 0 item 1), ErrConflict.
	CreateIdentity(ctx context.Context, name string, now int64) error
	Identity(ctx context.Context, name string) (Identity, error) // ErrNotFound
	ListIdentities(ctx context.Context) ([]Identity, error)      // by name, in byte order
	SetAdmin(ctx context.Context, name string, admin bool) error // ErrNotFound
	// AdvanceTagAckedMark sets the mark to max(current, seq).
	AdvanceTagAckedMark(ctx context.Context, name string, seq int64) error
	// PutPublicKey upserts by (identity, kid); ErrInvalid on an unknown
	// Algorithm, an empty KID or an empty Key; ErrNotFound on an unknown
	// identity.
	PutPublicKey(ctx context.Context, k PublicKey) error
	// PublicKeys lists by kid in byte order; an unknown identity has none (an
	// empty list, no error).
	PublicKeys(ctx context.Context, name string) ([]PublicKey, error)
	// DeletePublicKeys is reset-credential (Decision 73): it returns how many
	// keys it removed, 0 (no error) for an identity with none or an unknown one,
	// so a repeated reset is harmless.
	DeletePublicKeys(ctx context.Context, name string) (int, error)
}
