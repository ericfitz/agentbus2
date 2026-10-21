package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func TestNextSeqNeedsAppend(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, _ := openStore(t, e, sqlstore.Options{})
		err := s.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.NextSeq(ctx)
			return err
		})
		if !errors.Is(err, ab2store.ErrNotAppend) {
			t.Fatalf("NextSeq in Tx: err = %v, want ErrNotAppend", err)
		}
		var got []int64
		mustAppendTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for range 2 {
				n, err := tx.NextSeq(ctx)
				if err != nil {
					return err
				}
				got = append(got, n)
			}
			return nil
		})
		if len(got) != 2 || got[0] != 1 || got[1] != 2 {
			t.Fatalf("seqs = %v, want [1 2]", got)
		}
		var max int64
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			max, err = tx.MaxSeq(ctx)
			return err
		})
		if max != 2 {
			t.Fatalf("MaxSeq = %d, want 2", max)
		}
	})
}

// TestReadRejectsWrites: on postgres this is the Go-side guard, not the 25006
// backstop, since the guard fires before the engine sees the statement.
func TestReadRejectsWrites(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		for name, write := range map[string]func(context.Context, ab2store.Tx) error{
			"SetNotice": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.SetNotice(ctx, ab2store.Notice{Kind: "capacity", Message: "m", SetAt: 1})
			},
			"TryLease": func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.TryLease(ctx, "maintenance", "a", 100, 200)
				return err
			},
			"NextSeq": func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.NextSeq(ctx)
				return err
			},
		} {
			err := s.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error { return write(ctx, tx) })
			if !errors.Is(err, ab2store.ErrReadOnly) {
				t.Errorf("%s inside Read: err = %v, want ErrReadOnly", name, err)
			}
		}
		db := rawDB(t, e, rawDSN)
		if got := scalar[int](t, db, "SELECT count(*) FROM notices"); got != 0 {
			t.Errorf("notice rows = %d, want 0", got)
		}
		if got := scalar[string](t, db, "SELECT owner FROM leases WHERE name = 'maintenance'"); got != "" {
			t.Errorf("maintenance owner = %q, want empty", got)
		}
		if got := scalar[int](t, db, "SELECT v FROM seq_counter"); got != 0 {
			t.Errorf("seq_counter.v = %d, want 0", got)
		}
	})
}

func TestLeaseAndNotices(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, _ := openStore(t, e, sqlstore.Options{})
		lease := func(name, owner string, now, until int64) bool {
			t.Helper()
			var got bool
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				got, err = tx.TryLease(ctx, name, owner, now, until)
				return err
			})
			return got
		}
		// The plan lists the fourth call as now=250; a's renewal expires at
		// 300, so the lease is only expired from now > 300: 350 is the take-over.
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
			if got := lease("maintenance", c.owner, c.now, c.until); got != c.want {
				t.Errorf("call %d (%s, %d, %d) = %v, want %v", i, c.owner, c.now, c.until, got, c.want)
			}
		}
		if lease("nope", "a", 1, 2) {
			t.Error("a lease that does not exist was taken")
		}

		notice := func() (ab2store.Notice, bool) {
			t.Helper()
			var n ab2store.Notice
			var ok bool
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				n, ok, err = tx.Notice(ctx, "embedding")
				return err
			})
			return n, ok
		}
		if _, ok := notice(); ok {
			t.Fatal("notice present before set")
		}
		set := func(msg string, at int64) {
			t.Helper()
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				return tx.SetNotice(ctx, ab2store.Notice{Kind: "embedding", Message: msg, SetAt: at})
			})
		}
		set("first", 10)
		if n, ok := notice(); !ok || n != (ab2store.Notice{Kind: "embedding", Message: "first", SetAt: 10}) {
			t.Fatalf("notice = %+v, %v", n, ok)
		}
		set("second", 20)
		if n, _ := notice(); n.Message != "second" || n.SetAt != 20 {
			t.Fatalf("replaced notice = %+v", n)
		}
		if err := s.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error { return tx.ClearNotice(ctx, "embedding") }); err != nil {
			t.Fatal(err)
		}
		if _, ok := notice(); ok {
			t.Fatal("notice present after clear")
		}
	})
}

// TestTxRetriesInjectedSerializationFailure: fn returns SQLSTATE 40001 on its
// first call and nil after. Postgres recognizes it and re-runs; the SQLite
// dialect does not recognize pgx errors, so fn runs once and the raw error
// comes back unwrapped.
func TestTxRetriesInjectedSerializationFailure(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		calls := 0
		err := s.Tx(context.Background(), func(context.Context, ab2store.Tx) error {
			calls++
			if calls == 1 {
				return &pgconn.PgError{Code: "40001"}
			}
			return nil
		})
		switch e.Name {
		case "postgres":
			if err != nil || calls != 2 {
				t.Fatalf("postgres: err = %v, calls = %d; want nil, 2", err, calls)
			}
		default:
			var pe *pgconn.PgError
			if !errors.As(err, &pe) || errors.Is(err, ab2store.ErrRetryable) || calls != 1 {
				t.Fatalf("%s: err = %v, calls = %d; want the raw PgError, unwrapped, after 1 run", e.Name, err, calls)
			}
		}
	})
}

// barrier releases its waiters once n have arrived; a wait that times out
// reports false instead of hanging the test.
type barrier struct {
	mu   sync.Mutex
	need int
	done chan struct{}
}

func newBarrier(n int) *barrier { return &barrier{need: n, done: make(chan struct{})} }

func (b *barrier) wait() bool {
	b.mu.Lock()
	b.need--
	if b.need == 0 {
		close(b.done)
	}
	b.mu.Unlock()
	select {
	case <-b.done:
		return true
	case <-time.After(15 * time.Second):
		return false
	}
}

// TestTxSerializationFailureAtCommitIsRetried: two transactions form a
// read-write cycle (A reads y and writes x; B reads x and writes y). On
// postgres two barriers, each released only on each fn's first run, hold both
// transactions after both reads (both snapshots predate both writes) and again
// after both writes and before either fn returns, so the SSI failure can only
// come from COMMIT; every write statement must return nil. On sqlite B blocks
// at BEGIN IMMEDIATE until A commits (a barrier inside fn would deadlock).
func TestTxSerializationFailureAtCommitIsRetried(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		a, reopen, _ := openStore(t, e, sqlstore.Options{})
		b, err := reopen(sqlstore.Options{})
		if err != nil {
			t.Fatal(err)
		}
		barriers := e.Name == "postgres"
		afterReads, afterWrites := newBarrier(2), newBarrier(2)

		var mu sync.Mutex
		var writeErrs []error
		var runs atomic.Int32
		var runsOf [2]atomic.Int32
		txn := func(idx int, s ab2store.Store, readKey, writeKey string) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				first := runsOf[idx].Add(1) == 1
				runs.Add(1)
				_, saw, err := tx.Notice(ctx, readKey)
				if err != nil {
					return err
				}
				if barriers && first && !afterReads.wait() {
					return errors.New("barrier after reads timed out")
				}
				werr := tx.SetNotice(ctx, ab2store.Notice{Kind: writeKey, Message: fmt.Sprintf("saw=%v", saw), SetAt: 1})
				mu.Lock()
				if first {
					writeErrs = append(writeErrs, werr)
				}
				mu.Unlock()
				if barriers && first && !afterWrites.wait() {
					return errors.New("barrier after writes timed out")
				}
				return werr
			})
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); errs[0] = txn(0, a, "y", "x") }()
		go func() { defer wg.Done(); errs[1] = txn(1, b, "x", "y") }()
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("Tx %d: %v", i, err)
			}
		}
		for i, err := range writeErrs {
			if err != nil {
				t.Fatalf("first-run write %d returned %v; the failure must come from Commit", i, err)
			}
		}
		if len(writeErrs) != 2 {
			t.Fatalf("recorded %d first-run writes, want 2", len(writeErrs))
		}
		wantRuns := int32(2)
		if barriers {
			wantRuns = 3 // exactly one loser re-ran its fn
		}
		if got := runs.Load(); got != wantRuns {
			t.Fatalf("fn ran %d times in total, want %d", got, wantRuns)
		}
		// Serial order: the later transaction saw the earlier one's write.
		var x, y ab2store.Notice
		mustRead(t, a, func(ctx context.Context, tx ab2store.Tx) error {
			var err error
			if x, _, err = tx.Notice(ctx, "x"); err != nil {
				return err
			}
			y, _, err = tx.Notice(ctx, "y")
			return err
		})
		if x.Message == "saw=false" && y.Message == "saw=false" {
			t.Fatalf("x = %q, y = %q: both ran blind, no serial order explains that", x.Message, y.Message)
		}
		if x.Message == "" || y.Message == "" {
			t.Fatalf("x = %q, y = %q: a write is missing", x.Message, y.Message)
		}
	})
}

func TestTxGivesUpAfterAttempts(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		// pending Eric: Gate 0 item 3
		wantDefault := 1
		if e.Name == "postgres" {
			wantDefault = 5
		}
		if got := s.(*sqlstore.Store).Attempts(); got != wantDefault {
			t.Errorf("default Attempts = %d, want %d", got, wantDefault)
		}

		s2, _, _ := openStore(t, e, sqlstore.Options{TxAttempts: 2})
		calls := 0
		err := s2.Tx(context.Background(), func(context.Context, ab2store.Tx) error {
			calls++
			return &pgconn.PgError{Code: "40001"}
		})
		if e.Name == "postgres" {
			if !errors.Is(err, ab2store.ErrRetryable) || calls != 2 {
				t.Fatalf("postgres: err = %v, calls = %d; want ErrRetryable after 2 runs", err, calls)
			}
			return
		}
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || errors.Is(err, ab2store.ErrRetryable) || calls != 1 {
			t.Fatalf("sqlite: err = %v, calls = %d; want the raw error after 1 run", err, calls)
		}
	})
}

func TestTxNonRetryableErrorPassesThrough(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		boom := errors.New("boom")
		calls := 0
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			calls++
			if err := tx.SetNotice(ctx, ab2store.Notice{Kind: "k", Message: "m", SetAt: 1}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) || errors.Is(err, ab2store.ErrRetryable) || calls != 1 {
			t.Fatalf("err = %v, calls = %d; want boom, unwrapped, after 1 run", err, calls)
		}
		var ok bool
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			_, ok, err = tx.Notice(ctx, "k")
			return err
		})
		if ok {
			t.Fatal("a rolled back write is visible")
		}
	})
}
