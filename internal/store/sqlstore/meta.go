package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// resetDataTables is the ResetData order: messages first (explicitly, so the
// SQLite messages_ad trigger fires and keeps messages_fts in step), then the
// parents whose cascades remove the rest. Foreign keys are on for both
// engines (sqlite.DSN sets foreign_keys(1)). Port of the table wipe in v1
// internal/bus/admin.go Reset, re-scoped to data (Milestone 5 item 1).
var resetDataTables = []string{
	"notices", "messages", "channels", "subscriptions", "tag_subscriptions",
	"sessions", "memory_access", "receipts",
}

// ResetData: see store.Meta. It deletes an explicit list, so the scopes,
// retired_identities, bridges, bridge_messages, bridge_dropped and slack_users
// tables are kept (a bridge whose channel went is cleaned by the runner).
func (t *tx) ResetData(ctx context.Context) error {
	if err := t.writable(); err != nil {
		return err
	}
	for _, table := range resetDataTables {
		if _, err := t.execW(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	return nil
}

// ResetRegistry: see store.Meta. The reserved slack-bridge identity stays (its
// scope-free, key-free row is part of the schema).
func (t *tx) ResetRegistry(ctx context.Context, keep string) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := store.TextRule(keep); err != nil {
		return fmt.Errorf("%w: keep identity %q %s", store.ErrInvalid, keep, err)
	}
	if _, err := t.execW(ctx, "DELETE FROM identities WHERE name <> ? AND name <> ?", keep, store.SlackBridge); err != nil {
		return err
	}
	if _, err := t.execW(ctx, "DELETE FROM enrollment_tokens"); err != nil {
		return err
	}
	// human_bindings went with their identities (ON DELETE CASCADE).
	if _, err := t.execW(ctx, "DELETE FROM access_refs WHERE ref_list = ?", string(store.ListUsers)); err != nil {
		return err
	}
	_, err := t.execW(ctx, "DELETE FROM access_refs WHERE ref_list = ? AND NOT (ref_kind = ? AND issuer = '' AND value = ?)",
		string(store.ListAdmins), string(store.RefIdentity), keep)
	return err
}

// NextSeq allocates the next message sequence number (Decision 27). The
// read-only guard precedes the append check.
func (t *tx) NextSeq(ctx context.Context) (int64, error) {
	if err := t.writable(); err != nil {
		return 0, err
	}
	if !t.append {
		return 0, store.ErrNotAppend
	}
	var v int64
	if err := t.getW(ctx, &v, "UPDATE seq_counter SET v = v + 1 WHERE id = 1 RETURNING v"); err != nil {
		return 0, err
	}
	return v, nil
}

func (t *tx) MaxSeq(ctx context.Context) (int64, error) {
	var v int64
	if err := t.get(ctx, &v, "SELECT v FROM seq_counter WHERE id = 1"); err != nil {
		return 0, err
	}
	return v, nil
}

// TryLease is v1 takeLease (internal/bus/maintenance.go) with the lease name,
// owner and times given by the caller; ErrInvalid on a name or owner failing
// TextRule.
func (t *tx) TryLease(ctx context.Context, name, owner string, now, until int64) (bool, error) {
	if err := t.writable(); err != nil {
		return false, err
	}
	if err := checkText("lease", "name", name, "owner", owner); err != nil {
		return false, err
	}
	r, err := t.execW(ctx,
		"UPDATE leases SET owner = ?, expires_at = ? WHERE name = ? AND (expires_at < ? OR owner = ?)",
		owner, until, name, now, owner)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// SetNotice: ErrInvalid on a kind or message failing TextRule.
func (t *tx) SetNotice(ctx context.Context, n store.Notice) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := checkText("notice", "kind", n.Kind, "message", n.Message); err != nil {
		return err
	}
	_, err := t.execW(ctx,
		"INSERT INTO notices (kind, message, set_at) VALUES (?, ?, ?) ON CONFLICT (kind) DO UPDATE SET message = excluded.message, set_at = excluded.set_at",
		n.Kind, n.Message, n.SetAt)
	return err
}

// ClearNotice and Notice treat a kind failing TextRule as absent.
func (t *tx) ClearNotice(ctx context.Context, kind string) error {
	if err := t.writable(); err != nil {
		return err
	}
	if badText(kind) {
		return nil
	}
	_, err := t.execW(ctx, "DELETE FROM notices WHERE kind = ?", kind)
	return err
}

func (t *tx) Notice(ctx context.Context, kind string) (store.Notice, bool, error) {
	if badText(kind) {
		return store.Notice{}, false, nil
	}
	var n store.Notice
	err := t.get(ctx, &n, "SELECT kind AS kind, message AS message, set_at AS setat FROM notices WHERE kind = ?", kind)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Notice{}, false, nil
	}
	if err != nil {
		return store.Notice{}, false, err
	}
	return n, true, nil
}
