package sqlstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/ericfitz/agentbus2/internal/store"
)

// TryLock: see store.Locks. Both paths consult s.held under s.mu before any
// SQL, so a second owner on this handle never touches the row or the pinned
// connection beneath the first.
func (s *Store) TryLock(ctx context.Context, name, owner string, now, until int64) (bool, error) {
	if err := checkLock(name, owner); err != nil {
		return false, err
	}
	// Checked on both engines although Postgres ignores the times: until <=
	// now would grant a SQLite lease already expired, and a second handle
	// would take it at once.
	if now <= 0 || until <= now {
		return false, fmt.Errorf("%w: lock times must satisfy 0 < now < until, got now %d, until %d", store.ErrInvalid, now, until)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.held[name]; ok && cur != owner {
		return false, nil
	}
	if l, ok := s.d.(Locker); ok {
		return s.tryLockSession(ctx, l, name, owner)
	}
	var held bool
	err := s.Tx(ctx, func(ctx context.Context, t store.Tx) error {
		x := t.(*tx)
		if _, err := x.execW(ctx,
			"INSERT INTO leases (name, owner, expires_at) VALUES (?, '', 0) ON CONFLICT (name) DO NOTHING", name); err != nil {
			return err
		}
		var err error
		held, err = t.TryLease(ctx, name, owner, now, until)
		return err
	})
	if err != nil {
		return false, err
	}
	if held {
		s.held[name] = owner
	} else if s.held[name] == owner {
		delete(s.held, name)
	}
	return held, nil
}

func (s *Store) tryLockSession(ctx context.Context, l Locker, name, owner string) (bool, error) {
	if conn, ok := s.conns[name]; ok {
		held, err := l.TryLock(ctx, conn, name)
		if err == nil && held {
			return true, nil
		}
		// The pinned session is gone, lost the lock or failed (a canceled
		// ctx, say): end it, so a live session never goes back to the pool
		// still holding the lock, and acquire afresh below.
		discard(conn)
		delete(s.conns, name)
		delete(s.held, name)
		if err != nil && ctx.Err() != nil {
			return false, err
		}
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	held, err := l.TryLock(ctx, conn, name)
	if err != nil {
		discard(conn) // the lock may have been granted before the error
		return false, err
	}
	if !held {
		_ = conn.Close() // holds nothing: back to the pool
		return false, nil
	}
	s.conns[name] = conn
	s.held[name] = owner
	return true, nil
}

// checkLock refuses (ErrInvalid) a name or owner failing TextRule or empty:
// an empty name has no row to name; an empty owner would equal the unheld
// marker of a seeded leases row.
func checkLock(name, owner string) error {
	if err := checkText("lock", "name", name, "owner", owner); err != nil {
		return err
	}
	if name == "" || owner == "" {
		return fmt.Errorf("%w: lock name and owner must not be empty", store.ErrInvalid)
	}
	return nil
}

// Unlock: see store.Locks.
func (s *Store) Unlock(ctx context.Context, name, owner string) error {
	if err := checkLock(name, owner); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Only what this handle holds is released: the leases row names an
	// owner, not a handle, so without this a handle that holds nothing
	// would release another handle's lease under the same owner.
	if cur, ok := s.held[name]; !ok || cur != owner {
		return nil
	}
	return s.unlockLocked(ctx, name, owner)
}

func (s *Store) unlockLocked(ctx context.Context, name, owner string) error {
	if l, ok := s.d.(Locker); ok {
		conn, ok := s.conns[name]
		if !ok {
			return nil
		}
		err := l.Unlock(ctx, conn)
		if err != nil {
			discard(conn) // ending the session releases the lock Unlock could not
		} else {
			_ = conn.Close() // unlocked: back to the pool
		}
		delete(s.conns, name)
		delete(s.held, name)
		return err
	}
	err := s.Tx(ctx, func(ctx context.Context, t store.Tx) error {
		_, err := t.(*tx).execW(ctx, "UPDATE leases SET expires_at = 0 WHERE name = ? AND owner = ?", name, owner)
		return err
	})
	// Forget the lease even when the UPDATE failed: the row still expires at its
	// expires_at, so the database stays correct, while a kept entry would
	// refuse every other owner on this handle until the process restarts.
	delete(s.held, name)
	return err
}

// discard ends conn's session instead of returning it to the pool:
// sql.Conn.Close alone would hand a session that may still hold an advisory
// lock to the next caller. A Raw callback returning driver.ErrBadConn makes
// database/sql close the driver connection.
func discard(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

// releaseTimeout bounds releaseAll (a variable for tests).
var releaseTimeout = 5 * time.Second

// releaseAll unlocks everything this Store holds (Close), within
// releaseTimeout: an unreachable server must not hang Close. A Postgres
// unlock cut short discards its connection, which ends the session and so
// the lock; a SQLite lease cut short expires at its until.
func (s *Store) releaseAll() {
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, owner := range s.held {
		_ = s.unlockLocked(ctx, name, owner)
		delete(s.held, name) // released or not, this Store is closing
	}
	for name, c := range s.conns {
		discard(c)
		delete(s.conns, name)
	}
}
