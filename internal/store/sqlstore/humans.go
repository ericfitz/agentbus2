package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Human identities and their bindings are new in v2 (federated identity spec
// 6.2); no v1 source.

type humanBindingRow struct {
	Identity        string `db:"identity"`
	Issuer          string `db:"issuer"`
	ClaimType       string `db:"claim_type"`
	Value           string `db:"value"`
	ProviderSubject string `db:"provider_subject"`
	CreatedAt       int64  `db:"created_at"`
}

const humanBindingCols = "identity, issuer, claim_type, value, provider_subject, created_at"

func (r humanBindingRow) binding() store.HumanBinding {
	return store.HumanBinding{
		Identity: r.Identity, Issuer: r.Issuer, ClaimType: r.ClaimType, Value: r.Value,
		ProviderSubject: r.ProviderSubject, CreatedAt: r.CreatedAt,
	}
}

// checkSubject enforces that issuer and subject are both set or both empty,
// and that each passes TextRule.
func checkSubject(issuer, subject string) error {
	if (issuer == "") != (subject == "") {
		return fmt.Errorf("%w: provider issuer and subject must be set together", store.ErrInvalid)
	}
	return checkText("human binding", "issuer", issuer, "provider subject", subject)
}

// CreateHuman validates the binding, inserts the identity row (kind human)
// and then the binding; a unique violation on either is ErrConflict.
func (t *tx) CreateHuman(ctx context.Context, b store.HumanBinding, now int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := t.checkNewIdentityName(ctx, b.Identity); err != nil {
		return err
	}
	if b.ClaimType != store.ClaimEmail {
		return fmt.Errorf("%w: human binding claim type %q (want %s)", store.ErrInvalid, b.ClaimType, store.ClaimEmail)
	}
	if b.Value == "" {
		return fmt.Errorf("%w: human binding value must not be empty", store.ErrInvalid)
	}
	if err := checkText("human binding", "value", b.Value); err != nil {
		return err
	}
	if err := checkSubject(b.Issuer, b.ProviderSubject); err != nil {
		return err
	}
	_, err := t.execW(ctx, "INSERT INTO identities (name, kind, is_admin, tags_acked_seq, created_at) VALUES (?, ?, FALSE, 0, ?)",
		b.Identity, string(store.IdentityHuman), now)
	if isConflict(err) {
		return fmt.Errorf("%w: identity %q", store.ErrConflict, b.Identity)
	}
	if err != nil {
		return err
	}
	_, err = t.execW(ctx, "INSERT INTO human_bindings (identity, issuer, claim_type, value, provider_subject, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		b.Identity, b.Issuer, b.ClaimType, b.Value, b.ProviderSubject, now)
	if isConflict(err) {
		return fmt.Errorf("%w: %s %q or the provider subject is already bound", store.ErrConflict, b.ClaimType, b.Value)
	}
	return err
}

// MarkHuman turns an agent without keys into a human. A human is left alone.
func (t *tx) MarkHuman(ctx context.Context, name string) error {
	if err := t.guardIdentityName(name); err != nil {
		return err
	}
	var kind string
	err := t.getW(ctx, &kind, "SELECT kind FROM identities WHERE name = ?", name)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: identity %q", store.ErrNotFound, name)
	}
	if err != nil {
		return err
	}
	if store.IdentityKind(kind) == store.IdentityHuman {
		return nil
	}
	var keys int
	if err := t.getW(ctx, &keys, "SELECT count(*) FROM identity_keys WHERE identity = ?", name); err != nil {
		return err
	}
	if keys > 0 {
		return fmt.Errorf("%w: identity %q has keys and cannot become a human", store.ErrConflict, name)
	}
	_, err = t.execW(ctx, "UPDATE identities SET kind = ?, scope = NULL WHERE name = ?", string(store.IdentityHuman), name)
	return err
}

func (t *tx) humanBindingWhere(ctx context.Context, what, where string, args ...any) (store.HumanBinding, error) {
	var r humanBindingRow
	err := t.get(ctx, &r, "SELECT "+humanBindingCols+" FROM human_bindings WHERE "+where, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return store.HumanBinding{}, fmt.Errorf("%w: human binding %s", store.ErrNotFound, what)
	}
	if err != nil {
		return store.HumanBinding{}, err
	}
	return r.binding(), nil
}

func (t *tx) HumanBinding(ctx context.Context, identity string) (store.HumanBinding, error) {
	if badText(identity) {
		return store.HumanBinding{}, fmt.Errorf("%w: human binding for %q", store.ErrNotFound, identity)
	}
	return t.humanBindingWhere(ctx, "for "+fmt.Sprintf("%q", identity), "identity = ?", identity)
}

func (t *tx) HumanBindingByValue(ctx context.Context, claimType, value string) (store.HumanBinding, error) {
	if badText(claimType, value) {
		return store.HumanBinding{}, fmt.Errorf("%w: human binding %s %q", store.ErrNotFound, claimType, value)
	}
	return t.humanBindingWhere(ctx, fmt.Sprintf("%s %q", claimType, value), "claim_type = ? AND value = ?", claimType, value)
}

// HumanBindingBySubject never matches the empty pair: an unset subject is
// stored as ("", ""), which is not a provider subject.
func (t *tx) HumanBindingBySubject(ctx context.Context, issuer, subject string) (store.HumanBinding, error) {
	if issuer == "" || subject == "" || badText(issuer, subject) {
		return store.HumanBinding{}, fmt.Errorf("%w: human binding for subject %q of %q", store.ErrNotFound, subject, issuer)
	}
	return t.humanBindingWhere(ctx, fmt.Sprintf("for subject %q of %q", subject, issuer), "issuer = ? AND provider_subject = ?", issuer, subject)
}

// SetHumanSubject updates the binding's issuer and subject (both "" clears).
// The unique index on (issuer, provider_subject) refuses a pair bound to
// another identity.
func (t *tx) SetHumanSubject(ctx context.Context, identity, issuer, subject string) error {
	if err := t.guardIdentityName(identity); err != nil {
		return err
	}
	if err := checkSubject(issuer, subject); err != nil {
		return err
	}
	res, err := t.execW(ctx, "UPDATE human_bindings SET issuer = ?, provider_subject = ? WHERE identity = ?", issuer, subject, identity)
	if isConflict(err) {
		return fmt.Errorf("%w: provider subject %q of %q is bound to another identity", store.ErrConflict, subject, issuer)
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: human binding for %q", store.ErrNotFound, identity)
	}
	return nil
}
