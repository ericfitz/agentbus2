package sqlstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ericfitz/agentbus2/internal/store"
)

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
// owner and times given by the caller.
func (t *tx) TryLease(ctx context.Context, name, owner string, now, until int64) (bool, error) {
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

func (t *tx) SetNotice(ctx context.Context, n store.Notice) error {
	_, err := t.execW(ctx,
		"INSERT INTO notices (kind, message, set_at) VALUES (?, ?, ?) ON CONFLICT (kind) DO UPDATE SET message = excluded.message, set_at = excluded.set_at",
		n.Kind, n.Message, n.SetAt)
	return err
}

func (t *tx) ClearNotice(ctx context.Context, kind string) error {
	_, err := t.execW(ctx, "DELETE FROM notices WHERE kind = ?", kind)
	return err
}

func (t *tx) Notice(ctx context.Context, kind string) (store.Notice, bool, error) {
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
