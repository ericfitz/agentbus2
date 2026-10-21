package store

import "context"

// Engine names the database engine behind a Store.
type Engine string

const (
	EngineSQLite   Engine = "sqlite"
	EnginePostgres Engine = "postgres"
)

// Store is the entry point to persistence.
type Store interface {
	Engine() Engine
	// Tx runs fn in a serializable transaction. fn may run more than once and
	// must have no side effects (Decision 26); a transaction that cannot
	// complete returns ErrRetryable (Decision 69).
	Tx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	// Append is as Tx, and the only kind in which NextSeq and AppendMessage
	// work (plan choice 4).
	Append(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	// Read runs fn in a read-only snapshot; a write inside it fails with
	// ErrReadOnly; it is never retried.
	Read(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	Close() error
}

// Tx is the set of repositories available inside a transaction.
type Tx interface {
	Meta
	Identities
	Receipts
	Channels
	Subscriptions
	Sessions
	Messages
	Memories
	Tasks
}
