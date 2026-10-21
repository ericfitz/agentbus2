package sqlstore

import (
	"context"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// TryLock: see store.Locks. Both paths consult s.held under s.mu before any
// SQL, so a second owner on this handle never touches the row or the pinned
// connection beneath the first.
func (s *Store) TryLock(ctx context.Context, name, owner string, now, until int64) (bool, error) {
	if err := checkText("lock", "name", name, "owner", owner); err != nil {
		return false, err
	}
	// An empty name has no row to name; an empty owner would equal the
	// unheld marker of a seeded leases row.
	if name == "" || owner == "" {
		return false, fmt.Errorf("%w: lock name and owner must not be empty", store.ErrInvalid)
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
		// The pinned session is gone (or lost the lock): drop it and acquire
		// afresh below.
		_ = conn.Close()
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
	if err != nil || !held {
		_ = conn.Close()
		return false, err
	}
	s.conns[name] = conn
	s.held[name] = owner
	return true, nil
}

// Unlock: see store.Locks.
func (s *Store) Unlock(ctx context.Context, name, owner string) error {
	if badText(name, owner) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.held[name]; ok && cur != owner {
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
		_ = conn.Close() // ends the session, which releases the lock even if Unlock failed
		delete(s.conns, name)
		delete(s.held, name)
		return err
	}
	err := s.Tx(ctx, func(ctx context.Context, t store.Tx) error {
		_, err := t.(*tx).execW(ctx, "UPDATE leases SET expires_at = 0 WHERE name = ? AND owner = ?", name, owner)
		return err
	})
	if err == nil {
		delete(s.held, name)
	}
	return err
}

// releaseAll unlocks everything this Store holds (Close).
func (s *Store) releaseAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, owner := range s.held {
		_ = s.unlockLocked(context.Background(), name, owner)
	}
	for name, c := range s.conns {
		_ = c.Close()
		delete(s.conns, name)
	}
}
