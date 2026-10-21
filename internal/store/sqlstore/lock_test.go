package sqlstore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // the driver behind the pinned connection
)

// hangingLocker is a Locker whose Unlock never answers until its context
// ends: a Postgres server that became unreachable while the lock was held.
type hangingLocker struct{ Dialect }

func (hangingLocker) TryLock(context.Context, *sql.Conn, string) (bool, error) { return true, nil }

func (hangingLocker) Unlock(ctx context.Context, _ *sql.Conn) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestCloseReleasesLocksWithinADeadline(t *testing.T) {
	old := releaseTimeout
	releaseTimeout = 100 * time.Millisecond
	t.Cleanup(func() { releaseTimeout = old })
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, d: hangingLocker{}, held: map[string]string{}, conns: map[string]*sql.Conn{}}
	if held, err := s.TryLock(context.Background(), "maintenance", "a", 1, 2); err != nil || !held {
		t.Fatalf("TryLock = %v, %v; want true", held, err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close still waiting on Unlock after 5 s")
	}
	if len(s.held) != 0 || len(s.conns) != 0 {
		t.Fatalf("after Close held = %v, conns = %d; want both empty", s.held, len(s.conns))
	}
}
