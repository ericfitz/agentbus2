package dbtest

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestEnginesOrderAndDrivers(t *testing.T) {
	want := []Engine{{"sqlite", "sqlite"}, {"postgres", "pgx"}}
	if got := Engines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestSQLiteOpensInTempDir(t *testing.T) {
	db := Engine{"sqlite", "sqlite"}.Open(t)
	var n int
	if err := db.QueryRow("SELECT 1").Scan(&n); err != nil || n != 1 {
		t.Fatalf("got %d %v", n, err)
	}
}

func TestOpenDBUnreachablePostgres(t *testing.T) {
	dsn := "postgres://agentbus2:agentbus2@127.0.0.1:1/agentbus2?sslmode=disable&connect_timeout=2"
	_, err := openDB(context.Background(), Engine{"postgres", "pgx"}, dsn, PostgresDSNEnv)
	if err == nil || !strings.HasPrefix(err.Error(), "postgres from AGENTBUS2_TEST_POSTGRES_DSN is unreachable") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "agentbus2:agentbus2@") {
		t.Fatalf("error leaks the DSN: %v", err)
	}
}

func TestOpenDBRejectsMalformedDSN(t *testing.T) {
	_, err := openDB(context.Background(), Engine{"postgres", "pgx"}, "not-a-dsn", PostgresDSNEnv)
	if err == nil || !strings.HasPrefix(err.Error(), "postgres from AGENTBUS2_TEST_POSTGRES_DSN could not be opened") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "not-a-dsn") {
		t.Fatalf("error leaks the DSN: %v", err)
	}
}

func TestEngineRejectsUnknown(t *testing.T) {
	for _, e := range []Engine{{"mysql", "mysql"}, {"sqlite", "pgx"}, {"postgres", "sqlite"}, {"", ""}} {
		err := e.validate()
		if err == nil || !strings.HasPrefix(err.Error(), "dbtest: unknown engine") {
			t.Fatalf("%v: validate got %v", e, err)
		}
		_, err2 := openDB(context.Background(), e, "x", "y")
		if err2 == nil || err2.Error() != err.Error() {
			t.Fatalf("%v: openDB got %v", e, err2)
		}
	}
	for _, e := range Engines() {
		if err := e.validate(); err != nil {
			t.Fatalf("%v: %v", e, err)
		}
	}
}

func TestForEachEngineRunsBothInOrder(t *testing.T) {
	var got []Engine
	var names []string
	t.Run("group", func(t *testing.T) {
		ForEachEngine(t, func(t *testing.T, e Engine) {
			got = append(got, e)
			names = append(names, t.Name())
		})
	})
	if !reflect.DeepEqual(got, Engines()) || len(names) != 2 {
		t.Fatalf("got %v", got)
	}
	if !strings.HasSuffix(names[0], "/sqlite") || !strings.HasSuffix(names[1], "/postgres") {
		t.Fatalf("names %v", names)
	}
}
