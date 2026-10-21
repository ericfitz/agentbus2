// Package core holds the rules of Milestone 3 once, over the Store: the
// configuration, the injected clock (Decision 49), the per-identity rate
// limiter (Decision 86), the idempotency receipts (Decisions 46, 67), the one
// ACL check (Decision 80), the append path and the store/bus type
// conversions. Aggregate packages under internal/core build on it; the local
// Bus in internal/bus/local is the thin adapter over those packages.
package core
