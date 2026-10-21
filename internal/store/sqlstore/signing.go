package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// The signing key row is new in v2 (Decision (Eric, 2026-10-21), Milestone 5
// item 2); no v1 source.

type signingKeyRow struct {
	KID           string `db:"kid"`
	Algorithm     string `db:"algorithm"`
	PrivateKeyPEM string `db:"private_key_pem"`
	CreatedAt     int64  `db:"created_at"`
}

func (t *tx) PutSigningKey(ctx context.Context, k store.SigningKey) error {
	if err := t.writable(); err != nil {
		return err
	}
	if k.KID == "" || k.PrivateKeyPEM == "" {
		return fmt.Errorf("%w: signing key kid and private key must not be empty", store.ErrInvalid)
	}
	if err := checkText("signing key", "kid", k.KID, "private key", k.PrivateKeyPEM); err != nil {
		return err
	}
	if k.Algorithm != "EdDSA" && k.Algorithm != "ES256" {
		return fmt.Errorf("%w: unknown signing key algorithm %q (want EdDSA or ES256)", store.ErrInvalid, k.Algorithm)
	}
	if k.CreatedAt <= 0 {
		return fmt.Errorf("%w: signing key created_at must be positive", store.ErrInvalid)
	}
	_, err := t.execW(ctx, "INSERT INTO signing_keys (kid, singleton, algorithm, private_key_pem, created_at) VALUES (?, 1, ?, ?, ?)",
		k.KID, k.Algorithm, k.PrivateKeyPEM, k.CreatedAt)
	if isConflict(err) {
		return fmt.Errorf("%w: a signing key already exists", store.ErrConflict)
	}
	return err
}

func (t *tx) SigningKey(ctx context.Context) (store.SigningKey, error) {
	var r signingKeyRow
	err := t.get(ctx, &r, "SELECT kid, algorithm, private_key_pem, created_at FROM signing_keys")
	if errors.Is(err, sql.ErrNoRows) {
		return store.SigningKey{}, fmt.Errorf("%w: signing key", store.ErrNotFound)
	}
	if err != nil {
		return store.SigningKey{}, err
	}
	return store.SigningKey{KID: r.KID, Algorithm: r.Algorithm, PrivateKeyPEM: r.PrivateKeyPEM, CreatedAt: r.CreatedAt}, nil
}
