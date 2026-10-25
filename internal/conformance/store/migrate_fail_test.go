package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// failAfter runs the real migration step and then fails, so the whole step
// has executed inside its transaction when the failure rolls it back.
func failAfter(m sqlstore.Migration) sqlstore.Migration {
	up := m.Up
	return sqlstore.Migration{Version: m.Version, Up: func(ctx context.Context, tx *sqlx.Tx) error {
		if err := up(ctx, tx); err != nil {
			return err
		}
		return errors.New("forced failure after the step ran")
	}}
}

// TestMigrationStepFailureRollsBack forces a failure after migration 5 and
// after migration 6 on a database holding data from the step before: the
// failed open leaves the previous version with none of the step's objects,
// and the next open reruns the step from clean (one row per seeded entry,
// not two).
func TestMigrationStepFailureRollsBack(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		full := migrationsFor(e)
		if len(full) < 6 {
			t.Fatalf("want at least 6 migrations, have %d", len(full))
		}
		for _, c := range []struct {
			step   int
			seed   []string
			absent []string // tables the step creates
			check  func(t *testing.T, db *sql.DB)
		}{
			{step: 5,
				seed:   []string{"INSERT INTO identities (name, is_admin, tags_acked_seq, created_at) VALUES ('adm', TRUE, 0, 1)"},
				absent: []string{"human_bindings", "access_refs"},
				check: func(t *testing.T, db *sql.DB) {
					// The admin flag became exactly one admins entry.
					if got := scalar[int](t, db, "SELECT count(*) FROM access_refs WHERE ref_list = 'admins' AND ref_kind = 'identity' AND value = 'adm'"); got != 1 {
						t.Errorf("admins entries for adm = %d, want 1", got)
					}
					if got := scalar[string](t, db, "SELECT kind FROM identities WHERE name = 'adm'"); got != "agent" {
						t.Errorf("adm kind = %q, want agent", got)
					}
				}},
			{step: 6,
				seed: []string{
					"INSERT INTO identities (name, is_admin, tags_acked_seq, created_at) VALUES ('slack-bridge', TRUE, 0, 1)",
					"INSERT INTO tag_subscriptions (identity, tags_key, created_seq) VALUES ('slack-bridge', 'k', 1)",
					"INSERT INTO access_refs (ref_list, ref_kind, issuer, value, created_at) VALUES ('admins', 'identity', '', 'slack-bridge', 1)",
				},
				absent: []string{"scopes", "bridges", "bridge_messages", "slack_users", "bridge_dropped", "retired_identities"},
				check: func(t *testing.T, db *sql.DB) {
					// The pre-existing row was made the reserved shape, once.
					if got := scalar[int](t, db, "SELECT count(*) FROM identities WHERE name = 'slack-bridge' AND is_admin = FALSE AND kind = 'agent'"); got != 1 {
						t.Errorf("reserved slack-bridge rows = %d, want 1", got)
					}
					for _, q := range []string{
						"SELECT count(*) FROM tag_subscriptions WHERE identity = 'slack-bridge'",
						"SELECT count(*) FROM access_refs WHERE ref_kind = 'identity' AND value = 'slack-bridge'",
					} {
						if got := scalar[int](t, db, q); got != 0 {
							t.Errorf("%s = %d, want 0", q, got)
						}
					}
				}},
		} {
			t.Run(fmt.Sprintf("step%d", c.step), func(t *testing.T) {
				before := full[:c.step-1]
				prev := c.step - 1
				s, reopen, rawDSN := openStore(t, e, sqlstore.Options{Migrations: before})
				_ = s.Close()
				db := rawDB(t, e, rawDSN)
				if got := scalar[int](t, db, "SELECT schema_version FROM schema_meta"); got != prev {
					t.Fatalf("seed version = %d, want %d", got, prev)
				}
				execAll(t, db, c.seed...)
				idents := scalar[int](t, db, "SELECT count(*) FROM identities")

				failing := append(append([]sqlstore.Migration{}, before...), failAfter(full[c.step-1]))
				if _, err := reopen(sqlstore.Options{Migrations: failing}); err == nil {
					t.Fatalf("Open with a failing step %d succeeded", c.step)
				}
				if got := scalar[int](t, db, "SELECT schema_version FROM schema_meta"); got != prev {
					t.Fatalf("schema_version after the failed step = %d, want %d", got, prev)
				}
				for _, tbl := range c.absent {
					if got := tableCount(t, e, db, tbl); got != 0 {
						t.Errorf("table %s survived the rolled back step", tbl)
					}
				}
				if got := scalar[int](t, db, "SELECT count(*) FROM identities"); got != idents {
					t.Errorf("identities after the rollback = %d, want %d", got, idents)
				}

				s2, err := reopen(sqlstore.Options{})
				if err != nil {
					t.Fatalf("rerun Open: %v", err)
				}
				if got := schemaVersion(t, s2); got != len(full) {
					t.Fatalf("SchemaVersion after rerun = %d, want %d", got, len(full))
				}
				for _, tbl := range c.absent {
					if got := tableCount(t, e, db, tbl); got != 1 {
						t.Errorf("table %s after the rerun = %d, want 1", tbl, got)
					}
				}
				c.check(t, db)
			})
		}
	})
}
