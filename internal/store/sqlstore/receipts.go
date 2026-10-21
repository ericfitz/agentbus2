package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

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

// Receipt returns an unexpired receipt only (expires_at > now).
func (t *tx) Receipt(ctx context.Context, identity, key string, now int64) (store.Receipt, bool, error) {
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
// be non-empty and Result valid UTF-8 (it is stored as TEXT; Postgres
// refuses other bytes).
func (t *tx) PutReceipt(ctx context.Context, r store.Receipt) error {
	if r.Identity == "" || r.Key == "" {
		return fmt.Errorf("%w: a receipt needs an identity and a key", store.ErrInvalid)
	}
	if !utf8.Valid(r.Result) {
		return fmt.Errorf("%w: receipt result is not valid UTF-8", store.ErrInvalid)
	}
	_, err := t.execW(ctx, `INSERT INTO receipts (identity, key, fingerprint, result, expires_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (identity, key) DO UPDATE SET fingerprint = excluded.fingerprint, result = excluded.result, expires_at = excluded.expires_at`,
		r.Identity, r.Key, r.Fingerprint, string(r.Result), r.ExpiresAt)
	return err
}

// DeleteExpiredReceipts selects up to limit expired keys, then deletes them
// (no LIMIT inside DELETE, portability rule) idChunk keys per statement: each
// key binds 2 variables and adds one OR term, and SQLite caps a statement at
// 32766 variables and an expression depth of 1000.
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
	for chunk := range slices.Chunk(keys, idChunk) {
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
