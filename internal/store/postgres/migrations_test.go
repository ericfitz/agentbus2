package postgres_test

import (
	"slices"
	"testing"

	"github.com/ericfitz/agentbus2/internal/store/postgres"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// TestMigrationSetsAlign: one migration set per engine, numbered in step
// (Decisions 34 and 53). It lives here because sqlstore cannot import the
// engines that import it.
func TestMigrationSetsAlign(t *testing.T) {
	versions := func(ms []sqlstore.Migration) []int {
		var v []int
		for _, m := range ms {
			v = append(v, m.Version)
		}
		return v
	}
	pg, lite := versions(postgres.Migrations()), versions(sqlite.Migrations())
	if len(pg) == 0 || !slices.Equal(pg, lite) {
		t.Fatalf("postgres versions %v, sqlite versions %v", pg, lite)
	}
}
