package store

import "context"

// Identity is a registered principal (Decisions 55, 80).
type Identity struct {
	Name         string
	Kind         IdentityKind // agent or human; CreateIdentity makes an agent
	Admin        bool
	TagsAckedSeq int64 // per-identity acked mark for tag-match delivery (plan choice 7)
	CreatedAt    int64
}

// IdentityKind says whether an identity is an agent or a human.
type IdentityKind string

const (
	IdentityAgent IdentityKind = "agent"
	IdentityHuman IdentityKind = "human"
)

// ValidIdentityKind reports whether k is exactly agent or human.
func ValidIdentityKind(k IdentityKind) bool { return k == IdentityAgent || k == IdentityHuman }

// ClaimEmail is the one binding claim type.
const ClaimEmail = "email"

// HumanBinding ties a human identity to the claim that names it. Issuer is
// the provider's iss URL; Issuer and ProviderSubject are "" until the first
// sign-in and after a reset.
type HumanBinding struct {
	Identity, Issuer, ClaimType, Value, ProviderSubject string
	CreatedAt                                           int64
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
	// CreateIdentity: ErrInvalid (NameRule, reserved name;
	// Decision (Eric, 2026-10-21): Gate 0 item 1), ErrConflict.
	CreateIdentity(ctx context.Context, name string, now int64) error
	Identity(ctx context.Context, name string) (Identity, error) // ErrNotFound
	ListIdentities(ctx context.Context) ([]Identity, error)      // by name, in byte order
	SetAdmin(ctx context.Context, name string, admin bool) error // ErrNotFound
	// AdvanceTagAckedMark sets the mark to max(current, seq).
	AdvanceTagAckedMark(ctx context.Context, name string, seq int64) error
	// CreateHuman creates the identity row (kind human) and its binding.
	// ErrInvalid: a name failing NameRule or reserved, a ClaimType other than
	// ClaimEmail, an empty Value, a subject without an issuer (or the reverse),
	// or text failing TextRule. ErrConflict: the name exists, the
	// (claim_type, value) is bound, or a non-empty (issuer, provider_subject)
	// is bound.
	CreateHuman(ctx context.Context, b HumanBinding, now int64) error
	// MarkHuman turns an agent into a human; idempotent for a human.
	// ErrNotFound; ErrConflict when the identity has keys.
	MarkHuman(ctx context.Context, name string) error
	// HumanBinding, HumanBindingByValue and HumanBindingBySubject answer
	// ErrNotFound for no row and for text failing TextRule, without a query.
	HumanBinding(ctx context.Context, identity string) (HumanBinding, error)
	HumanBindingByValue(ctx context.Context, claimType, value string) (HumanBinding, error)
	HumanBindingBySubject(ctx context.Context, issuer, subject string) (HumanBinding, error)
	// SetHumanSubject records the provider subject; both "" clears it.
	// ErrNotFound with no binding; ErrConflict when the pair is bound to
	// another identity; ErrInvalid when exactly one is empty or on text
	// failing TextRule.
	SetHumanSubject(ctx context.Context, identity, issuer, subject string) error
	// PutPublicKey upserts by (identity, kid); ErrInvalid on an unknown
	// Algorithm, an empty KID or one failing TextRule (not valid UTF-8, or a
	// NUL) or an empty Key; ErrNotFound on an unknown identity.
	PutPublicKey(ctx context.Context, k PublicKey) error
	// PublicKeys lists by kid in byte order; an unknown identity has none (an
	// empty list, no error).
	PublicKeys(ctx context.Context, name string) ([]PublicKey, error)
	// DeletePublicKeys is reset-credential (Decision 73): it returns how many
	// keys it removed, 0 (no error) for an identity with none or an unknown one,
	// so a repeated reset is harmless.
	DeletePublicKeys(ctx context.Context, name string) (int, error)
}
