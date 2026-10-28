package sqlite

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
)

// vacuumPages is the page count of one Reclaim (v1 internal/bus/maintenance.go
// vacuumPages).
const vacuumPages = 256

// autoVacuumIncremental is PRAGMA auto_vacuum's value for INCREMENTAL.
const autoVacuumIncremental = 2

// Usage is v1 internal/bus/bus.go Usage: Bytes is (page_count -
// freelist_count) * page_size, DiskBytes page_count * page_size, the three
// PRAGMAs read on one connection. Logical is false: a delete frees pages, so
// Bytes is measured again.
func (dialect) Usage(ctx context.Context, db *sqlx.DB) (store.Usage, error) {
	conn, err := db.Connx(ctx)
	if err != nil {
		return store.Usage{}, err
	}
	defer func() { _ = conn.Close() }()
	var pages, free, size int64
	for _, p := range []struct {
		q string
		v *int64
	}{{"PRAGMA page_count", &pages}, {"PRAGMA freelist_count", &free}, {"PRAGMA page_size", &size}} {
		if err := conn.GetContext(ctx, p.v, p.q); err != nil {
			return store.Usage{}, err
		}
	}
	return store.Usage{Bytes: (pages - free) * size, DiskBytes: pages * size}, nil
}

// Reclaim runs one incremental_vacuum step, outside any transaction.
func (dialect) Reclaim(ctx context.Context, db *sqlx.DB) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", vacuumPages))
	return err
}

// ensureIncrementalVacuum converts the file to auto_vacuum = INCREMENTAL when
// it is not (v1 internal/bus/bus.go): the pragma only takes effect through a
// VACUUM. A file already in mode 2 costs one PRAGMA read.
func ensureIncrementalVacuum(ctx context.Context, db *sqlx.DB) error {
	conn, err := db.Connx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var mode int
	if err := conn.GetContext(ctx, &mode, "PRAGMA auto_vacuum"); err != nil {
		return err
	}
	if mode == autoVacuumIncremental {
		return nil
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA auto_vacuum = %d", autoVacuumIncremental)); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "VACUUM")
	return err
}
