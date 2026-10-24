package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Scopes are new in v2 (Slack bridge spec 4); no v1 source.

type scopeRow struct {
	Name         string `db:"name"`
	State        string `db:"state"`
	RetireMode   string `db:"retire_mode"`
	CreatedAt    int64  `db:"created_at"`
	DoneAt       int64  `db:"done_at"`
	RetiredAt    int64  `db:"retired_at"`
	LastActivity int64  `db:"last_activity"`
}

const scopeCols = "name, state, retire_mode, created_at, done_at, retired_at, last_activity"

func (r scopeRow) scope() store.Scope {
	return store.Scope{Name: r.Name, State: store.ScopeState(r.State), Retire: store.RetireMode(r.RetireMode),
		CreatedAt: r.CreatedAt, DoneAt: r.DoneAt, RetiredAt: r.RetiredAt, LastActivity: r.LastActivity}
}

func notFoundScope(name string) error {
	return fmt.Errorf("%w: scope %q", store.ErrNotFound, name)
}

func (t *tx) CreateScope(ctx context.Context, name string, now int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := store.NameRule(name); err != nil {
		return fmt.Errorf("%w: scope name %q %s", store.ErrInvalid, name, err)
	}
	_, err := t.execW(ctx, "INSERT INTO scopes (name, state, created_at, last_activity) VALUES (?, 'open', ?, ?)", name, now, now)
	if isConflict(err) {
		return fmt.Errorf("%w: scope %q", store.ErrConflict, name)
	}
	return err
}

// Scope answers ErrNotFound for a name failing TextRule without a query.
func (t *tx) Scope(ctx context.Context, name string) (store.Scope, error) {
	if badText(name) {
		return store.Scope{}, notFoundScope(name)
	}
	var r scopeRow
	err := t.get(ctx, &r, "SELECT "+scopeCols+" FROM scopes WHERE name = ?", name)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Scope{}, notFoundScope(name)
	}
	if err != nil {
		return store.Scope{}, err
	}
	return r.scope(), nil
}

func (t *tx) ListScopes(ctx context.Context) ([]store.Scope, error) {
	var rows []scopeRow
	if err := t.sel(ctx, &rows, "SELECT "+scopeCols+" FROM scopes"); err != nil {
		return nil, err
	}
	sortByBytes(rows, func(r scopeRow) string { return r.Name })
	out := make([]store.Scope, len(rows))
	for i, r := range rows {
		out[i] = r.scope()
	}
	return out, nil
}

// MarkScopeDone is one conditional UPDATE; when it matches nothing a SELECT
// tells an unknown scope (ErrNotFound) from one that is not open (ErrConflict).
func (t *tx) MarkScopeDone(ctx context.Context, name string, mode store.RetireMode, now int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	if !store.ValidRetireMode(mode) {
		return fmt.Errorf("%w: retire mode %q (want with_bridge or normal)", store.ErrInvalid, mode)
	}
	if badText(name) {
		return notFoundScope(name)
	}
	res, err := t.execW(ctx, "UPDATE scopes SET state = 'done', retire_mode = ?, done_at = ? WHERE name = ? AND state = 'open'", string(mode), now, name)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	sc, err := t.Scope(ctx, name)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: scope %q is %s, not open", store.ErrConflict, name, sc.State)
}

// MarkScopeRetired is idempotent: a retired scope keeps its retired_at.
func (t *tx) MarkScopeRetired(ctx context.Context, name string, now int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	if badText(name) {
		return notFoundScope(name)
	}
	res, err := t.execW(ctx, "UPDATE scopes SET state = 'retired', retired_at = ? WHERE name = ? AND state <> 'retired'", now, name)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err = t.Scope(ctx, name) // ErrNotFound, or already retired
	return err
}

func (t *tx) DeleteRetiredScopes(ctx context.Context, retiredBefore int64) (int, error) {
	res, err := t.execW(ctx, `DELETE FROM scopes WHERE state = 'retired' AND retired_at < ?
	AND NOT EXISTS (SELECT 1 FROM channels c WHERE c.scope = scopes.name)
	AND NOT EXISTS (SELECT 1 FROM identities i WHERE i.scope = scopes.name)`, retiredBefore)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (t *tx) CreateScopedIdentity(ctx context.Context, name, scope string, now int64) error {
	if err := t.checkNewIdentityName(ctx, name); err != nil {
		return err
	}
	if badText(scope) {
		return notFoundScope(scope)
	}
	_, err := t.execW(ctx, "INSERT INTO identities (name, scope, is_admin, tags_acked_seq, created_at) VALUES (?, ?, FALSE, 0, ?)", name, scope, now)
	if isConflict(err) {
		return fmt.Errorf("%w: identity %q", store.ErrConflict, name)
	}
	if isForeignKey(err) {
		return notFoundScope(scope)
	}
	return err
}

func (t *tx) ScopeMembers(ctx context.Context, scope string) ([]store.Channel, []store.Identity, error) {
	if badText(scope) {
		return nil, nil, nil
	}
	var crows []channelRow
	if err := t.sel(ctx, &crows, "SELECT "+channelCols+" FROM channels WHERE scope = ?", scope); err != nil {
		return nil, nil, err
	}
	sortByBytes(crows, func(r channelRow) string { return r.Name })
	var irows []identityRow
	if err := t.sel(ctx, &irows, "SELECT "+identityCols+" FROM identities WHERE scope = ?", scope); err != nil {
		return nil, nil, err
	}
	sortByBytes(irows, func(r identityRow) string { return r.Name })
	ids := make([]store.Identity, len(irows))
	for i, r := range irows {
		ids[i] = r.identity()
	}
	return channelsOf(crows), ids, nil
}

// RetireIdentity runs in the caller's transaction, so the ACL rows, tokens,
// identity row and retired name change together. The identity's keys,
// subscriptions, tag subscriptions, sessions (with their cursors) and
// slack_users row go by ON DELETE CASCADE; messages stay. A second call finds
// no row and answers false, nil.
func (t *tx) RetireIdentity(ctx context.Context, name string, now int64) (bool, error) {
	if err := t.writable(); err != nil {
		return false, err
	}
	if badText(name) {
		return false, nil
	}
	var scope string
	err := t.get(ctx, &scope, "SELECT COALESCE(scope, '') FROM identities WHERE name = ?", name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := t.execW(ctx, "DELETE FROM channel_acl WHERE identity = ?", name); err != nil {
		return false, err
	}
	if _, err := t.DeleteEnrollmentTokensFor(ctx, name); err != nil {
		return false, err
	}
	if _, err := t.execW(ctx, "DELETE FROM identities WHERE name = ?", name); err != nil {
		return false, err
	}
	if _, err := t.execW(ctx, "INSERT INTO retired_identities (name, scope, retired_at) VALUES (?, ?, ?) ON CONFLICT (name) DO NOTHING", name, scope, now); err != nil {
		return false, err
	}
	return true, nil
}

func (t *tx) IdentityRetired(ctx context.Context, name string) (bool, error) {
	if badText(name) {
		return false, nil
	}
	var n int
	if err := t.get(ctx, &n, "SELECT count(*) FROM retired_identities WHERE name = ?", name); err != nil {
		return false, err
	}
	return n > 0, nil
}
