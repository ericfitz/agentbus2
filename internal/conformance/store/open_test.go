package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// openStep is a migration step running sqlText, optionally failing after it.
func openStep(version int, sqlText string, fail bool) sqlstore.Migration {
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

const probeDDL = "CREATE TABLE probe (x INTEGER)"

func otherEngine(e dbtest.Engine) string {
	if e.Name == "sqlite" {
		return "postgres"
	}
	return "sqlite"
}

func TestOpenEmptyCreatesSchema(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		latest := len(migrationsFor(e))
		s, reopen, rawDSN := openStore(t, e, sqlstore.Options{})
		if got := schemaVersion(t, s); got != latest {
			t.Fatalf("SchemaVersion = %d, want %d", got, latest)
		}
		if string(s.Engine()) != e.Name {
			t.Fatalf("Engine = %q, want %q", s.Engine(), e.Name)
		}
		db := rawDB(t, e, rawDSN)
		var app, engine string
		var created int64
		if err := db.QueryRow("SELECT app, engine, created_at FROM schema_meta").Scan(&app, &engine, &created); err != nil {
			t.Fatal(err)
		}
		if app != "agentbus2" || engine != e.Name || created != fixedNow {
			t.Fatalf("schema_meta = %q %q %d", app, engine, created)
		}
		if got := scalar[int](t, db, "SELECT count(*) FROM schema_meta"); got != 1 {
			t.Fatalf("schema_meta rows = %d", got)
		}
		if got := scalar[int](t, db, "SELECT v FROM seq_counter"); got != 0 {
			t.Fatalf("seq_counter.v = %d", got)
		}
		if got := scalar[int](t, db, "SELECT count(*) FROM leases WHERE name = 'maintenance'"); got != 1 {
			t.Fatalf("maintenance lease rows = %d", got)
		}
		if e.Name == "sqlite" {
			if got := scalar[int32](t, db, "PRAGMA application_id"); got != 0x61623200 {
				t.Fatalf("application_id = %#x", got)
			}
			if got := scalar[int](t, db, "PRAGMA user_version"); got != 0 {
				t.Fatalf("user_version = %d, want 0", got)
			}
		}

		// Second run: nothing changes, created_at stays.
		_ = s.Close()
		s2, err := reopen(sqlstore.Options{Now: func() int64 { return 9999 }})
		if err != nil {
			t.Fatalf("second Open: %v", err)
		}
		if got := schemaVersion(t, s2); got != latest {
			t.Fatalf("second SchemaVersion = %d, want %d", got, latest)
		}
		if got := scalar[int64](t, db, "SELECT created_at FROM schema_meta"); got != fixedNow {
			t.Fatalf("created_at after reopen = %d, want %d", got, fixedNow)
		}
	})
}

func TestOpenRefusesNonEmptyWithoutSchemaMeta(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		reopen, rawDSN := isolate(t, e)
		db := rawDB(t, e, rawDSN)
		count := func() int {
			if e.Name == "sqlite" {
				return scalar[int](t, db, "SELECT count(*) FROM sqlite_master")
			}
			return scalar[int](t, db, "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()")
		}
		if e.Name == "sqlite" {
			execAll(t, db, "CREATE TABLE channels (name TEXT PRIMARY KEY, kind TEXT)", "PRAGMA user_version = 12")
		} else {
			execAll(t, db, "CREATE TABLE stray (x INTEGER)")
		}
		before := count()

		if _, err := reopen(sqlstore.Options{}); !errors.Is(err, ab2store.ErrNotAgentbus2) {
			t.Fatalf("err = %v, want ErrNotAgentbus2", err)
		}
		if got := count(); got != before {
			t.Fatalf("table count %d -> %d (the refused open changed the database)", before, got)
		}
		if got := tableCount(t, e, db, "schema_meta"); got != 0 {
			t.Fatalf("schema_meta tables = %d, want 0", got)
		}
		if e.Name == "sqlite" {
			if got := scalar[int](t, db, "PRAGMA user_version"); got != 12 {
				t.Fatalf("user_version = %d, want 12", got)
			}
			if got := scalar[int32](t, db, "PRAGMA application_id"); got != 0 {
				t.Fatalf("application_id = %#x, want 0 (file untouched)", got)
			}
		}
	})
}

func TestOpenRefusesWrongApp(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, reopen, rawDSN := openStore(t, e, sqlstore.Options{})
		_ = s.Close()
		db := rawDB(t, e, rawDSN)

		execAll(t, db, "UPDATE schema_meta SET app = 'other'")
		if _, err := reopen(sqlstore.Options{}); !errors.Is(err, ab2store.ErrNotAgentbus2) {
			t.Fatalf("wrong app: err = %v, want ErrNotAgentbus2", err)
		}
		if e.Name == "sqlite" { // Stamp and CheckStamp are no-ops on postgres
			execAll(t, db, "UPDATE schema_meta SET app = 'agentbus2'", "PRAGMA application_id = 0")
			if _, err := reopen(sqlstore.Options{}); !errors.Is(err, ab2store.ErrNotAgentbus2) {
				t.Fatalf("application_id 0: err = %v, want ErrNotAgentbus2", err)
			}
		}
	})
}

func TestOpenRefusesNewerVersion(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, reopen, rawDSN := openStore(t, e, sqlstore.Options{})
		_ = s.Close()
		latest := len(migrationsFor(e))
		execAll(t, rawDB(t, e, rawDSN), fmt.Sprintf("UPDATE schema_meta SET schema_version = %d", latest+1))
		_, err := reopen(sqlstore.Options{})
		if !errors.Is(err, ab2store.ErrUpgradeNeeded) {
			t.Fatalf("err = %v, want ErrUpgradeNeeded", err)
		}
		if !strings.Contains(err.Error(), "upgrade agentbus2") {
			t.Fatalf("err %q lacks 'upgrade agentbus2'", err)
		}
	})
}

func TestOpenMigratesOlderVersion(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, reopen, rawDSN := openStore(t, e, sqlstore.Options{})
		_ = s.Close()
		db := rawDB(t, e, rawDSN)

		latest := len(migrationsFor(e))
		opts := sqlstore.Options{Migrations: append(migrationsFor(e), openStep(latest+1, probeDDL, false))}
		s2, err := reopen(opts)
		if err != nil {
			t.Fatalf("Open with step %d: %v", latest+1, err)
		}
		if got := schemaVersion(t, s2); got != latest+1 {
			t.Fatalf("SchemaVersion = %d, want %d", got, latest+1)
		}
		if got := tableCount(t, e, db, "probe"); got != 1 {
			t.Fatalf("probe tables = %d", got)
		}
		_ = s2.Close()

		// Second run with the same set changes nothing (a re-run of the probe step would fail: probe exists).
		opts.Migrations = append(migrationsFor(e), openStep(latest+1, probeDDL, false))
		s3, err := reopen(opts)
		if err != nil {
			t.Fatalf("third Open: %v", err)
		}
		if got := schemaVersion(t, s3); got != latest+1 {
			t.Fatalf("third open SchemaVersion = %d, want %d", got, latest+1)
		}
	})
}

func TestOpenFailedStepLeavesPreviousVersion(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		t.Run("step_past_latest", func(t *testing.T) {
			latest := len(migrationsFor(e))
			s, reopen, rawDSN := openStore(t, e, sqlstore.Options{})
			_ = s.Close()
			db := rawDB(t, e, rawDSN)

			failing := sqlstore.Options{Migrations: append(migrationsFor(e), openStep(latest+1, probeDDL, true))}
			if _, err := reopen(failing); err == nil {
				t.Fatal("Open with a failing step succeeded")
			}
			if got := scalar[int](t, db, "SELECT schema_version FROM schema_meta"); got != latest {
				t.Fatalf("schema_version = %d, want %d", got, latest)
			}
			if got := tableCount(t, e, db, "probe"); got != 0 {
				t.Fatal("probe survived a rolled back step")
			}
			working := sqlstore.Options{Migrations: append(migrationsFor(e), openStep(latest+1, probeDDL, false))}
			s2, err := reopen(working)
			if err != nil {
				t.Fatalf("retry Open: %v", err)
			}
			if got := schemaVersion(t, s2); got != latest+1 {
				t.Fatalf("after retry SchemaVersion = %d, want %d", got, latest+1)
			}
		})
		t.Run("step1_on_fresh_database", func(t *testing.T) {
			reopen, rawDSN := isolate(t, e)
			db := rawDB(t, e, rawDSN)

			failing := sqlstore.Options{Migrations: []sqlstore.Migration{openStep(1, probeDDL, true)}}
			if _, err := reopen(failing); err == nil {
				t.Fatal("Open with a failing step 1 succeeded")
			}
			if got := scalar[int](t, db, "SELECT schema_version FROM schema_meta"); got != 0 {
				t.Fatalf("schema_version = %d, want 0 (bootstrap committed, step rolled back)", got)
			}
			if e.Name == "sqlite" {
				if got := scalar[int32](t, db, "PRAGMA application_id"); got != 0x61623200 {
					t.Fatalf("application_id = %#x", got)
				}
			}
			if got := tableCount(t, e, db, "probe"); got != 0 {
				t.Fatal("probe survived a rolled back step")
			}
			s, err := reopen(sqlstore.Options{})
			if err != nil {
				t.Fatalf("recovery Open: %v", err)
			}
			if got := schemaVersion(t, s); got != len(migrationsFor(e)) {
				t.Fatalf("SchemaVersion = %d, want %d", got, len(migrationsFor(e)))
			}
			if got := tableCount(t, e, db, "seq_counter"); got != 1 {
				t.Fatal("version 1 tables missing after recovery")
			}
		})
	})
}

func TestOpenRejectsInvalidMeta(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, reopen, rawDSN := openStore(t, e, sqlstore.Options{})
		_ = s.Close()
		db := rawDB(t, e, rawDSN)

		for _, bad := range []string{otherEngine(e), "mysql", ""} {
			execAll(t, db, "UPDATE schema_meta SET engine = '"+bad+"'")
			if _, err := reopen(sqlstore.Options{}); !errors.Is(err, ab2store.ErrSchemaInvalid) {
				t.Fatalf("engine %q: err = %v, want ErrSchemaInvalid", bad, err)
			}
		}
		latest := len(migrationsFor(e))
		execAll(t, db, "UPDATE schema_meta SET engine = '"+e.Name+"', schema_version = -1")
		if _, err := reopen(sqlstore.Options{}); !errors.Is(err, ab2store.ErrSchemaInvalid) {
			t.Fatalf("version -1: err = %v, want ErrSchemaInvalid", err)
		}
		execAll(t, db, fmt.Sprintf("UPDATE schema_meta SET schema_version = %d", latest))
		gap := sqlstore.Options{Migrations: append(migrationsFor(e), openStep(latest+2, "SELECT 1", false))}
		if _, err := reopen(gap); !errors.Is(err, ab2store.ErrSchemaInvalid) {
			t.Fatalf("gap in set: err = %v, want ErrSchemaInvalid", err)
		}
		// The rejected opens changed nothing: the plain set still opens it.
		if _, err := reopen(sqlstore.Options{}); err != nil {
			t.Fatalf("restored meta did not open: %v", err)
		}
	})
}

// TestOpenConcurrentEmpty is the overlap proof: two opens of a never-opened
// database released by one barrier must both succeed and migrate once. It
// must not use openStore (the proof is vacuous on an opened database).
func TestOpenConcurrentEmpty(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		reopen, rawDSN := isolate(t, e)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = reopen(sqlstore.Options{})
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("Open %d: %v", i, err)
			}
		}
		db := rawDB(t, e, rawDSN)
		if got := scalar[int](t, db, "SELECT count(*) FROM schema_meta"); got != 1 {
			t.Fatalf("schema_meta rows = %d, want 1", got)
		}
		if got := scalar[int](t, db, "SELECT schema_version FROM schema_meta"); got != len(migrationsFor(e)) {
			t.Fatalf("schema_version = %d, want %d", got, len(migrationsFor(e)))
		}
	})
}

// TestOpenMigratesVersion1Database opens a database at version 1 (the set cut
// to its first step), which has neither auth table nor assertion_jti, then
// reopens with the full set.
func TestOpenMigratesVersion1Database(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		full := migrationsFor(e)
		s, reopen, rawDSN := openStore(t, e, sqlstore.Options{Migrations: full[:1]})
		db := rawDB(t, e, rawDSN)
		if got := schemaVersion(t, s); got != 1 {
			t.Fatalf("SchemaVersion = %d, want 1", got)
		}
		for _, tbl := range []string{"enrollment_tokens", "signing_keys", "assertion_jti"} {
			if got := tableCount(t, e, db, tbl); got != 0 {
				t.Fatalf("%s tables at version 1 = %d, want 0", tbl, got)
			}
		}
		_ = s.Close()
		for i := range 2 { // the second reopen changes nothing
			s2, err := reopen(sqlstore.Options{})
			if err != nil {
				t.Fatalf("reopen %d: %v", i, err)
			}
			if got := schemaVersion(t, s2); got != len(full) {
				t.Fatalf("SchemaVersion = %d, want %d", got, len(full))
			}
			for _, tbl := range []string{"enrollment_tokens", "signing_keys", "assertion_jti"} {
				if got := tableCount(t, e, db, tbl); got != 1 {
					t.Fatalf("%s tables = %d, want 1", tbl, got)
				}
			}
			_ = s2.Close()
		}
	})
}
