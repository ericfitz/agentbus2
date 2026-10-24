package identities

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

// TokenInput describes an enrollment token to create.
type TokenInput struct {
	Identity string        // "" any free name or keyless non-admin row
	Expires  time.Duration // 0: 24 h; otherwise at least one millisecond
	Uses     int           // 0: 1
}

// HashToken is the stored form of a token: sha256 in hex.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateEnrollmentToken mints a token and stores its hash. The token is
// returned once and never stored.
func (s Service) CreateEnrollmentToken(ctx context.Context, in TokenInput) (string, store.EnrollmentToken, error) {
	if in.Expires < 0 {
		return "", store.EnrollmentToken{}, core.E(bus.CodeValidation, "expires must not be negative")
	}
	// The Store keeps milliseconds: a shorter positive Expires would truncate
	// to a token that expired when it was minted.
	if in.Expires > 0 && in.Expires < time.Millisecond {
		return "", store.EnrollmentToken{}, core.E(bus.CodeValidation, "expires must be at least one millisecond")
	}
	if in.Uses < 0 {
		return "", store.EnrollmentToken{}, core.E(bus.CodeValidation, "uses must not be negative")
	}
	if in.Expires == 0 {
		in.Expires = 24 * time.Hour
	}
	if in.Uses == 0 {
		in.Uses = 1
	}
	token, row, err := s.mintToken(in.Identity, in.Expires, in.Uses)
	if err != nil {
		return "", store.EnrollmentToken{}, err
	}
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error { return tx.PutEnrollmentToken(ctx, row) })
	if err != nil {
		return "", store.EnrollmentToken{}, core.FromStore(err)
	}
	return token, row, nil
}

// ListEnrollmentTokens lists the stored tokens (hashes only).
func (s Service) ListEnrollmentTokens(ctx context.Context) ([]store.EnrollmentToken, error) {
	var out []store.EnrollmentToken
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.ListEnrollmentTokens(ctx)
		return err
	})
	return out, core.FromStore(err)
}

// RevokeEnrollmentToken deletes the token with the given id (not_found).
func (s Service) RevokeEnrollmentToken(ctx context.Context, id string) error {
	return core.FromStore(s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.DeleteEnrollmentToken(ctx, id)
	}))
}

// mintToken builds a token and its row without storing either: the caller
// puts the row in its own transaction. expires and uses must be positive.
func (s Service) mintToken(identity string, expires time.Duration, uses int) (string, store.EnrollmentToken, error) {
	rnd, err := core.RandomToken(32)
	if err != nil {
		return "", store.EnrollmentToken{}, bus.Internal(err)
	}
	token := "abe_" + rnd
	id, err := core.RandomHex(8)
	if err != nil {
		return "", store.EnrollmentToken{}, bus.Internal(err)
	}
	now := s.NowMs()
	row := store.EnrollmentToken{
		ID: id, TokenHash: HashToken(token), Identity: identity,
		ExpiresAt: now + expires.Milliseconds(), UsesLeft: uses, CreatedAt: now,
	}
	return token, row, nil
}
