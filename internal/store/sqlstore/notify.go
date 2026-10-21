package sqlstore

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// ControlNotifier is an optional Dialect extension: RegisterWatch calls it
// after its UPDATE, inside the same transaction, so a replica holding an
// armed watch hears about the new registration (Decision 68 across replicas).
// Postgres implements it with a NOTIFY; SQLite does not.
type ControlNotifier interface {
	NotifyControl(ctx context.Context, tx *sqlx.Tx) error
}
