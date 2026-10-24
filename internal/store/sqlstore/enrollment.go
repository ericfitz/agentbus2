package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Enrollment tokens are new in v2 (Decision 20); no v1 source.

type enrollmentRow struct {
	ID        string `db:"id"`
	TokenHash string `db:"token_hash"`
	Identity  string `db:"identity"`
	ExpiresAt int64  `db:"expires_at"`
	UsesLeft  int    `db:"uses_left"`
	CreatedAt int64  `db:"created_at"`
}

const enrollmentCols = "id, token_hash, identity, expires_at, uses_left, created_at"

func (r enrollmentRow) token() store.EnrollmentToken {
	return store.EnrollmentToken{ID: r.ID, TokenHash: r.TokenHash, Identity: r.Identity, ExpiresAt: r.ExpiresAt, UsesLeft: r.UsesLeft, CreatedAt: r.CreatedAt}
}

func notFoundToken(id string) error {
	return fmt.Errorf("%w: enrollment token %q", store.ErrNotFound, id)
}

func (t *tx) PutEnrollmentToken(ctx context.Context, e store.EnrollmentToken) error {
	if err := t.writable(); err != nil {
		return err
	}
	if e.ID == "" || e.TokenHash == "" {
		return fmt.Errorf("%w: enrollment token id and hash must not be empty", store.ErrInvalid)
	}
	if err := checkText("enrollment token", "id", e.ID, "token hash", e.TokenHash); err != nil {
		return err
	}
	if e.UsesLeft < 1 {
		return fmt.Errorf("%w: enrollment token uses must be at least 1", store.ErrInvalid)
	}
	if e.ExpiresAt <= 0 {
		return fmt.Errorf("%w: enrollment token expiry must be positive", store.ErrInvalid)
	}
	if e.Identity != "" {
		if err := store.NameRule(e.Identity); err != nil {
			return fmt.Errorf("%w: enrollment token identity %q %s", store.ErrInvalid, e.Identity, err)
		}
		if store.ReservedIdentity(e.Identity) {
			return fmt.Errorf("%w: enrollment token identity %q is reserved", store.ErrInvalid, e.Identity)
		}
	}
	_, err := t.execW(ctx, "INSERT INTO enrollment_tokens ("+enrollmentCols+") VALUES (?, ?, ?, ?, ?, ?)",
		e.ID, e.TokenHash, e.Identity, e.ExpiresAt, e.UsesLeft, e.CreatedAt)
	if isConflict(err) {
		return fmt.Errorf("%w: enrollment token id or hash already exists", store.ErrConflict)
	}
	return err
}

// EnrollmentTokenByHash answers ErrNotFound for a hash failing TextRule
// without a query: no such hash can be stored.
func (t *tx) EnrollmentTokenByHash(ctx context.Context, hash string) (store.EnrollmentToken, error) {
	if store.TextRule(hash) != nil {
		return store.EnrollmentToken{}, fmt.Errorf("%w: enrollment token", store.ErrNotFound)
	}
	var r enrollmentRow
	err := t.get(ctx, &r, "SELECT "+enrollmentCols+" FROM enrollment_tokens WHERE token_hash = ?", hash)
	if errors.Is(err, sql.ErrNoRows) {
		return store.EnrollmentToken{}, fmt.Errorf("%w: enrollment token", store.ErrNotFound)
	}
	if err != nil {
		return store.EnrollmentToken{}, err
	}
	return r.token(), nil
}

// ListEnrollmentTokens sorts in Go (CreatedAt, then ID in byte order): the
// engines collate text differently (see sortByBytes).
func (t *tx) ListEnrollmentTokens(ctx context.Context) ([]store.EnrollmentToken, error) {
	var rows []enrollmentRow
	if err := t.sel(ctx, &rows, "SELECT "+enrollmentCols+" FROM enrollment_tokens"); err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(a, b enrollmentRow) int {
		switch {
		case a.CreatedAt < b.CreatedAt:
			return -1
		case a.CreatedAt > b.CreatedAt:
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	out := make([]store.EnrollmentToken, len(rows))
	for i, r := range rows {
		out[i] = r.token()
	}
	return out, nil
}

// ConsumeEnrollmentToken is one conditional UPDATE whose rows-affected decides
// the result, after a SELECT that gives ErrNotFound for an unknown id.
func (t *tx) ConsumeEnrollmentToken(ctx context.Context, id string, now int64) (bool, error) {
	if err := t.writable(); err != nil {
		return false, err
	}
	if store.TextRule(id) != nil {
		return false, notFoundToken(id)
	}
	var n int
	if err := t.get(ctx, &n, "SELECT count(*) FROM enrollment_tokens WHERE id = ?", id); err != nil {
		return false, err
	}
	if n == 0 {
		return false, notFoundToken(id)
	}
	res, err := t.execW(ctx, "UPDATE enrollment_tokens SET uses_left = uses_left - 1 WHERE id = ? AND uses_left > 0 AND expires_at > ?", id, now)
	if err != nil {
		return false, err
	}
	aff, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return aff == 1, nil
}

func (t *tx) DeleteEnrollmentToken(ctx context.Context, id string) error {
	if err := t.writable(); err != nil {
		return err
	}
	if store.TextRule(id) != nil {
		return notFoundToken(id)
	}
	res, err := t.execW(ctx, "DELETE FROM enrollment_tokens WHERE id = ?", id)
	if err != nil {
		return err
	}
	aff, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if aff == 0 {
		return notFoundToken(id)
	}
	return nil
}

// DeleteEnrollmentTokensFor deletes every token bound to identity; 0, nil when
// there is none (or the name fails TextRule, which no token can carry).
func (t *tx) DeleteEnrollmentTokensFor(ctx context.Context, identity string) (int, error) {
	if err := t.writable(); err != nil {
		return 0, err
	}
	if badText(identity) {
		return 0, nil
	}
	res, err := t.execW(ctx, "DELETE FROM enrollment_tokens WHERE identity = ?", identity)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
