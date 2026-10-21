package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/postgres"
)

// withDatabase returns dsn pointing at database name (URL or keyword/value).
func withDatabase(t *testing.T, dsn, name string) string {
	t.Helper()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/" + name
		return u.String()
	}
	return dsn + " dbname=" + name // a later keyword wins
}

// Postgres Open refuses a database whose ctype is not UTF-8 (Decision (Eric,
// 2026-10-21), Milestone 2 Gate 0 item 6).
func TestPostgresOpenRefusesNonUTF8Ctype(t *testing.T) {
	e := dbtest.Engines()[1]
	if e.Name != "postgres" {
		t.Fatalf("engine %q is not postgres", e.Name)
	}
	admin := e.Open(t)
	ctx := context.Background()
	const name = "agentbus2_ctype_c"
	if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name+" WITH TEMPLATE template0 ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C'"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	s, err := postgres.Open(ctx, withDatabase(t, e.DSN(t), name), postgres.Options{})
	if err == nil {
		_ = s.Close()
		t.Fatal("Open accepted a database with LC_CTYPE 'C'")
	}
	if !errors.Is(err, ab2store.ErrSchemaInvalid) {
		t.Fatalf("err = %v, want ErrSchemaInvalid", err)
	}
	if !strings.Contains(err.Error(), `"C"`) || !strings.Contains(err.Error(), "UTF-8 ctype is required") {
		t.Fatalf("message %q does not name the ctype and the requirement", err)
	}
	db, err := sql.Open(e.Driver, withDatabase(t, e.DSN(t), name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("refused Open left %d tables behind", n)
	}
}
