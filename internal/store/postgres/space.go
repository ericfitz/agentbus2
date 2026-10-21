package postgres

import (
	"context"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Usage sums the total relation size of the current schema's tables.
// Evictable is false: the size includes dead tuples until vacuum, so deleting
// rows does not lower it (Gate 0 item 2).
func (dialect) Usage(ctx context.Context, db *sqlx.DB) (store.Usage, error) {
	var n int64
	err := db.GetContext(ctx, &n, "SELECT coalesce(sum(pg_total_relation_size(c.oid)), 0)::bigint FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relkind = 'r'")
	if err != nil {
		return store.Usage{}, err
	}
	return store.Usage{Bytes: n}, nil
}

// Reclaim is a no-op: autovacuum owns it.
func (dialect) Reclaim(context.Context, *sqlx.DB) error { return nil }
