package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Identities and keys are new in v2 (Decisions 14, 55, 57, 73); no v1 source.

type identityRow struct {
	Name         string `db:"name"`
	Kind         string `db:"kind"`
	Scope        string `db:"scope"`
	Admin        bool   `db:"is_admin"`
	TagsAckedSeq int64  `db:"tags_acked_seq"`
	CreatedAt    int64  `db:"created_at"`
}

// guardIdentityName runs the read-only guard, then maps a name failing
// TextRule to ErrNotFound for the writes that need the identity to exist: no
// such name can be stored, and Postgres would refuse it raw (SQLSTATE 22021).
func (t *tx) guardIdentityName(name string) error {
	if err := t.writable(); err != nil {
		return err
	}
	if store.TextRule(name) != nil {
		return fmt.Errorf("%w: identity %q", store.ErrNotFound, name)
	}
	return nil
}

func (r identityRow) identity() store.Identity {
	return store.Identity{Name: r.Name, Kind: store.IdentityKind(r.Kind), Scope: r.Scope, Admin: r.Admin, TagsAckedSeq: r.TagsAckedSeq, CreatedAt: r.CreatedAt}
}

const identityCols = "name, kind, COALESCE(scope, '') AS scope, is_admin, tags_acked_seq, created_at"

// identityUpdated maps an UPDATE on identities to ErrNotFound when no row
// matched.
func identityUpdated(res sql.Result, err error, name string) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: identity %q", store.ErrNotFound, name)
	}
	return nil
}

// checkIdentityName is the static part of the name rule shared by
// CreateIdentity, CreateHuman and CreateScopedIdentity: NameRule, then the
// reserved names.
func checkIdentityName(name string) error {
	if err := store.NameRule(name); err != nil {
		return fmt.Errorf("%w: identity name %q %s", store.ErrInvalid, name, err)
	}
	if store.ReservedIdentity(name) {
		return fmt.Errorf("%w: identity name %q is reserved", store.ErrInvalid, name)
	}
	return nil
}

// checkNewIdentityName is checkIdentityName plus the retired names: a
// retired name is never reused (ErrConflict).
func (t *tx) checkNewIdentityName(ctx context.Context, name string) error {
	if err := checkIdentityName(name); err != nil {
		return err
	}
	retired, err := t.IdentityRetired(ctx, name)
	if err != nil {
		return err
	}
	if retired {
		return fmt.Errorf("%w: identity name %q is retired", store.ErrConflict, name)
	}
	return nil
}

// CreateIdentity validates the name (NameRule, then the reserved names;
// Decision (Eric, 2026-10-21): Gate 0 item 1) before the INSERT.
func (t *tx) CreateIdentity(ctx context.Context, name string, now int64) error {
	if err := t.checkNewIdentityName(ctx, name); err != nil {
		return err
	}
	_, err := t.execW(ctx, "INSERT INTO identities (name, is_admin, tags_acked_seq, created_at) VALUES (?, FALSE, 0, ?)", name, now)
	if isConflict(err) {
		return fmt.Errorf("%w: identity %q", store.ErrConflict, name)
	}
	return err
}

// Identity answers ErrNotFound for a name failing TextRule without a query:
// no such name can be stored, and Postgres would refuse it raw.
func (t *tx) Identity(ctx context.Context, name string) (store.Identity, error) {
	if store.TextRule(name) != nil {
		return store.Identity{}, fmt.Errorf("%w: identity %q", store.ErrNotFound, name)
	}
	var r identityRow
	err := t.get(ctx, &r, "SELECT "+identityCols+" FROM identities WHERE name = ?", name)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Identity{}, fmt.Errorf("%w: identity %q", store.ErrNotFound, name)
	}
	if err != nil {
		return store.Identity{}, err
	}
	return r.identity(), nil
}

func (t *tx) ListIdentities(ctx context.Context) ([]store.Identity, error) {
	var rows []identityRow
	if err := t.sel(ctx, &rows, "SELECT "+identityCols+" FROM identities"); err != nil {
		return nil, err
	}
	sortByBytes(rows, func(r identityRow) string { return r.Name })
	out := make([]store.Identity, len(rows))
	for i, r := range rows {
		out[i] = r.identity()
	}
	return out, nil
}

// SetAdmin, like every write by identity name, answers ErrNotFound for a name
// failing TextRule (after the read-only guard).
func (t *tx) SetAdmin(ctx context.Context, name string, admin bool) error {
	if err := t.guardIdentityName(name); err != nil {
		return err
	}
	res, err := t.execW(ctx, "UPDATE identities SET is_admin = ? WHERE name = ?", admin, name)
	return identityUpdated(res, err, name)
}

// AdvanceTagAckedMark sets the mark to max(current, seq) with a CASE (no
// scalar max, portability rule).
func (t *tx) AdvanceTagAckedMark(ctx context.Context, name string, seq int64) error {
	if err := t.guardIdentityName(name); err != nil {
		return err
	}
	res, err := t.execW(ctx, "UPDATE identities SET tags_acked_seq = CASE WHEN tags_acked_seq < ? THEN ? ELSE tags_acked_seq END WHERE name = ?", seq, seq, name)
	return identityUpdated(res, err, name)
}

// PutPublicKey validates the key before the upsert: Algorithm (Decision 55:
// EdDSA or ES256), a non-empty KID passing TextRule and a non-empty Key (PKIX
// DER); a human identity is ErrConflict (the rule MarkHuman applies from the
// other side); an unknown identity, and one failing TextRule (no such
// identity can exist), is ErrNotFound.
func (t *tx) PutPublicKey(ctx context.Context, k store.PublicKey) error {
	if k.Algorithm != "EdDSA" && k.Algorithm != "ES256" {
		return fmt.Errorf("%w: unknown public key algorithm %q (want EdDSA or ES256)", store.ErrInvalid, k.Algorithm)
	}
	if k.KID == "" {
		return fmt.Errorf("%w: public key kid must not be empty", store.ErrInvalid)
	}
	if err := store.TextRule(k.KID); err != nil {
		return fmt.Errorf("%w: public key kid %q %s", store.ErrInvalid, k.KID, err)
	}
	if len(k.Key) == 0 {
		return fmt.Errorf("%w: public key %q has no key bytes (want PKIX DER)", store.ErrInvalid, k.KID)
	}
	if err := t.guardIdentityName(k.Identity); err != nil {
		return err
	}
	if k.Identity == store.SlackBridge {
		return fmt.Errorf("%w: identity %q is reserved and takes no key", store.ErrInvalid, k.Identity)
	}
	var kind string
	switch err := t.get(ctx, &kind, "SELECT kind FROM identities WHERE name = ?", k.Identity); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: identity %q", store.ErrNotFound, k.Identity)
	case err != nil:
		return err
	case store.IdentityKind(kind) == store.IdentityHuman:
		return fmt.Errorf("%w: identity %q is a human and takes no key", store.ErrConflict, k.Identity)
	}
	_, err := t.execW(ctx, `INSERT INTO identity_keys (identity, kid, algorithm, public_key, created_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (identity, kid) DO UPDATE SET algorithm = excluded.algorithm, public_key = excluded.public_key, created_at = excluded.created_at`,
		k.Identity, k.KID, k.Algorithm, k.Key, k.CreatedAt)
	if isForeignKey(err) {
		return fmt.Errorf("%w: identity %q", store.ErrNotFound, k.Identity)
	}
	return err
}

type identityKeyRow struct {
	Identity  string `db:"identity"`
	KID       string `db:"kid"`
	Algorithm string `db:"algorithm"`
	Key       []byte `db:"public_key"`
	CreatedAt int64  `db:"created_at"`
}

// PublicKeys lists none, without a query, for a name failing TextRule.
func (t *tx) PublicKeys(ctx context.Context, name string) ([]store.PublicKey, error) {
	if store.TextRule(name) != nil {
		return []store.PublicKey{}, nil
	}
	var rows []identityKeyRow
	if err := t.sel(ctx, &rows, "SELECT identity, kid, algorithm, public_key, created_at FROM identity_keys WHERE identity = ?", name); err != nil {
		return nil, err
	}
	sortByBytes(rows, func(r identityKeyRow) string { return r.KID })
	out := make([]store.PublicKey, len(rows))
	for i, r := range rows {
		out[i] = store.PublicKey{Identity: r.Identity, KID: r.KID, Algorithm: r.Algorithm, Key: r.Key, CreatedAt: r.CreatedAt}
	}
	return out, nil
}

// DeletePublicKeys removes none for a name failing TextRule (after the
// read-only guard).
func (t *tx) DeletePublicKeys(ctx context.Context, name string) (int, error) {
	if err := t.writable(); err != nil {
		return 0, err
	}
	if store.TextRule(name) != nil {
		return 0, nil
	}
	res, err := t.execW(ctx, "DELETE FROM identity_keys WHERE identity = ?", name)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
