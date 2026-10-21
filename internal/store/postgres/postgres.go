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
	if err := checkLocale(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
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

// checkLocale refuses a database whose text-search normalization would not
// match SQLite's (Decision (Eric, 2026-10-21), Milestone 2 Gate 0 item 6).
// tsNormalize relies on [[:alnum:]] covering non-ASCII letters, which needs a
// UTF-8 character classification.
//
// Provider choice: with the libc provider (the only one before PostgreSQL 15,
// and the default since) the regex classes follow pg_database.datctype, so
// datctype must name a UTF-8 locale. With an ICU or builtin provider
// (datlocprovider i or b) datctype is only the libc fallback and the regex
// classes follow the ICU or builtin locale, so datctype says nothing about
// them; there the database encoding must be UTF8 instead (a UTF8 database
// classifies by Unicode code point). datlocprovider is absent before
// PostgreSQL 15 and is read through to_jsonb so the query works on every
// version.
func checkLocale(ctx context.Context, db *sqlx.DB) error {
	var ctype, encoding, provider string
	err := db.QueryRowContext(ctx, `SELECT datctype, pg_encoding_to_char(encoding), coalesce(to_jsonb(d)->>'datlocprovider', 'c')
FROM pg_database d WHERE datname = current_database()`).Scan(&ctype, &encoding, &provider)
	if err != nil {
		return fmt.Errorf("postgres: read database locale: %w", err)
	}
	return classifyLocale(ctype, encoding, provider)
}

// classifyLocale is checkLocale's decision over the values it read.
func classifyLocale(ctype, encoding, provider string) error {
	if provider == "i" || provider == "b" {
		if strings.EqualFold(encoding, "UTF8") {
			return nil
		}
		return fmt.Errorf("%w: database encoding is %q with locale provider %q; a UTF8 encoding is required for text-search normalization",
			store.ErrSchemaInvalid, encoding, provider)
	}
	if utf8Ctype(ctype) {
		return nil
	}
	return fmt.Errorf("%w: database ctype is %q; a UTF-8 ctype is required for text-search normalization (create the database with LC_CTYPE 'en_US.UTF-8' or 'C.UTF-8')",
		store.ErrSchemaInvalid, ctype)
}

// utf8Ctype reports whether a libc ctype names a UTF-8 locale: en_US.utf8,
// en_US.UTF-8 and C.UTF-8 do; C, POSIX, en_US.ISO8859-1 and "" do not.
func utf8Ctype(ctype string) bool {
	c := strings.ToLower(ctype)
	return strings.Contains(c, "utf-8") || strings.Contains(c, "utf8")
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

// SQL fragments of the text-search translation (Decision 51). tsNormalize
// turns every run of non-alphanumeric characters into one space so the
// tokens match FTS5's unicode61 tokens; it relies on [[:alnum:]] covering
// non-ASCII letters, which holds for a UTF-8 database ctype (checkLocale
// refuses any other ctype at Open).
const (
	tsNormalize = `regexp_replace(?, '[^[:alnum:]]+', ' ', 'g')`
	tsWord      = `phraseto_tsquery('simple', ` + tsNormalize + `)`
	// tsPrefix puts the :* prefix marker on the last token only.
	tsPrefix = `to_tsquery('simple', regexp_replace(phraseto_tsquery('simple', ` + tsNormalize + `)::text, '''$', ''':*'))`
)

// TextSearch translates the query to an AND of per-term conditions over the
// per-column tsvectors, so a phrase never spans the subject/content boundary
// (as in FTS5). Each term's text is bound twice, once per column.
func (dialect) TextSearch(q search.Query) (string, []any, error) {
	if q.Empty() {
		return "", nil, fmt.Errorf("%w: search query has no searchable terms", store.ErrInvalid)
	}
	conds := make([]string, 0, len(q.Terms))
	args := make([]any, 0, 2*len(q.Terms))
	for _, t := range q.Terms {
		ts := tsWord
		if t.Prefix {
			ts = tsPrefix
		}
		conds = append(conds, "(m.fts_subject @@ "+ts+" OR m.fts_content @@ "+ts+")")
		args = append(args, t.Text, t.Text)
	}
	return strings.Join(conds, " AND "), args, nil
}

// NotifyAppend is the Milestone 7 hook.
func (dialect) NotifyAppend(context.Context, *sqlx.Tx, int64, int64) error { return nil }
