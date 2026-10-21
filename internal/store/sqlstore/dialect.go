package sqlstore

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/search"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Dialect holds the only engine-specific code (plan choice 3).
type Dialect interface {
	Name() store.Engine
	BindType() int              // sqlx.QUESTION or sqlx.DOLLAR
	IsRetryable(err error) bool // choice 5
	// IsReadOnlyViolation: Postgres SQLSTATE 25006 (a write reached the READ
	// ONLY transaction); SQLite: always false (a deferred begin upgrades to a
	// write silently, so the Go-side guard is the only one there).
	IsReadOnlyViolation(err error) bool
	// IsEmpty: no user tables.
	IsEmpty(ctx context.Context, db *sqlx.DB) (bool, error)
	// TableExists is the same catalog query for one table. Open asks for
	// "schema_meta" before selecting from it, so ErrNotAgentbus2 never
	// depends on classifying an engine's error.
	TableExists(ctx context.Context, db *sqlx.DB, name string) (bool, error)
	Stamp(ctx context.Context, db *sqlx.DB) error          // SQLite: PRAGMA application_id; Postgres: no-op
	CheckStamp(ctx context.Context, db *sqlx.DB) error     // SQLite: ErrNotAgentbus2 on mismatch; Postgres: no-op
	LockMigrations(ctx context.Context, tx *sqlx.Tx) error // SQLite: no-op (immediate tx); Postgres: advisory lock
	LockAppend(ctx context.Context, tx *sqlx.Tx) error     // SQLite: no-op; Postgres: LOCK TABLE seq_counter (choice 4)
	// TextSearch (Task 10) returns a condition on alias m with ? placeholders.
	TextSearch(q search.Query) (cond string, args []any, err error)
	// NotifyAppend is the Milestone 7 hook; a no-op now.
	NotifyAppend(ctx context.Context, tx *sqlx.Tx, channelID int64, seq int64) error
}

// Backoffer is an optional Dialect extension: the wait before retry
// attempt+1 after attempt (counting from 1) failed with a retryable error
// (Decision 69's jittered backoff; Postgres only).
type Backoffer interface {
	Backoff(attempt int) time.Duration
}

// Migration is one schema step; Version is the version reached by running Up.
type Migration struct {
	Version int
	Up      func(ctx context.Context, tx *sqlx.Tx) error
}

// Options configures Open.
type Options struct {
	Migrations []Migration  // nil: the engine's set; tests may pass the engine's set plus extra steps
	TxAttempts int          // 0: 1 here; the Postgres engine substitutes its own default (5) before calling Open
	Now        func() int64 // nil: time.Now().UnixMilli(); schema_meta.created_at only
}
