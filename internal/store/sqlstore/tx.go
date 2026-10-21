package sqlstore

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
)

// tx implements store.Tx over one database transaction.
type tx struct {
	tx       *sqlx.Tx
	d        Dialect
	append   bool // created by Store.Append
	readOnly bool // created by Store.Read
}

var _ store.Tx = (*tx)(nil)

// writable is the read-only guard; every write entry point calls it first
// (Review Focus 10).
func (t *tx) writable() error {
	if t.readOnly {
		return store.ErrReadOnly
	}
	return nil
}

func (t *tx) rebind(q string) string { return sqlx.Rebind(t.d.BindType(), q) }

// get and sel are for SELECT only. A mutating statement, even with
// RETURNING, goes through getW, selW or execW.
func (t *tx) get(ctx context.Context, dest any, q string, args ...any) error {
	return t.tx.GetContext(ctx, dest, t.rebind(q), args...)
}

//nolint:unused // used by the aggregate tasks (4 to 11)
func (t *tx) sel(ctx context.Context, dest any, q string, args ...any) error {
	return t.tx.SelectContext(ctx, dest, t.rebind(q), args...)
}

func (t *tx) execW(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if err := t.writable(); err != nil {
		return nil, err
	}
	return t.tx.ExecContext(ctx, t.rebind(q), args...)
}

func (t *tx) getW(ctx context.Context, dest any, q string, args ...any) error {
	if err := t.writable(); err != nil {
		return err
	}
	return t.tx.GetContext(ctx, dest, t.rebind(q), args...)
}

//nolint:unused // used by the aggregate tasks (4 to 11)
func (t *tx) selW(ctx context.Context, dest any, q string, args ...any) error {
	if err := t.writable(); err != nil {
		return err
	}
	return t.tx.SelectContext(ctx, dest, t.rebind(q), args...)
}
