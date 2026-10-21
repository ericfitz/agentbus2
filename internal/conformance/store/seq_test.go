package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// TestSeqConcurrentWriters is the Decision 54 concurrent-writer test: 8
// writers over their own stores, 50 appends each. The messages aggregate
// lands in Task 6, so each append marks its commit with SetNotice("seq-<v>")
// in the same Append transaction as NextSeq (Task 12 switches this to
// AppendMessage). A reader polls back to back inside one Read per poll: a
// snapshot that shows counter value m shows every lower seq's marker, because
// each lower value's transaction held the counter row earlier and committed
// earlier (a Postgres sequence would not give this, Decision 27).
func TestSeqConcurrentWriters(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		const writers, perWriter = 8, 50
		const total = writers * perWriter

		first, reopen, _ := openStore(t, e, sqlstore.Options{})
		stores := []ab2store.Store{first}
		for range writers - 1 {
			s, err := reopen(sqlstore.Options{})
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			stores = append(stores, s)
		}
		reader, err := reopen(sqlstore.Options{})
		if err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var readerErr error
		midRun := 0
		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			var last int64
			for last < total && ctx.Err() == nil {
				var m int64
				err := reader.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
					var err error
					if m, err = tx.MaxSeq(ctx); err != nil {
						return err
					}
					for k := int64(1); k <= m; k++ {
						if _, ok, err := tx.Notice(ctx, fmt.Sprintf("seq-%d", k)); err != nil {
							return err
						} else if !ok {
							return fmt.Errorf("snapshot shows MaxSeq %d but marker seq-%d is missing", m, k)
						}
					}
					return nil
				})
				if err != nil {
					if ctx.Err() == nil {
						readerErr = err
					}
					return
				}
				if m < last {
					readerErr = fmt.Errorf("MaxSeq went backwards: %d after %d", m, last)
					return
				}
				last = m
				if m > 0 && m < total {
					midRun++
				}
			}
		}()

		var wg sync.WaitGroup
		errs := make([]error, writers)
		for w, s := range stores {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range perWriter {
					err := s.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
						v, err := tx.NextSeq(ctx)
						if err != nil {
							return err
						}
						return tx.SetNotice(ctx, ab2store.Notice{Kind: fmt.Sprintf("seq-%d", v), Message: "m", SetAt: v})
					})
					if err != nil {
						errs[w] = err
						return
					}
				}
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			cancel() // a writer failed: the reader would wait for a total that never arrives
			<-readerDone
			t.Fatalf("writers: %v", err)
		}
		<-readerDone
		elapsed := time.Since(start)

		if readerErr != nil {
			t.Fatalf("reader: %v", readerErr)
		}
		if midRun < 5 {
			t.Errorf("reader saw %d mid-run snapshots (0 < MaxSeq < %d), want at least 5", midRun, total)
		}
		var max int64
		mustRead(t, first, func(ctx context.Context, tx ab2store.Tx) (err error) {
			max, err = tx.MaxSeq(ctx)
			return err
		})
		if max != total {
			t.Errorf("MaxSeq = %d, want %d", max, total)
		}
		if e.Name == "postgres" && elapsed > 30*time.Second {
			t.Errorf("run took %v, want under 30 s", elapsed)
		}
	})
}
