package sqlite

import (
	"context"
	"embed"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// Migrations is the SQLite migration set, numbered from version 1
// (Decision 53). Version 1 is the whole schema: the pre-release steps were
// flattened before the first release (ADR 0004 addendum, 2026-10-25); later
// steps add 0002 and up. Each step executes its embedded file as one statement list;
// the files must not contain BEGIN or COMMIT.
func Migrations() []sqlstore.Migration {
	return []sqlstore.Migration{
		{Version: 1, Up: runFile("schema/0001_initial.sql")},
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
