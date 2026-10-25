package store

import "errors"

var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: already exists")
	// ErrInvalid is always wrapped with the reason:
	// fmt.Errorf("%w: channel name must not contain '/'", ErrInvalid).
	ErrInvalid = errors.New("store: invalid input")
	// ErrRetryable: the transaction could not complete (Decision 69).
	ErrRetryable = errors.New("store: transaction could not complete, retry")
	// ErrNotAppend: NextSeq or AppendMessage outside Store.Append.
	ErrNotAppend = errors.New("store: NextSeq and AppendMessage need Store.Append")
	// ErrReadOnly: a write inside Store.Read.
	ErrReadOnly = errors.New("store: write inside Store.Read")
	// ErrNotAgentbus2: wrong or missing app, wrong application_id (Decision 34).
	ErrNotAgentbus2 = errors.New("store: not an agentbus2 database")
	// ErrUpgradeNeeded: the schema is newer than this binary (Decision 34).
	// Before the first release the only such database comes from an earlier
	// build, whose migrations were flattened to version 1 (ADR 0004 addendum,
	// 2026-10-25), so the text also tells the operator to recreate it.
	ErrUpgradeNeeded = errors.New("store: database schema is newer than this binary: upgrade agentbus2, or, if an earlier pre-release build created this database, recreate it (pre-release databases are not converted)")
	// ErrSchemaInvalid: schema_meta is anything else (Decision 34).
	ErrSchemaInvalid = errors.New("store: schema_meta is invalid")
)
