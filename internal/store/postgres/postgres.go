// Package postgres is the Postgres engine of the store: Open, the sqlstore
// dialect and the embedded migration set.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/search"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// TxAttempts is the Decision 69 retry count for serialization failures
// (choice 5). pending Eric: Gate 0 item 3.
const TxAttempts = 5

const (
	backoffBase = 10 * time.Millisecond
	backoffCap  = 200 * time.Millisecond
)

// Options configures Open.
type Options struct {
	Store sqlstore.Options
}

// Open opens the database named by dsn under the Decision 34 rules, creating
// and migrating it when empty. Error text never includes the DSN.
func Open(ctx context.Context, dsn string, opts Options) (store.Store, error) {
	if _, err := pgx.ParseConfig(dsn); err != nil {
		cause := "invalid connection string"
		var pce *pgconn.ParseConfigError
		if errors.As(err, &pce) {
			if inner := pce.Unwrap(); inner != nil {
				cause = inner.Error()
			}
		}
		return nil, fmt.Errorf("postgres: %s", cause)
	}
	db, err := sqlx.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: unreachable: %w", err)
	}
	so := opts.Store
	if so.Migrations == nil {
		so.Migrations = Migrations()
	}
	if so.TxAttempts <= 0 {
		so.TxAttempts = TxAttempts
	}
	s, err := sqlstore.Open(ctx, db, dialect{}, so)
	if err != nil {
		return nil, err
	}
	return s, nil
}

var schemaName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// WithSearchPath returns dsn with search_path=<schema> added as a runtime
// parameter (pgx treats unknown DSN keys as runtime parameters). It is for
// tests and tools that isolate a database inside one schema. The schema must
// match ^[a-z][a-z0-9_]{0,62}$ (ErrInvalid otherwise). Both the URL and the
// keyword/value DSN forms are accepted.
func WithSearchPath(dsn, schema string) (string, error) {
	if !schemaName.MatchString(schema) {
		return "", fmt.Errorf("%w: schema name must match %s", store.ErrInvalid, schemaName)
	}
	if !strings.Contains(dsn, "://") {
		return dsn + " search_path=" + schema, nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", errors.New("postgres: invalid DSN")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errors.New("postgres: invalid DSN query")
	}
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// dialect is the Postgres sqlstore.Dialect.
type dialect struct{}

var (
	_ sqlstore.Dialect   = dialect{}
	_ sqlstore.Backoffer = dialect{}
)

func (dialect) Name() store.Engine { return store.EnginePostgres }
func (dialect) BindType() int      { return sqlx.DOLLAR }

func pgCode(err error) string {
	var e *pgconn.PgError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// IsRetryable: SQLSTATE 40001 (serialization_failure, Decision 69) and 40P01
// (deadlock_detected; pending Eric: Gate 0 item 2).
func (dialect) IsRetryable(err error) bool {
	switch pgCode(err) {
	case "40001", "40P01":
		return true
	}
	return false
}

// IsReadOnlyViolation: SQLSTATE 25006, a write reached the READ ONLY
// transaction of Store.Read.
func (dialect) IsReadOnlyViolation(err error) bool { return pgCode(err) == "25006" }

// Backoff is the jittered wait before retry attempt+1 (choice 5): uniform in
// [0, 10ms * 2^(attempt-1)), capped at 200 ms.
func (dialect) Backoff(attempt int) time.Duration {
	limit := backoffCap
	if attempt < 1 {
		attempt = 1
	}
	if attempt <= 6 { // 10ms << 5 = 320ms already exceeds the cap
		limit = min(backoffBase<<(attempt-1), backoffCap)
	}
	return time.Duration(rand.Int64N(int64(limit)))
}

func (dialect) IsEmpty(ctx context.Context, db *sqlx.DB) (bool, error) {
	var n int
	if err := db.GetContext(ctx, &n, "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()"); err != nil {
		return false, err
	}
	return n == 0, nil
}

func (dialect) TableExists(ctx context.Context, db *sqlx.DB, name string) (bool, error) {
	var n int
	if err := db.GetContext(ctx, &n, "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1", name); err != nil {
		return false, err
	}
	return n == 1, nil
}

// Stamp is a no-op: Postgres has no application_id.
func (dialect) Stamp(context.Context, *sqlx.DB) error { return nil }

// CheckStamp is a no-op: Postgres has no application_id.
func (dialect) CheckStamp(context.Context, *sqlx.DB) error { return nil }

// LockMigrations takes the per-schema advisory lock (Decision 68). The
// framework calls it inside a READ COMMITTED transaction only (Review Focus 4).
func (dialect) LockMigrations(ctx context.Context, tx *sqlx.Tx) error {
	_, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext(current_schema()))")
	return err
}

// LockAppend makes concurrent appends queue behind the counter instead of
// aborting with 40001 (choice 4).
func (dialect) LockAppend(ctx context.Context, tx *sqlx.Tx) error {
	_, err := tx.ExecContext(ctx, "LOCK TABLE seq_counter IN EXCLUSIVE MODE")
	return err
}

// TextSearch arrives in Task 10.
func (dialect) TextSearch(search.Query) (string, []any, error) {
	return "", nil, sqlstore.ErrNotImplemented
}

// NotifyAppend is the Milestone 7 hook.
func (dialect) NotifyAppend(context.Context, *sqlx.Tx, int64, int64) error { return nil }
