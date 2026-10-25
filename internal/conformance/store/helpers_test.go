package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/postgres"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// testSchemaRE matches the per-test Postgres schemas this package creates.
var testSchemaRE = regexp.MustCompile(`^t_([0-9]+)_[0-9a-f]{8}$`)

// staleAfter is how old a test schema must be before sweepStaleSchemas drops it.
const staleAfter = time.Hour

var sweepOnce sync.Once

// migrationsFor returns a fresh copy of the engine's production migration set.
func migrationsFor(e dbtest.Engine) []sqlstore.Migration {
	if e.Name == "postgres" {
		return postgres.Migrations()
	}
	return sqlite.Migrations()
}

// isolate prepares a fresh, isolated, never-opened database on e and opens
// nothing. sqlite: the path filepath.Join(t.TempDir(), "agentbus2.db") (no
// file exists yet). postgres: a schema t_<unix seconds>_<8 hex> created on
// e.Open(t), with DROP SCHEMA ... CASCADE registered in t.Cleanup before any
// store is opened (so cleanup's LIFO order closes stores first), after the
// once-per-process sweep of leftovers. reopen opens that database through the
// engine's own Open and registers Close in t.Cleanup; it may be called any
// number of times, including zero, and defaults opts.Now to fixedNow. rawDSN
// returns the path or the schema-scoped DSN for a plain *sql.DB (never print
// it).
func isolate(t *testing.T, e dbtest.Engine) (reopen func(opts sqlstore.Options) (ab2store.Store, error), rawDSN func() string) {
	t.Helper()
	withNow := func(o sqlstore.Options) sqlstore.Options {
		if o.Now == nil {
			o.Now = func() int64 { return fixedNow }
		}
		return o
	}
	register := func(s ab2store.Store, err error) (ab2store.Store, error) {
		if err == nil {
			t.Cleanup(func() { _ = s.Close() })
		}
		return s, err
	}
	switch e.Name {
	case "sqlite":
		path := e.DSN(t)
		return func(o sqlstore.Options) (ab2store.Store, error) {
				return register(sqlite.Open(context.Background(), path, sqlite.Options{Store: withNow(o)}))
			},
			func() string { return path }
	case "postgres":
		base := e.DSN(t)
		db := e.Open(t)
		if os.Getenv(dbtest.PostgresDSNEnv) != "" { // a testcontainer is discarded; only a developer's own server needs sweeping
			sweepOnce.Do(func() {
				if _, err := sweepStaleSchemas(context.Background(), e, db, time.Now()); err != nil {
					t.Logf("sweeping stale test schemas: %v", err)
				}
			})
		}
		var rnd [4]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("t_%d_%s", time.Now().Unix(), hex.EncodeToString(rnd[:]))
		if !testSchemaRE.MatchString(name) {
			t.Fatalf("generated schema name %q does not match %s", name, testSchemaRE)
		}
		if _, err := db.ExecContext(context.Background(), "CREATE SCHEMA "+name); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		t.Cleanup(func() {
			if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+name+" CASCADE"); err != nil {
				t.Errorf("drop schema %s: %v", name, err)
			}
		})
		dsn, err := postgres.WithSearchPath(base, name)
		if err != nil {
			t.Fatal(err)
		}
		return func(o sqlstore.Options) (ab2store.Store, error) {
				return register(postgres.Open(context.Background(), dsn, postgres.Options{Store: withNow(o)}))
			},
			func() string { return dsn }
	}
	t.Fatalf("unknown engine %q", e.Name)
	return nil, nil
}

// openStore is isolate plus one reopen(opts) that must succeed.
func openStore(t *testing.T, e dbtest.Engine, opts sqlstore.Options) (s ab2store.Store, reopen func(opts sqlstore.Options) (ab2store.Store, error), rawDSN func() string) {
	t.Helper()
	reopen, rawDSN = isolate(t, e)
	s, err := reopen(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, reopen, rawDSN
}

// rawDB opens a plain *sql.DB on the database rawDSN names, bypassing Open.
func rawDB(t *testing.T, e dbtest.Engine, rawDSN func() string) *sql.DB {
	t.Helper()
	db, err := sql.Open(e.Driver, rawDSN())
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// staleSchemas lists the test schemas on db older than staleAfter at now.
func staleSchemas(ctx context.Context, db *sql.DB, now time.Time) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT nspname FROM pg_namespace WHERE nspname ~ '^t_[0-9]+_[0-9a-f]{8}$'")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var stale []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		m := testSchemaRE.FindStringSubmatch(name)
		if m == nil {
			continue // the server-side regexp is looser than ours; never interpolate a name we did not validate
		}
		secs, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		if now.Sub(time.Unix(secs, 0)) > staleAfter {
			stale = append(stale, name)
		}
	}
	return stale, rows.Err()
}

// sweepStaleSchemas drops every test schema older than one hour and returns
// how many it dropped, so a killed run's leftovers disappear on the next run;
// schemas younger than an hour (an overlapping run's) are never touched. On
// sqlite it is a no-op returning 0.
func sweepStaleSchemas(ctx context.Context, e dbtest.Engine, db *sql.DB, now time.Time) (int, error) {
	if e.Name != "postgres" {
		return 0, nil
	}
	stale, err := staleSchemas(ctx, db, now)
	if err != nil {
		return 0, err
	}
	dropped := 0
	for _, name := range stale {
		if _, err := db.ExecContext(ctx, "DROP SCHEMA "+name+" CASCADE"); err != nil {
			return dropped, err
		}
		dropped++
	}
	return dropped, nil
}

func TestHelpersSweepStaleSchemas(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		if e.Name != "postgres" {
			db := e.Open(t)
			n, err := sweepStaleSchemas(ctx, e, db, time.Now())
			if err != nil || n != 0 {
				t.Fatalf("sqlite sweep = %d, %v; want 0, nil", n, err)
			}
			return
		}
		db := e.Open(t)
		now := time.Now()
		stale := fmt.Sprintf("t_%d_%s", now.Add(-2*time.Hour).Unix(), "deadbeef")
		fresh := fmt.Sprintf("t_%d_%s", now.Unix(), "cafe0123")
		for _, name := range []string{stale, fresh} {
			if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+name); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+fresh+" CASCADE") })
		// A shared developer server may hold other stale schemas of killed
		// runs; the sweep must drop exactly those it lists, ours among them.
		want, err := staleSchemas(ctx, db, now)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, n := range want {
			found = found || n == stale
		}
		if !found {
			t.Fatalf("stale schema %s not listed in %v", stale, want)
		}
		dropped, err := sweepStaleSchemas(ctx, e, db, now)
		if err != nil || dropped != len(want) {
			t.Fatalf("sweep = %d, %v; want %d, nil", dropped, err, len(want))
		}
		exists := func(name string) bool {
			var n int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_namespace WHERE nspname = $1", name).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n == 1
		}
		if exists(stale) {
			t.Error("stale schema survived the sweep")
		}
		if !exists(fresh) {
			t.Error("fresh schema was dropped by the sweep")
		}
		// Second run: nothing left to drop.
		if n, err := sweepStaleSchemas(ctx, e, db, now); err != nil || n != 0 {
			t.Errorf("second sweep = %d, %v; want 0, nil", n, err)
		}
	})
}

// scalar runs a one-value query on a raw handle.
func scalar[T any](t *testing.T, db *sql.DB, q string) T {
	t.Helper()
	var v T
	if err := db.QueryRow(q).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// tableCount reports how many tables named name exist in the isolated database.
func tableCount(t *testing.T, e dbtest.Engine, db *sql.DB, name string) int {
	t.Helper()
	if e.Name == "postgres" {
		return scalar[int](t, db, "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = '"+name+"'")
	}
	return scalar[int](t, db, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = '"+name+"'")
}

func execAll(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func schemaVersion(t *testing.T, s ab2store.Store) int {
	t.Helper()
	v, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}
