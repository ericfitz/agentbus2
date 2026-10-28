package postgres

import (
	"context"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// usageQuery is one statement: the logical bytes (the sum of size_bytes over
// sqlstore.MeasuredTables) and the disk bytes (the total relation size of the
// current schema's tables, dead tuples included).
var usageQuery = buildUsageQuery()

func buildUsageQuery() string {
	parts := make([]string, len(sqlstore.MeasuredTables))
	for i, t := range sqlstore.MeasuredTables {
		parts[i] = "(SELECT coalesce(sum(size_bytes), 0) FROM " + t + ")"
	}
	return "SELECT (" + strings.Join(parts, " + ") + ")::bigint AS logical, " +
		"(SELECT coalesce(sum(pg_total_relation_size(c.oid)), 0)::bigint FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relkind = 'r') AS disk"
}

// Usage reports the logical bytes (the enforced measure: deleting a row lowers
// it by exactly its size_bytes, vacuum or not) and the disk bytes.
func (dialect) Usage(ctx context.Context, db *sqlx.DB) (store.Usage, error) {
	var row struct{ Logical, Disk int64 }
	if err := db.GetContext(ctx, &row, usageQuery); err != nil {
		return store.Usage{}, err
	}
	logical, disk := row.Logical, row.Disk
	return store.Usage{Bytes: logical, DiskBytes: disk, Logical: true}, nil
}

// Reclaim is a no-op: autovacuum owns it.
func (dialect) Reclaim(context.Context, *sqlx.DB) error { return nil }
