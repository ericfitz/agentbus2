package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/store"
	sqlitedrv "modernc.org/sqlite"
)

func TestNextSeqNeedsAppend(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, newPath(t), Options{})
	err := s.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.NextSeq(ctx)
		return err
	})
	if !errors.Is(err, store.ErrNotAppend) {
		t.Fatalf("NextSeq in Tx: err = %v, want ErrNotAppend", err)
	}
	var got []int64
	err = s.Append(ctx, func(ctx context.Context, tx store.Tx) error {
		for range 2 {
			n, err := tx.NextSeq(ctx)
			if err != nil {
				return err
			}
			got = append(got, n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("seqs = %v, want [1 2]", got)
	}
	var max int64
	err = s.Read(ctx, func(ctx context.Context, tx store.Tx) (err error) {
		max, err = tx.MaxSeq(ctx)
		return err
	})
	if err != nil || max != 2 {
		t.Fatalf("MaxSeq = %d, %v; want 2", max, err)
	}
}

func TestReadRejectsWrites(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	s := mustOpen(t, path, Options{})

	for name, write := range map[string]func(context.Context, store.Tx) error{
		"SetNotice": func(ctx context.Context, tx store.Tx) error {
			return tx.SetNotice(ctx, store.Notice{Kind: "capacity", Message: "m", SetAt: 1})
		},
		"TryLease": func(ctx context.Context, tx store.Tx) error {
			_, err := tx.TryLease(ctx, "maintenance", "a", 100, 200)
			return err
		},
		"NextSeq": func(ctx context.Context, tx store.Tx) error {
			_, err := tx.NextSeq(ctx)
			return err
		},
	} {
		err := s.Read(ctx, func(ctx context.Context, tx store.Tx) error { return write(ctx, tx) })
		if !errors.Is(err, store.ErrReadOnly) {
			t.Errorf("%s inside Read: err = %v, want ErrReadOnly", name, err)
		}
	}

	db := rawDB(t, path)
	if got := scalar[int](t, db, "SELECT count(*) FROM notices"); got != 0 {
		t.Errorf("notice rows = %d, want 0", got)
	}
	if got := scalar[string](t, db, "SELECT owner FROM leases WHERE name = 'maintenance'"); got != "" {
		t.Errorf("maintenance owner = %q, want empty", got)
	}
	if got := scalar[int](t, db, "SELECT v FROM seq_counter"); got != 0 {
		t.Errorf("seq_counter.v = %d, want 0", got)
	}

	d := dialect{}
	if d.IsReadOnlyViolation(errors.New("x")) {
		t.Error("IsReadOnlyViolation(plain error) = true")
	}
	if d.IsReadOnlyViolation(&sqlitedrv.Error{}) {
		t.Error("IsReadOnlyViolation(*sqlite.Error) = true")
	}
}

func TestBusyIsRetryable(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	opts := Options{BusyTimeout: 100 * time.Millisecond}
	holder := mustOpen(t, path, opts)
	other := mustOpen(t, path, opts)

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- holder.Append(ctx, func(ctx context.Context, tx store.Tx) error {
			if _, err := tx.NextSeq(ctx); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	calls := 0
	start := time.Now()
	err := other.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		calls++
		return tx.SetNotice(ctx, store.Notice{Kind: "k", Message: "m", SetAt: 1})
	})
	elapsed := time.Since(start)
	if !errors.Is(err, store.ErrRetryable) {
		t.Fatalf("err = %v, want ErrRetryable", err)
	}
	if calls != 0 {
		t.Fatalf("fn ran %d times, want 0 (busy came from BeginTx)", calls)
	}
	if elapsed > time.Second {
		t.Fatalf("took %v, want under a second", elapsed)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("holder: %v", err)
	}
	calls = 0
	err = other.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		calls++
		return tx.SetNotice(ctx, store.Notice{Kind: "k", Message: "m", SetAt: 1})
	})
	if err != nil || calls != 1 {
		t.Fatalf("after release: err = %v, calls = %d; want nil, 1", err, calls)
	}
}

func TestTxRetriesNothingOnSQLite(t *testing.T) {
	s := mustOpen(t, newPath(t), Options{})
	if got := s.Attempts(); got != 1 {
		t.Fatalf("Attempts = %d, want 1", got)
	}
}

func TestTxErrorsAreNotRetriedOrWrapped(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, newPath(t), Options{})
	boom := errors.New("boom")
	calls := 0
	err := s.Tx(ctx, func(context.Context, store.Tx) error { calls++; return boom })
	if !errors.Is(err, boom) || errors.Is(err, store.ErrRetryable) || calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, newPath(t), Options{})
	_ = s.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.SetNotice(ctx, store.Notice{Kind: "k", Message: "m", SetAt: 1}); err != nil {
			return err
		}
		return errors.New("abort")
	})
	var ok bool
	err := s.Read(ctx, func(ctx context.Context, tx store.Tx) (err error) {
		_, ok, err = tx.Notice(ctx, "k")
		return err
	})
	if err != nil || ok {
		t.Fatalf("Notice after rollback: ok = %v, err = %v", ok, err)
	}
}

func TestLeaseAndNotices(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, newPath(t), Options{})
	lease := func(owner string, now, until int64) bool {
		t.Helper()
		var got bool
		err := s.Tx(ctx, func(ctx context.Context, tx store.Tx) (err error) {
			got, err = tx.TryLease(ctx, "maintenance", owner, now, until)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// Plan text lists the fourth call as now=250; a's renewal expires at 300,
	// so the lease is only expired from now > 300: 350 is used here.
	for i, c := range []struct {
		owner      string
		now, until int64
		want       bool
	}{
		{"a", 100, 200, true},
		{"b", 150, 300, false},
		{"a", 150, 300, true},
		{"b", 250, 400, false},
		{"b", 350, 400, true},
	} {
		if got := lease(c.owner, c.now, c.until); got != c.want {
			t.Errorf("call %d (%s, %d, %d) = %v, want %v", i, c.owner, c.now, c.until, got, c.want)
		}
	}
	var unknown bool
	err := s.Tx(ctx, func(ctx context.Context, tx store.Tx) (err error) {
		unknown, err = tx.TryLease(ctx, "nope", "a", 1, 2)
		return err
	})
	if err != nil || unknown {
		t.Errorf("unknown lease: %v, %v; want false, nil", unknown, err)
	}

	notice := func() (store.Notice, bool) {
		t.Helper()
		var n store.Notice
		var ok bool
		err := s.Read(ctx, func(ctx context.Context, tx store.Tx) (err error) {
			n, ok, err = tx.Notice(ctx, "embedding")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return n, ok
	}
	if _, ok := notice(); ok {
		t.Fatal("notice present before set")
	}
	set := func(msg string, at int64) {
		t.Helper()
		if err := s.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			return tx.SetNotice(ctx, store.Notice{Kind: "embedding", Message: msg, SetAt: at})
		}); err != nil {
			t.Fatal(err)
		}
	}
	set("first", 10)
	if n, ok := notice(); !ok || n != (store.Notice{Kind: "embedding", Message: "first", SetAt: 10}) {
		t.Fatalf("notice = %+v, %v", n, ok)
	}
	set("second", 20)
	if n, _ := notice(); n.Message != "second" || n.SetAt != 20 {
		t.Fatalf("replaced notice = %+v", n)
	}
	if err := s.Tx(ctx, func(ctx context.Context, tx store.Tx) error { return tx.ClearNotice(ctx, "embedding") }); err != nil {
		t.Fatal(err)
	}
	if _, ok := notice(); ok {
		t.Fatal("notice present after clear")
	}
}
