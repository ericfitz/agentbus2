package postgres

import (
	"context"
	"embed"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// Migrations is the Postgres migration set, numbered from version 1
// (Decision 53). Each step executes its embedded file as one statement list;
// the files must not contain BEGIN or COMMIT.
func Migrations() []sqlstore.Migration {
	return []sqlstore.Migration{
		{Version: 1, Up: runFile("schema/0001_initial.sql")},
		{Version: 2, Up: runFile("schema/0002_auth.sql")},
		{Version: 3, Up: runFile("schema/0003_assertion_jti.sql")},
		{Version: 4, Up: runFile("schema/0004_assertion_jti_identity.sql")},
		{Version: 5, Up: runFile("schema/0005_human_identity.sql")},
		{Version: 6, Up: runFile("schema/0006_scopes_bridges.sql")},
	}
}

func runFile(name string) func(context.Context, *sqlx.Tx) error {
	return func(ctx context.Context, tx *sqlx.Tx) error {
		b, err := schemaFS.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(b)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
}
