package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func mustTryLock(t *testing.T, s ab2store.Store, name, owner string, now, until int64, want bool) {
	t.Helper()
	got, err := s.TryLock(context.Background(), name, owner, now, until)
	if err != nil {
		t.Fatalf("TryLock(%q, %q): %v", name, owner, err)
	}
	if got != want {
		t.Fatalf("TryLock(%q, %q, %d, %d) = %v, want %v", name, owner, now, until, got, want)
	}
}

func mustUnlock(t *testing.T, s ab2store.Store, name, owner string) {
	t.Helper()
	if err := s.Unlock(context.Background(), name, owner); err != nil {
		t.Fatalf("Unlock(%q, %q): %v", name, owner, err)
	}
}

// twoHandles opens two Stores over one database: two processes.
func twoHandles(t *testing.T, e dbtest.Engine) (s1, s2 ab2store.Store, rawDSN func() string) {
	t.Helper()
	s1, reopen, rawDSN := openStore(t, e, sqlstore.Options{})
	s2, err := reopen(sqlstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s1, s2, rawDSN
}

func TestLockExclusiveBetweenHandles(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s1, s2, _ := twoHandles(t, e)
		mustTryLock(t, s1, "maintenance", "a", 100, 200, true)
		mustTryLock(t, s2, "maintenance", "b", 150, 300, false)
		mustTryLock(t, s1, "maintenance", "a", 150, 300, true) // renew or re-assert
		mustUnlock(t, s1, "maintenance", "a")
		mustTryLock(t, s2, "maintenance", "b", 160, 400, true)
	})
}

func TestLockExpiryIsSQLiteOnly(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s1, s2, _ := twoHandles(t, e)
		mustTryLock(t, s1, "maintenance", "a", 100, 200, true)
		// The one documented engine difference (choice 1): a SQLite lease
		// expires by the clock; a Postgres session lock has no clock and
		// lasts until its session ends.
		mustTryLock(t, s2, "maintenance", "b", 250, 400, e.Name == "sqlite")
	})
}

func TestLockOtherNames(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustTryLock(t, s, "embeddings", "a", 100, 200, true) // a name the schema never seeded
		mustUnlock(t, s, "nobody-holds-this", "a")
		for _, c := range []struct{ name, owner string }{
			{"", "a"}, {"x", ""}, {"x", "a\x00b"}, {"x\x00", "a"}, {"x", "\xff"}, {"\xff", "a"},
		} {
			held, err := s.TryLock(context.Background(), c.name, c.owner, 1, 2)
			if !errors.Is(err, ab2store.ErrInvalid) || held {
				t.Errorf("TryLock(%q, %q) = %v, %v; want ErrInvalid", c.name, c.owner, held, err)
			}
		}
	})
}

func TestLockReleasedOnClose(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s1, s2, _ := twoHandles(t, e)
		mustTryLock(t, s1, "maintenance", "a", 100, 1000, true)
		mustTryLock(t, s2, "maintenance", "b", 110, 1000, false)
		if err := s1.Close(); err != nil {
			t.Fatal(err)
		}
		mustTryLock(t, s2, "maintenance", "b", 120, 1000, true)
	})
}

func TestLockUnlockByOtherOwnerIsNoop(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s1, s2, _ := twoHandles(t, e)
		mustTryLock(t, s1, "maintenance", "a", 100, 1000, true)
		mustUnlock(t, s1, "maintenance", "b")
		mustUnlock(t, s2, "maintenance", "b") // a handle that holds nothing
		mustTryLock(t, s2, "maintenance", "b", 110, 1000, false)
		mustTryLock(t, s1, "maintenance", "a", 120, 1000, true)
	})
}

func TestLockSameHandleTwoOwners(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		mustTryLock(t, s, "maintenance", "a", 100, 1000, true)
		mustTryLock(t, s, "maintenance", "b", 110, 1000, false)
		db := rawDB(t, e, rawDSN)
		if e.Name == "postgres" {
			n := scalar[int](t, db, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND granted AND classid = hashtext(current_schema())::oid AND objid = hashtext('maintenance')::oid")
			if n != 1 {
				t.Fatalf("granted advisory locks = %d, want 1", n)
			}
		} else if o := scalar[string](t, db, "SELECT owner FROM leases WHERE name = 'maintenance'"); o != "a" {
			t.Fatalf("lease owner = %q, want a", o)
		}
		mustUnlock(t, s, "maintenance", "a")
		mustTryLock(t, s, "maintenance", "b", 120, 1000, true)
	})
}

func TestLockHolderSurvivesLostHold(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s1, s2, rawDSN := twoHandles(t, e)
		mustTryLock(t, s1, "maintenance", "a", 100, 1000, true)
		db := rawDB(t, e, rawDSN)
		// The two engines lose a hold in different ways: Postgres, the
		// server ends the pinned session; SQLite, the lease row expires.
		if e.Name == "postgres" {
			const q = "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND classid = hashtext(current_schema())::oid AND objid = hashtext('maintenance')::oid"
			if _, err := db.Exec("SELECT pg_terminate_backend(l.pid) FROM pg_locks l WHERE l.locktype = 'advisory' AND l.classid = hashtext(current_schema())::oid AND l.objid = hashtext('maintenance')::oid AND l.pid <> pg_backend_pid()"); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for scalar[int](t, db, q) != 0 { // the terminated backend releases its locks asynchronously
				if time.Now().After(deadline) {
					t.Fatal("advisory lock still held after pg_terminate_backend")
				}
				time.Sleep(10 * time.Millisecond)
			}
		} else if _, err := db.Exec("UPDATE leases SET expires_at = 0 WHERE name = 'maintenance'"); err != nil {
			t.Fatal(err)
		}
		mustTryLock(t, s1, "maintenance", "a", 200, 1000, true)
		mustTryLock(t, s2, "maintenance", "b", 210, 1000, false)
	})
}

func TestLockFailedCallsNeverLeakTheSession(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		for _, c := range []struct {
			name string
			call func(s ab2store.Store) error
		}{
			{"unlock", func(s ab2store.Store) error { return s.Unlock(canceled, "maintenance", "a") }},
			{"re-assert", func(s ab2store.Store) error {
				_, err := s.TryLock(canceled, "maintenance", "a", 150, 1000)
				return err
			}},
		} {
			t.Run(c.name, func(t *testing.T) {
				s1, s2, _ := twoHandles(t, e)
				mustTryLock(t, s1, "maintenance", "a", 100, 1000, true)
				if err := c.call(s1); err == nil {
					t.Fatal("call with a canceled context returned nil")
				}
				if e.Name == "postgres" {
					// A failed call drops the pinned session; it must end the
					// session, not hand it (and its advisory lock) back to the pool.
					mustTryLock(t, s2, "maintenance", "b", 160, 1000, true)
					return
				}
				// SQLite: the row is untouched; a still holds until it expires.
				mustTryLock(t, s2, "maintenance", "b", 160, 1000, false)
				mustTryLock(t, s1, "maintenance", "a", 170, 1000, true)
			})
		}
	})
}
