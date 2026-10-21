package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func mustUsage(t *testing.T, s ab2store.Store) ab2store.Usage {
	t.Helper()
	u, err := s.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	return u
}

func appendKiB(t *testing.T, s ab2store.Store, n int) []int64 {
	t.Helper()
	ch := mustChannel(t, s, "bulk", ab2store.KindOrdinary, "", nil)
	body := strings.Repeat("x", 1024)
	seqs := make([]int64, 0, n)
	for range n {
		seqs = append(seqs, mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", Content: body}))
	}
	return seqs
}

func TestUsageGrowsWithData(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		before := mustUsage(t, s)
		appendKiB(t, s, 200)
		after := mustUsage(t, s)
		if after.Bytes <= before.Bytes {
			t.Fatalf("Usage.Bytes %d -> %d, want growth", before.Bytes, after.Bytes)
		}
		if want := e.Name == "sqlite"; after.Evictable != want || before.Evictable != want {
			t.Fatalf("Evictable = %v, want %v", after.Evictable, want)
		}
	})
}

func TestReclaimRunsOutsideTransactions(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		seqs := appendKiB(t, s, 200)
		peak := mustUsage(t, s)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteMessages(ctx, seqs) })
		var db *sql.DB
		var pagesBefore, freeBefore int
		if e.Name == "sqlite" {
			db = rawDB(t, e, rawDSN)
			pagesBefore = scalar[int](t, db, "PRAGMA page_count")
			freeBefore = scalar[int](t, db, "PRAGMA freelist_count")
			if freeBefore == 0 {
				t.Fatal("precondition: no free pages after the delete")
			}
		}
		if err := s.Reclaim(context.Background()); err != nil {
			t.Fatalf("Reclaim: %v", err)
		}
		if e.Name == "sqlite" {
			// Reclaim truncates up to 256 free pages from the file.
			want := min(freeBefore, 256)
			if got := pagesBefore - scalar[int](t, db, "PRAGMA page_count"); got != want {
				t.Fatalf("Reclaim removed %d pages, want %d (freelist was %d)", got, want, freeBefore)
			}
			// The freelist subtraction lowers Bytes even before pages are truncated.
			if got := mustUsage(t, s); got.Bytes >= peak.Bytes {
				t.Fatalf("Usage.Bytes after delete+Reclaim = %d, peak %d", got.Bytes, peak.Bytes)
			}
		}
	})
}
