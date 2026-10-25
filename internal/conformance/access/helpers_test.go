package access

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/postgres"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

const now int64 = 1_700_000_000_000

// openStore opens a fresh isolated store on e (a temp file for SQLite, a
// schema per test for Postgres, dropped on cleanup).
func openStore(t *testing.T, e dbtest.Engine) store.Store {
	t.Helper()
	opts := sqlstore.Options{Now: func() int64 { return now }}
	var (
		s   store.Store
		err error
	)
	switch e.Name {
	case "sqlite":
		s, err = sqlite.Open(context.Background(), e.DSN(t), sqlite.Options{Store: opts})
	case "postgres":
		base := e.DSN(t)
		db := e.Open(t)
		var rnd [4]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("t_%d_%s", time.Now().Unix(), hex.EncodeToString(rnd[:]))
		if _, err := db.ExecContext(context.Background(), "CREATE SCHEMA "+name); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+name+" CASCADE") })
		var dsn string
		if dsn, err = postgres.WithSearchPath(base, name); err != nil {
			t.Fatal(err)
		}
		s, err = postgres.Open(context.Background(), dsn, postgres.Options{Store: opts})
	default:
		t.Fatalf("unknown engine %q", e.Name)
	}
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustTx(t *testing.T, s store.Store, fn func(ctx context.Context, tx store.Tx) error) {
	t.Helper()
	if err := s.Tx(context.Background(), fn); err != nil {
		t.Fatalf("Tx: %v", err)
	}
}

func mustRead(t *testing.T, s store.Store, fn func(ctx context.Context, tx store.Tx) error) {
	t.Helper()
	if err := s.Read(context.Background(), fn); err != nil {
		t.Fatalf("Read: %v", err)
	}
}
