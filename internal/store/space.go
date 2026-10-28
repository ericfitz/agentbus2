package store

import "context"

// Usage is what the database holds. Bytes is the enforced measure; DiskBytes
// is the physical size. Logical is true when deleting a row lowers Bytes by
// exactly its size_bytes (Postgres: the sum of size_bytes over
// sqlstore.MeasuredTables, which dead tuples do not inflate); false when Bytes
// must be measured again after a delete (SQLite: pages in use times page
// size).
type Usage struct {
	Bytes, DiskBytes int64
	Logical          bool
}

// Space reports and reclaims database space.
type Space interface {
	Usage(ctx context.Context) (Usage, error)
	// Reclaim returns freed pages to the filesystem. SQLite: PRAGMA
	// incremental_vacuum(256) outside any transaction; Postgres: nil.
	Reclaim(ctx context.Context) error
}
