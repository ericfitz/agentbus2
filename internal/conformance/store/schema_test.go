package store

import (
	"context"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// TestSchemaVersionMatchesMigrations proves Store.SchemaVersion, called on the
// interface, reports the engine's migration count after Open on both engines.
func TestSchemaVersionMatchesMigrations(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		v, err := s.SchemaVersion(context.Background())
		if err != nil {
			t.Fatalf("SchemaVersion: %v", err)
		}
		if want := len(migrationsFor(e)); v != want {
			t.Fatalf("SchemaVersion = %d, want %d", v, want)
		}
	})
}
