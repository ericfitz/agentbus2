package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func putJTI(s ab2store.Store, jti string, exp int64) error {
	return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		return tx.PutAssertionJTI(ctx, jti, exp)
	})
}

func deleteJTIBefore(t *testing.T, s ab2store.Store, now int64) int {
	t.Helper()
	var n int
	if err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		n, err = tx.DeleteAssertionJTIBefore(ctx, now)
		return err
	}); err != nil {
		t.Fatalf("DeleteAssertionJTIBefore(%d): %v", now, err)
	}
	return n
}

func TestReplayPutAndConflict(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		if err := putJTI(s, "a", 1000); err != nil {
			t.Fatalf("first put: %v", err)
		}
		if err := putJTI(s, "a", 1000); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("second put: %v, want ErrConflict", err)
		}
		if err := putJTI(s, "a", 99999); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("second put, later expiry: %v, want ErrConflict", err)
		}
		if err := putJTI(s, "b", 1000); err != nil {
			t.Errorf("other jti: %v", err)
		}
		for name, c := range map[string]struct {
			jti string
			exp int64
		}{
			"empty jti":     {"", 1000},
			"zero expiry":   {"c", 0},
			"negative":      {"c", -5},
			"NUL in jti":    {"a\x00b", 1000},
			"invalid UTF-8": {"\xff", 1000},
		} {
			if err := putJTI(s, c.jti, c.exp); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("%s: %v, want ErrInvalid", name, err)
			}
		}
		err := s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutAssertionJTI(ctx, "r", 1000)
		})
		if !errors.Is(err, ab2store.ErrReadOnly) {
			t.Errorf("Read put: %v, want ErrReadOnly", err)
		}
		err = s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.DeleteAssertionJTIBefore(ctx, 5)
			return err
		})
		if !errors.Is(err, ab2store.ErrReadOnly) {
			t.Errorf("Read delete: %v, want ErrReadOnly", err)
		}
	})
}

// TestReplayPutConcurrent: eight writers over two Store handles (two
// processes' worth of connections) put one jti; the primary key lets exactly
// one commit.
func TestReplayPutConcurrent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s1, reopen, rawDSN := openStore(t, e, sqlstore.Options{})
		s2, err := reopen(sqlstore.Options{})
		if err != nil {
			t.Fatalf("second handle: %v", err)
		}
		t.Cleanup(func() { _ = s2.Close() })
		handles := []ab2store.Store{s1, s2}
		const n = 8
		var wg sync.WaitGroup
		errs := make([]error, n)
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = putJTI(handles[i%2], "same", 5000)
			}()
		}
		close(start)
		wg.Wait()
		wins := 0
		for i, err := range errs {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ab2store.ErrConflict):
			default:
				t.Errorf("put %d: %v", i, err)
			}
		}
		if wins != 1 {
			t.Fatalf("%d puts succeeded, want exactly 1 (errs %v)", wins, errs)
		}
		if got := scalar[int](t, rawDB(t, e, rawDSN), "SELECT count(*) FROM assertion_jti"); got != 1 {
			t.Fatalf("assertion_jti rows = %d, want 1", got)
		}
		// The other handle refuses what the first accepted.
		if err := putJTI(s2, "other", 5000); err != nil {
			t.Fatal(err)
		}
		if err := putJTI(s1, "other", 5000); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("cross-handle replay: %v, want ErrConflict", err)
		}
	})
}

func TestReplayExpiry(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		for jti, exp := range map[string]int64{"x100": 100, "x200": 200, "x300": 300} {
			if err := putJTI(s, jti, exp); err != nil {
				t.Fatal(err)
			}
		}
		if n := deleteJTIBefore(t, s, 250); n != 2 {
			t.Fatalf("delete before 250 = %d, want 2", n)
		}
		if err := putJTI(s, "x300", 300); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("surviving row: %v, want ErrConflict", err)
		}
		if n := deleteJTIBefore(t, s, 250); n != 0 {
			t.Errorf("second delete = %d, want 0", n)
		}
		// expires_at < now is strict: a row at exactly now stays.
		if n := deleteJTIBefore(t, s, 300); n != 0 {
			t.Errorf("delete before 300 = %d, want 0 (the row at 300 stays)", n)
		}
		// An expired-and-deleted jti is a new assertion: the bound the docs
		// state (a replay after expiry plus leeway).
		if err := putJTI(s, "x100", 400); err != nil {
			t.Errorf("put after expiry: %v", err)
		}
	})
}
