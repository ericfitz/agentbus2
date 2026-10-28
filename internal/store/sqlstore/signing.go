package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/ericfitz/agentbus2/internal/store"
)

// The signing key set is new in v2 (Decision (Eric, 2026-10-21), Milestone 5
// item 2; a key set since Eric, 2026-10-27, issue #6); no v1 source.

type signingKeyRow struct {
	KID           string         `db:"kid"`
	State         string         `db:"state"`
	Provider      string         `db:"provider"`
	ProviderRef   string         `db:"provider_ref"`
	Algorithm     string         `db:"algorithm"`
	PublicKey     []byte         `db:"public_key"`
	PrivateKeyPEM sql.NullString `db:"private_key_pem"`
	CreatedAt     int64          `db:"created_at"`
	RetiredAt     int64          `db:"retired_at"`
	RemoveAfter   int64          `db:"remove_after"`
}

const signingKeyColumns = "kid, state, provider, provider_ref, algorithm, public_key, private_key_pem, created_at, retired_at, remove_after"

func validateSigningKey(k store.SigningKey) error {
	if k.KID == "" || len(k.PublicKey) == 0 {
		return fmt.Errorf("%w: signing key kid and public key must not be empty", store.ErrInvalid)
	}
	if err := checkText("signing key", "kid", k.KID, "provider ref", k.ProviderRef, "private key", k.PrivateKeyPEM); err != nil {
		return err
	}
	if k.State != store.SigningKeyActive && k.State != store.SigningKeyRetiring {
		return fmt.Errorf("%w: unknown signing key state %q (want %s or %s)", store.ErrInvalid, k.State, store.SigningKeyActive, store.SigningKeyRetiring)
	}
	if !slices.Contains(store.SigningProviders, k.Provider) {
		return fmt.Errorf("%w: unknown signing key provider %q (want one of %v)", store.ErrInvalid, k.Provider, store.SigningProviders)
	}
	if k.Algorithm != "EdDSA" && k.Algorithm != "ES256" {
		return fmt.Errorf("%w: unknown signing key algorithm %q (want EdDSA or ES256)", store.ErrInvalid, k.Algorithm)
	}
	if k.CreatedAt <= 0 {
		return fmt.Errorf("%w: signing key created_at must be positive", store.ErrInvalid)
	}
	if k.State == store.SigningKeyActive {
		if k.RetiredAt != 0 || k.RemoveAfter != 0 {
			return fmt.Errorf("%w: an active signing key has no retired_at or remove_after", store.ErrInvalid)
		}
	} else if err := checkRetirement(k.RetiredAt, k.RemoveAfter); err != nil {
		return err
	}
	if k.Provider == store.SigningProviderStore {
		if k.PrivateKeyPEM == "" || k.ProviderRef != "" {
			return fmt.Errorf("%w: a store signing key needs a private key and no provider ref", store.ErrInvalid)
		}
	} else if k.PrivateKeyPEM != "" || k.ProviderRef == "" {
		return fmt.Errorf("%w: a %s signing key needs a provider ref and no private key", store.ErrInvalid, k.Provider)
	}
	return nil
}

func checkRetirement(retiredAt, removeAfter int64) error {
	if retiredAt <= 0 || removeAfter < retiredAt {
		return fmt.Errorf("%w: a retiring signing key needs retired_at > 0 and remove_after >= retired_at", store.ErrInvalid)
	}
	return nil
}

func (t *tx) PutSigningKey(ctx context.Context, k store.SigningKey) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := validateSigningKey(k); err != nil {
		return err
	}
	var pem sql.NullString
	if k.Provider == store.SigningProviderStore {
		pem = sql.NullString{String: k.PrivateKeyPEM, Valid: true}
	}
	_, err := t.execW(ctx, "INSERT INTO signing_keys ("+signingKeyColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		k.KID, k.State, k.Provider, k.ProviderRef, k.Algorithm, k.PublicKey, pem, k.CreatedAt, k.RetiredAt, k.RemoveAfter)
	if isConflict(err) {
		return fmt.Errorf("%w: signing key %q exists or another key is active", store.ErrConflict, k.KID)
	}
	return err
}

func (t *tx) ListSigningKeys(ctx context.Context) ([]store.SigningKey, error) {
	var rows []signingKeyRow
	if err := t.sel(ctx, &rows, "SELECT "+signingKeyColumns+" FROM signing_keys"+
		" ORDER BY (CASE WHEN state = 'active' THEN 0 ELSE 1 END), retired_at DESC, kid"); err != nil {
		return nil, err
	}
	out := make([]store.SigningKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.SigningKey{
			KID: r.KID, State: r.State, Provider: r.Provider, ProviderRef: r.ProviderRef, Algorithm: r.Algorithm,
			PublicKey: r.PublicKey, PrivateKeyPEM: r.PrivateKeyPEM.String,
			CreatedAt: r.CreatedAt, RetiredAt: r.RetiredAt, RemoveAfter: r.RemoveAfter,
		})
	}
	return out, nil
}

func (t *tx) RetireSigningKey(ctx context.Context, kid string, retiredAt, removeAfter int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := checkRetirement(retiredAt, removeAfter); err != nil {
		return err
	}
	if badText(kid) {
		return fmt.Errorf("%w: active signing key %q", store.ErrNotFound, kid)
	}
	res, err := t.execW(ctx, "UPDATE signing_keys SET state = 'retiring', retired_at = ?, remove_after = ? WHERE kid = ? AND state = 'active'",
		retiredAt, removeAfter, kid)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%w: active signing key %q", store.ErrNotFound, kid)
	}
	return nil
}

func (t *tx) DeleteSigningKey(ctx context.Context, kid string) error {
	if err := t.writable(); err != nil {
		return err
	}
	if badText(kid) {
		return fmt.Errorf("%w: signing key %q", store.ErrNotFound, kid)
	}
	res, err := t.execW(ctx, "DELETE FROM signing_keys WHERE kid = ?", kid)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%w: signing key %q", store.ErrNotFound, kid)
	}
	return nil
}

func (t *tx) DeleteSigningKeysBefore(ctx context.Context, now int64) (int, error) {
	if err := t.writable(); err != nil {
		return 0, err
	}
	res, err := t.execW(ctx, "DELETE FROM signing_keys WHERE state = 'retiring' AND remove_after < ?", now)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
