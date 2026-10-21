package postgres

import (
	"context"
	"database/sql"

	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

const tryLockSQL = "SELECT pg_try_advisory_lock(hashtext(current_schema()), hashtext($1))"

var _ sqlstore.Locker = dialect{}

// TryLock takes the session advisory lock on conn (re-entrant on the same
// session, so a repeat by the holder re-asserts).
func (dialect) TryLock(ctx context.Context, conn *sql.Conn, name string) (bool, error) {
	var ok bool
	if err := conn.QueryRowContext(ctx, tryLockSQL, name).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

// Unlock releases every advisory lock of the session (one name per session).
func (dialect) Unlock(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock_all()")
	return err
}
