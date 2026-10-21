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

// Ported from v1 internal/bus/receipts.go (checkReceipt, storeReceipt),
// re-keyed to the identity (Decision 67). The result column is TEXT, as in
// v1: Result is the JSON the core stores.

type receiptRow struct {
	Identity    string `db:"identity"`
	Key         string `db:"key"`
	Fingerprint string `db:"fingerprint"`
	Result      string `db:"result"`
	ExpiresAt   int64  `db:"expires_at"`
}

// Receipt returns an unexpired receipt only (expires_at > now). An identity or
// key failing TextRule finds nothing, without a query: PutReceipt refuses
// such strings, and Postgres would refuse them raw (SQLSTATE 22021).
func (t *tx) Receipt(ctx context.Context, identity, key string, now int64) (store.Receipt, bool, error) {
	if store.TextRule(identity) != nil || store.TextRule(key) != nil {
		return store.Receipt{}, false, nil
	}
	var r receiptRow
	err := t.get(ctx, &r, `SELECT identity, key, fingerprint, result, expires_at FROM receipts WHERE identity = ? AND key = ? AND expires_at > ?`, identity, key, now)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Receipt{}, false, nil
	}
	if err != nil {
		return store.Receipt{}, false, err
	}
	return store.Receipt{Identity: r.Identity, Key: r.Key, Fingerprint: r.Fingerprint, Result: []byte(r.Result), ExpiresAt: r.ExpiresAt}, true, nil
}

// PutReceipt replaces any receipt for (identity, key). Identity and Key must
// be non-empty, and Identity, Key, Fingerprint and Result pass TextRule
// (valid UTF-8, no NUL: all four are TEXT columns).
func (t *tx) PutReceipt(ctx context.Context, r store.Receipt) error {
	if r.Identity == "" || r.Key == "" {
		return fmt.Errorf("%w: a receipt needs an identity and a key", store.ErrInvalid)
	}
	for _, f := range []struct{ name, value string }{
		{"identity", r.Identity}, {"key", r.Key}, {"fingerprint", r.Fingerprint}, {"result", string(r.Result)},
	} {
		if err := store.TextRule(f.value); err != nil {
			return fmt.Errorf("%w: receipt %s %s", store.ErrInvalid, f.name, err)
		}
	}
	_, err := t.execW(ctx, `INSERT INTO receipts (identity, key, fingerprint, result, expires_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (identity, key) DO UPDATE SET fingerprint = excluded.fingerprint, result = excluded.result, expires_at = excluded.expires_at`,
		r.Identity, r.Key, r.Fingerprint, string(r.Result), r.ExpiresAt)
	return err
}

// receiptDeleteChunk bounds the keys of one DELETE in DeleteExpiredReceipts.
// Each key binds 2 variables and adds one OR term, so the binding limit is
// SQLite's expression depth of 1000 (about 999 keys), well below its 32766
// variables. It is its own constant, not idChunk, so raising idChunk for IN
// lists cannot break the OR chain.
const receiptDeleteChunk = 500

// DeleteExpiredReceipts selects up to limit expired keys, then deletes them
// (no LIMIT inside DELETE, portability rule) receiptDeleteChunk keys per
// statement.
func (t *tx) DeleteExpiredReceipts(ctx context.Context, now int64, limit int) (int, error) {
	if err := t.writable(); err != nil {
		return 0, err
	}
	if err := checkLimit(limit); err != nil {
		return 0, err
	}
	var keys []struct {
		Identity string `db:"identity"`
		Key      string `db:"key"`
	}
	if err := t.sel(ctx, &keys, "SELECT identity, key FROM receipts WHERE expires_at <= ? ORDER BY expires_at, identity, key LIMIT ?", now, limit); err != nil {
		return 0, err
	}
	deleted := 0
	for chunk := range slices.Chunk(keys, receiptDeleteChunk) {
		conds := make([]string, len(chunk))
		args := make([]any, 0, 2*len(chunk))
		for i, k := range chunk {
			conds[i] = "(identity = ? AND key = ?)"
			args = append(args, k.Identity, k.Key)
		}
		res, err := t.execW(ctx, "DELETE FROM receipts WHERE "+strings.Join(conds, " OR "), args...)
		if err != nil {
			return deleted, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += int(n)
	}
	return deleted, nil
}
