// Package store is the persistence contract of agentbus2 (spec section 5).
//
// The interfaces are grouped by aggregate (Decision 25) and composed into Tx.
// Store hands a Tx to a callback in one of three transaction kinds: Tx
// (serializable, retried), Append (as Tx, and the only kind in which NextSeq
// and AppendMessage work) and Read (a read-only snapshot, never retried).
// The one implementation is internal/store/sqlstore, driven by the engines in
// internal/store/sqlite and internal/store/postgres.
package store
