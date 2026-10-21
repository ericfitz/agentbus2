package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

const fixedNow = 4242

func newPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "agentbus2.db")
}

func openAt(t *testing.T, path string, opts Options) (store.Store, error) {
	t.Helper()
	if opts.Store.Now == nil {
		opts.Store.Now = func() int64 { return fixedNow }
	}
	return Open(context.Background(), path, opts)
}

func mustOpen(t *testing.T, path string, opts Options) *sqlstore.Store {
	t.Helper()
	s, err := openAt(t, path, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*sqlstore.Store)
}

// rawDB opens path with plain database/sql, bypassing Open.
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn, err := DSN(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func scalar[T any](t *testing.T, db *sql.DB, q string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func execAll(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func step(version int, sqlText string, fail bool) sqlstore.Migration {
	return sqlstore.Migration{Version: version, Up: func(ctx context.Context, tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlText); err != nil {
			return err
		}
		if fail {
			return errors.New("step failed on purpose")
		}
		return nil
	}}
}

func withSteps(extra ...sqlstore.Migration) Options {
	return Options{Store: sqlstore.Options{Migrations: append(Migrations(), extra...)}}
}

func schemaVersion(t *testing.T, s *sqlstore.Store) int {
	t.Helper()
	v, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpenEmptyCreatesSchema(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path, Options{})
	if got := schemaVersion(t, s); got != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", got)
	}
	if s.Engine() != store.EngineSQLite {
		t.Fatalf("Engine = %q", s.Engine())
	}
	db := rawDB(t, path)
	var app, engine string
	var created int64
	if err := db.QueryRow("SELECT app, engine, created_at FROM schema_meta").Scan(&app, &engine, &created); err != nil {
		t.Fatal(err)
	}
	if app != "agentbus2" || engine != "sqlite" || created != fixedNow {
		t.Fatalf("schema_meta = %q %q %d", app, engine, created)
	}
	if got := scalar[int32](t, db, "PRAGMA application_id"); got != ApplicationID {
		t.Fatalf("application_id = %#x, want %#x", got, ApplicationID)
	}
	if got := scalar[int](t, db, "PRAGMA user_version"); got != 0 {
		t.Fatalf("user_version = %d, want 0", got)
	}
	if got := scalar[int](t, db, "SELECT v FROM seq_counter"); got != 0 {
		t.Fatalf("seq_counter.v = %d", got)
	}
	if got := scalar[int](t, db, "SELECT count(*) FROM leases WHERE name = 'maintenance'"); got != 1 {
		t.Fatalf("maintenance lease rows = %d", got)
	}

	// Second run: nothing changes, created_at stays.
	_ = s.Close()
	s2, err := openAt(t, path, Options{Store: sqlstore.Options{Now: func() int64 { return 9999 }}})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	_ = s2.Close()
	if got := scalar[int64](t, db, "SELECT created_at FROM schema_meta"); got != fixedNow {
		t.Fatalf("created_at after reopen = %d, want %d", got, fixedNow)
	}
}

func TestOpenRefusesNonEmptyWithoutSchemaMeta(t *testing.T) {
	path := newPath(t)
	db := rawDB(t, path)
	execAll(t, db, "CREATE TABLE channels (name TEXT PRIMARY KEY, kind TEXT)", "PRAGMA user_version = 12")
	before := scalar[int](t, db, "SELECT count(*) FROM sqlite_master")

	_, err := openAt(t, path, Options{})
	if !errors.Is(err, store.ErrNotAgentbus2) {
		t.Fatalf("err = %v, want ErrNotAgentbus2", err)
	}
	if got := scalar[int](t, db, "SELECT count(*) FROM sqlite_master"); got != before {
		t.Fatalf("sqlite_master count %d -> %d", before, got)
	}
	if got := scalar[int](t, db, "PRAGMA user_version"); got != 12 {
		t.Fatalf("user_version = %d, want 12", got)
	}
	if got := scalar[int32](t, db, "PRAGMA application_id"); got != 0 {
		t.Fatalf("application_id = %#x, want 0 (file untouched)", got)
	}
}

func TestOpenRefusesWrongApp(t *testing.T) {
	path := newPath(t)
	_ = mustOpen(t, path, Options{}).Close()
	db := rawDB(t, path)

	execAll(t, db, "UPDATE schema_meta SET app = 'other'")
	if _, err := openAt(t, path, Options{}); !errors.Is(err, store.ErrNotAgentbus2) {
		t.Fatalf("wrong app: err = %v, want ErrNotAgentbus2", err)
	}

	execAll(t, db, "UPDATE schema_meta SET app = 'agentbus2'", "PRAGMA application_id = 0")
	if _, err := openAt(t, path, Options{}); !errors.Is(err, store.ErrNotAgentbus2) {
		t.Fatalf("application_id 0: err = %v, want ErrNotAgentbus2", err)
	}
}

func TestOpenRefusesNewerVersion(t *testing.T) {
	path := newPath(t)
	_ = mustOpen(t, path, Options{}).Close()
	execAll(t, rawDB(t, path), "UPDATE schema_meta SET schema_version = 2")
	_, err := openAt(t, path, Options{})
	if !errors.Is(err, store.ErrUpgradeNeeded) {
		t.Fatalf("err = %v, want ErrUpgradeNeeded", err)
	}
	if !strings.Contains(err.Error(), "upgrade agentbus2") {
		t.Fatalf("err %q lacks 'upgrade agentbus2'", err)
	}
}

func TestOpenMigratesOlderVersion(t *testing.T) {
	path := newPath(t)
	_ = mustOpen(t, path, Options{}).Close()

	opts := withSteps(step(2, "CREATE TABLE probe (x INTEGER)", false))
	s := mustOpen(t, path, opts)
	if got := schemaVersion(t, s); got != 2 {
		t.Fatalf("SchemaVersion = %d, want 2", got)
	}
	db := rawDB(t, path)
	if got := scalar[int](t, db, "SELECT count(*) FROM sqlite_master WHERE name = 'probe'"); got != 1 {
		t.Fatalf("probe tables = %d", got)
	}
	_ = s.Close()

	// Second run with the same set changes nothing (a re-run of step 2 would fail: probe exists).
	s3 := mustOpen(t, path, opts)
	if got := schemaVersion(t, s3); got != 2 {
		t.Fatalf("third open SchemaVersion = %d, want 2", got)
	}
}

func TestOpenFailedStepLeavesPreviousVersion(t *testing.T) {
	path := newPath(t)
	_ = mustOpen(t, path, Options{}).Close()
	db := rawDB(t, path)

	if _, err := openAt(t, path, withSteps(step(2, "CREATE TABLE probe (x INTEGER)", true))); err == nil {
		t.Fatal("Open with a failing step succeeded")
	}
	if got := scalar[int](t, db, "SELECT schema_version FROM schema_meta"); got != 1 {
		t.Fatalf("schema_version = %d, want 1", got)
	}
	if got := scalar[int](t, db, "SELECT count(*) FROM sqlite_master WHERE name = 'probe'"); got != 0 {
		t.Fatal("probe survived a rolled back step")
	}
	s := mustOpen(t, path, withSteps(step(2, "CREATE TABLE probe (x INTEGER)", false)))
	if got := schemaVersion(t, s); got != 2 {
		t.Fatalf("after retry SchemaVersion = %d, want 2", got)
	}
}

func TestOpenFailedFirstStepLeavesBootstrap(t *testing.T) {
	path := newPath(t)
	db := rawDB(t, path)

	failing := Options{Store: sqlstore.Options{Migrations: []sqlstore.Migration{step(1, "CREATE TABLE probe (x INTEGER)", true)}}}
	if _, err := openAt(t, path, failing); err == nil {
		t.Fatal("Open with a failing step 1 succeeded")
	}
	if got := scalar[int](t, db, "SELECT schema_version FROM schema_meta"); got != 0 {
		t.Fatalf("schema_version = %d, want 0", got)
	}
	if got := scalar[int32](t, db, "PRAGMA application_id"); got != ApplicationID {
		t.Fatalf("application_id = %#x, want %#x", got, ApplicationID)
	}
	if got := scalar[int](t, db, "SELECT count(*) FROM sqlite_master WHERE name = 'probe'"); got != 0 {
		t.Fatal("probe survived")
	}

	s := mustOpen(t, path, Options{})
	if got := schemaVersion(t, s); got != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", got)
	}
	if got := scalar[int](t, db, "SELECT count(*) FROM sqlite_master WHERE name = 'seq_counter'"); got != 1 {
		t.Fatal("version 1 tables missing after recovery")
	}
}

func TestOpenRejectsInvalidMeta(t *testing.T) {
	path := newPath(t)
	_ = mustOpen(t, path, Options{}).Close()
	db := rawDB(t, path)

	execAll(t, db, "UPDATE schema_meta SET engine = 'postgres'")
	if _, err := openAt(t, path, Options{}); !errors.Is(err, store.ErrSchemaInvalid) {
		t.Fatalf("engine postgres: err = %v, want ErrSchemaInvalid", err)
	}
	execAll(t, db, "UPDATE schema_meta SET engine = 'mysql'")
	if _, err := openAt(t, path, Options{}); !errors.Is(err, store.ErrSchemaInvalid) {
		t.Fatalf("unknown engine: err = %v, want ErrSchemaInvalid", err)
	}
	execAll(t, db, "UPDATE schema_meta SET engine = 'sqlite', schema_version = -1")
	if _, err := openAt(t, path, Options{}); !errors.Is(err, store.ErrSchemaInvalid) {
		t.Fatalf("version -1: err = %v, want ErrSchemaInvalid", err)
	}
	execAll(t, db, "UPDATE schema_meta SET schema_version = 1")
	gap := Options{Store: sqlstore.Options{Migrations: append(Migrations(), step(3, "SELECT 1", false))}}
	if _, err := openAt(t, path, gap); !errors.Is(err, store.ErrSchemaInvalid) {
		t.Fatalf("gap in set: err = %v, want ErrSchemaInvalid", err)
	}
}

func TestOpenConcurrentEmpty(t *testing.T) {
	path := newPath(t)
	var wg sync.WaitGroup
	stores := make([]store.Store, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			stores[i], errs[i] = openAt(t, path, Options{})
		}()
	}
	close(start)
	wg.Wait()
	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("Open %d: %v", i, errs[i])
		}
		defer func() { _ = stores[i].Close() }()
	}
	db := rawDB(t, path)
	if got := scalar[int](t, db, "SELECT count(*) FROM schema_meta"); got != 1 {
		t.Fatalf("schema_meta rows = %d, want 1", got)
	}
	if got := scalar[int](t, db, "SELECT schema_version FROM schema_meta"); got != 1 {
		t.Fatalf("schema_version = %d, want 1", got)
	}
}
