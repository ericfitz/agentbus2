// Package dbtest hands both database engines to the test suites. SQLite lives
// in a temp file; Postgres comes from AGENTBUS2_TEST_POSTGRES_DSN or, when it
// is unset or empty, from a testcontainers container started once per test
// binary. Nothing here skips: an unreachable engine is a test failure.
package dbtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
	_ "modernc.org/sqlite"             // registers the "sqlite" driver
)

const (
	PostgresDSNEnv = "AGENTBUS2_TEST_POSTGRES_DSN"
	PostgresImage  = "postgres:17"
	pingTimeout    = 10 * time.Second
)

// Engine is one database engine the suites run against.
type Engine struct {
	Name   string // "sqlite" or "postgres"; also the subtest name
	Driver string // database/sql driver name: "sqlite" or "pgx"
}

// Engines returns the only valid engines, in run order.
func Engines() []Engine {
	return []Engine{{"sqlite", "sqlite"}, {"postgres", "pgx"}}
}

func (e Engine) validate() error {
	for _, k := range Engines() {
		if e == k {
			return nil
		}
	}
	return fmt.Errorf("dbtest: unknown engine name %q with driver %q (want sqlite/sqlite or postgres/pgx)", e.Name, e.Driver)
}

// DSN returns the data source name for the engine. It fails the test on error
// and never skips. Postgres resolves with context.Background: the first result
// is stored for the whole binary, so a per-test context must not bound it.
func (e Engine) DSN(t testing.TB) string {
	t.Helper()
	dsn, _, err := e.dsn(t)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return dsn
}

func (e Engine) dsn(t testing.TB) (dsn, from string, err error) {
	if err := e.validate(); err != nil {
		return "", "", err
	}
	if e.Name == "sqlite" {
		return filepath.Join(t.TempDir(), "agentbus2.db"), "temp file", nil
	}
	return defaultPostgres.resolve(context.Background())
}

// Open returns an open, pinged handle that is closed at test cleanup.
func (e Engine) Open(t testing.TB) *sql.DB {
	t.Helper()
	dsn, from, err := e.dsn(t)
	if err != nil {
		t.Fatalf("%v", err)
	}
	db, err := openDB(context.Background(), e, dsn, from)
	if err != nil {
		t.Fatalf("%v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ForEachEngine runs fn as a subtest named for each engine, in order.
func ForEachEngine(t *testing.T, fn func(t *testing.T, e Engine)) {
	t.Helper()
	for _, e := range Engines() {
		t.Run(e.Name, func(t *testing.T) { fn(t, e) })
	}
}

// Run is for TestMain: it runs the tests, then terminates the Postgres
// container if one was started.
func Run(m *testing.M) int {
	code := m.Run()
	if defaultPostgres.stop != nil {
		defaultPostgres.stop()
	}
	return code
}

// openDB validates the engine, parses a pgx DSN (sql.Open does not), opens and
// pings. No error text includes the DSN.
func openDB(ctx context.Context, e Engine, dsn, from string) (*sql.DB, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	if e.Driver == "pgx" {
		if _, err := pgx.ParseConfig(dsn); err != nil {
			cause := "invalid connection string"
			var pce *pgconn.ParseConfigError
			if errors.As(err, &pce) {
				if inner := pce.Unwrap(); inner != nil {
					cause = inner.Error()
				}
			}
			return nil, fmt.Errorf("postgres from %s could not be opened (check %s): %s", from, PostgresDSNEnv, cause)
		}
	}
	db, err := sql.Open(e.Driver, dsn)
	if err != nil {
		if e.Name == "sqlite" {
			return nil, fmt.Errorf("sqlite: %w", err)
		}
		return nil, fmt.Errorf("postgres from %s could not be opened (check %s): %w", from, PostgresDSNEnv, err)
	}
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		_ = db.Close()
		if e.Name == "sqlite" {
			return nil, fmt.Errorf("sqlite: %w", err)
		}
		return nil, fmt.Errorf("postgres from %s is unreachable (set %s to a reachable server, or unset it to use testcontainers): %w", from, PostgresDSNEnv, err)
	}
	return db, nil
}
