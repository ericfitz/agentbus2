package store

import "context"

// EnrollmentToken authorizes registration on a listener that requires
// enrollment (Decision 20). Only the hash of the token is stored.
type EnrollmentToken struct {
	ID        string // 16 hex, random; what list and revoke name
	TokenHash string // sha256 hex of the token
	Identity  string // "" or the name the token is bound to
	ExpiresAt int64
	UsesLeft  int
	CreatedAt int64
}

type EnrollmentTokens interface {
	// PutEnrollmentToken: ErrInvalid on an empty ID or TokenHash, UsesLeft < 1,
	// ExpiresAt <= 0, or a non-empty Identity failing NameRule or reserved (slack-bridge included);
	// ErrConflict when the ID or TokenHash is taken.
	PutEnrollmentToken(ctx context.Context, t EnrollmentToken) error
	// EnrollmentTokenByHash: ErrNotFound.
	EnrollmentTokenByHash(ctx context.Context, hash string) (EnrollmentToken, error)
	// ListEnrollmentTokens lists by CreatedAt, then ID in byte order.
	ListEnrollmentTokens(ctx context.Context) ([]EnrollmentToken, error)
	// ConsumeEnrollmentToken decrements UsesLeft and returns true when
	// UsesLeft > 0 and ExpiresAt > now; otherwise it returns false and changes
	// nothing. ErrNotFound for an unknown id.
	ConsumeEnrollmentToken(ctx context.Context, id string, now int64) (bool, error)
	// DeleteEnrollmentToken: ErrNotFound.
	DeleteEnrollmentToken(ctx context.Context, id string) error
	// DeleteEnrollmentTokensFor deletes every token bound to identity and
	// returns how many; 0, nil for none.
	DeleteEnrollmentTokensFor(ctx context.Context, identity string) (int, error)
}
