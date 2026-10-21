package sqlstore

import (
	"context"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// The assertion_jti table is new in v2 (Decision (Eric, 2026-10-21),
// Milestone 7 item 4); no v1 source.

func (t *tx) PutAssertionJTI(ctx context.Context, jti string, expiresAt int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	if jti == "" {
		return fmt.Errorf("%w: assertion jti must not be empty", store.ErrInvalid)
	}
	if len(jti) > store.MaxJTIBytes {
		return fmt.Errorf("%w: assertion jti is %d bytes, at most %d", store.ErrInvalid, len(jti), store.MaxJTIBytes)
	}
	if err := checkText("assertion", "jti", jti); err != nil {
		return err
	}
	if expiresAt <= 0 {
		return fmt.Errorf("%w: assertion expires_at must be positive", store.ErrInvalid)
	}
	_, err := t.execW(ctx, "INSERT INTO assertion_jti (jti, expires_at) VALUES (?, ?)", jti, expiresAt)
	if isConflict(err) {
		return fmt.Errorf("%w: assertion jti already used", store.ErrConflict)
	}
	return err
}

func (t *tx) DeleteAssertionJTIBefore(ctx context.Context, now int64) (int, error) {
	if err := t.writable(); err != nil {
		return 0, err
	}
	res, err := t.execW(ctx, "DELETE FROM assertion_jti WHERE expires_at < ?", now)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
