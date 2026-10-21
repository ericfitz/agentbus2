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
// writers over their own stores, 50 AppendMessage calls each, spread over
// three channels. A reader polls back to back, one Read per poll: m :=
// MaxSeq, then MessagesAfter(channel, 0, 1000) for each channel. The union of
// the seqs returned must be exactly 1..m (contiguous, nothing above m) and
// each channel's list ascending: a snapshot that shows counter value m shows
// every lower seq's row, because each lower value's transaction held the
// counter row earlier and committed earlier (a Postgres sequence would not
// give this, Decision 27).
func TestSeqConcurrentWriters(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		const writers, perWriter = 8, 50
		const total = writers * perWriter

		first, reopen, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, first, "w")
		channels := make([]int64, 3)
		for i := range channels {
			channels[i] = mustChannel(t, first, fmt.Sprintf("c%d", i), ab2store.KindOrdinary, "w", nil).ID
		}
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
				seen := map[int64]bool{}
				err := reader.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
					clear(seen)
					var err error
					if m, err = tx.MaxSeq(ctx); err != nil {
						return err
					}
					for _, ch := range channels {
						msgs, err := tx.MessagesAfter(ctx, ch, 0, ab2store.MessageFilter{}, 1000)
						if err != nil {
							return err
						}
						var prev int64
						for _, msg := range msgs {
							if msg.Seq <= prev {
								return fmt.Errorf("channel %d not ascending: %d after %d", ch, msg.Seq, prev)
							}
							prev = msg.Seq
							seen[msg.Seq] = true
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
				if int64(len(seen)) != m {
					readerErr = fmt.Errorf("MaxSeq %d but %d messages visible", m, len(seen))
					return
				}
				for k := int64(1); k <= m; k++ {
					if !seen[k] {
						readerErr = fmt.Errorf("snapshot shows MaxSeq %d but seq %d is missing", m, k)
						return
					}
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
				for i := range perWriter {
					err := s.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
						_, err := tx.AppendMessage(ctx, ab2store.NewMessage{
							ChannelID: channels[(w+i)%len(channels)], Sender: "w", SessionID: "s",
							CreatedAt: fixedNow, Type: "text", Content: "m",
						})
						return err
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
