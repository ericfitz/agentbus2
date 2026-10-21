package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
	sqlitedrv "modernc.org/sqlite"

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

// constraintErr classifies a constraint violation from either engine: SQLite
// by extended result code (falling back on the primary code 19 with the
// message text), Postgres by SQLSTATE.
func constraintErr(err error, sqliteExt []int, sqliteText, pgState string) bool {
	var se *sqlitedrv.Error
	if errors.As(err, &se) {
		if slices.Contains(sqliteExt, se.Code()) {
			return true
		}
		return se.Code()&0xff == 19 && strings.Contains(se.Error(), sqliteText)
	}
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == pgState
}

// isConflict reports a unique or primary-key violation
// (SQLITE_CONSTRAINT_PRIMARYKEY 1555 / _UNIQUE 2067; Postgres 23505).
func isConflict(err error) bool {
	return constraintErr(err, []int{1555, 2067}, "UNIQUE constraint failed", "23505")
}

// isForeignKey reports a foreign-key violation (SQLITE_CONSTRAINT_FOREIGNKEY
// 787; Postgres 23503).
func isForeignKey(err error) bool {
	return constraintErr(err, []int{787}, "FOREIGN KEY constraint failed", "23503")
}
