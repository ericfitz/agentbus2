package bus

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
// The prefix b_ keeps it apart from the Store suite's t_ schemas, so neither
// suite's sweep drops the other's.
var testSchemaRE = regexp.MustCompile(`^b_([0-9]+)_[0-9a-f]{8}$`)

// staleAfter is how old a test schema must be before the sweep drops it.
const staleAfter = time.Hour

var sweepOnce sync.Once

// fixedNowMs is the Store clock and the harness clock's start.
const fixedNowMs int64 = 1_700_000_000_000

// sweepStale drops every b_ schema on db older than staleAfter at now, so a
// killed run's leftovers disappear on the next run; younger schemas (an
// overlapping run's) are never touched. It returns how many it dropped.
func sweepStale(ctx context.Context, db *sql.DB, now time.Time) (int, error) {
	rows, err := db.QueryContext(ctx, "SELECT nspname FROM pg_namespace WHERE nspname ~ '^b_[0-9]+_[0-9a-f]{8}$'")
	if err != nil {
		return 0, err
	}
	var stale []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return 0, err
		}
		m := testSchemaRE.FindStringSubmatch(name)
		if m == nil {
			continue // the server-side regexp is looser than ours; never interpolate an unvalidated name
		}
		secs, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		if now.Sub(time.Unix(secs, 0)) > staleAfter {
			stale = append(stale, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()
	dropped := 0
	for _, name := range stale {
		if _, err := db.ExecContext(ctx, "DROP SCHEMA "+name+" CASCADE"); err != nil {
			return dropped, err
		}
		dropped++
	}
	return dropped, nil
}

// openStore prepares a fresh isolated database on e and opens it. sqlite: a
// new file under t.TempDir(). postgres: a schema b_<unix seconds>_<8 hex>
// created on e.Open(t) and dropped in t.Cleanup after every store has been
// closed (cleanups run last in, first out), after the once-per-process sweep
// when AGENTBUS2_TEST_POSTGRES_DSN names a developer's own server (a
// testcontainer is discarded). reopen opens another handle on the same
// database (a second Store over the same file or schema); every handle is
// closed at cleanup.
func openStore(t *testing.T, e dbtest.Engine) (s ab2store.Store, reopen func() (ab2store.Store, error)) {
	t.Helper()
	opts := sqlstore.Options{Now: func() int64 { return fixedNowMs }}
	register := func(s ab2store.Store, err error) (ab2store.Store, error) {
		if err == nil {
			t.Cleanup(func() { _ = s.Close() })
		}
		return s, err
	}
	switch e.Name {
	case "sqlite":
		path := e.DSN(t)
		reopen = func() (ab2store.Store, error) {
			return register(sqlite.Open(context.Background(), path, sqlite.Options{Store: opts}))
		}
	case "postgres":
		base := e.DSN(t)
		db := e.Open(t)
		if os.Getenv(dbtest.PostgresDSNEnv) != "" {
			sweepOnce.Do(func() {
				if n, err := sweepStale(context.Background(), db, time.Now()); err != nil {
					t.Logf("sweeping stale test schemas: %v", err)
				} else if n > 0 {
					t.Logf("dropped %d stale test schemas", n)
				}
			})
		}
		var rnd [4]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("b_%d_%s", time.Now().Unix(), hex.EncodeToString(rnd[:]))
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
		reopen = func() (ab2store.Store, error) {
			return register(postgres.Open(context.Background(), dsn, postgres.Options{Store: opts}))
		}
	default:
		t.Fatalf("unknown engine %q", e.Name)
	}
	s, err := reopen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, reopen
}
