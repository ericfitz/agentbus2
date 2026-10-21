package store

import "context"

// Locks are exclusive, named, cross-process locks (Decisions 49, 68).
//
// SQLite: the leases row of v1 takeLease, held until `until` and renewed by
// its owner. Postgres: pg_try_advisory_lock(hashtext(current_schema()),
// hashtext(name)) on a connection the Store pins per name; now and until are
// ignored (a session lock has no clock). Held across calls either way:
// TryLock by the holder renews (SQLite) or re-asserts (Postgres) and Unlock
// releases. Close releases every lock the Store holds.
type Locks interface {
	// TryLock takes or renews the named lock for owner. ErrInvalid on a name
	// or owner empty or failing TextRule, or unless 0 < now < until; (false,
	// nil) when another owner holds it.
	TryLock(ctx context.Context, name, owner string, now, until int64) (held bool, err error)
	// Unlock releases the lock; a no-op (nil) unless this Store holds it for
	// owner (another handle's lock under the same owner is left alone);
	// ErrInvalid as TryLock on the name and owner.
	Unlock(ctx context.Context, name, owner string) error
}
