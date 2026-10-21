package store

import "context"

// Usage is the database's size on disk. Evictable: deleting rows lowers Bytes
// promptly (SQLite); false on Postgres, where the relation size includes dead
// tuples until vacuum, so a budget is never enforced against it (Gate 0 item
// 2, Decision (Eric, 2026-10-21)).
type Usage struct {
	Bytes     int64
	Evictable bool
}

// Space reports and reclaims database space.
type Space interface {
	Usage(ctx context.Context) (Usage, error)
	// Reclaim returns freed pages to the filesystem. SQLite: PRAGMA
	// incremental_vacuum(256) outside any transaction; Postgres: nil.
	Reclaim(ctx context.Context) error
}
