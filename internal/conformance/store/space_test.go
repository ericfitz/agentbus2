package store

import (
	"context"
	"database/sql"
	"math"
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
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		before := mustUsage(t, s)
		appendKiB(t, s, 200)
		after := mustUsage(t, s)
		if after.Bytes <= before.Bytes {
			t.Fatalf("Usage.Bytes %d -> %d, want growth", before.Bytes, after.Bytes)
		}
		if want := e.Name == "postgres"; after.Logical != want || before.Logical != want {
			t.Fatalf("Logical = %v, want %v", after.Logical, want)
		}
		if after.DiskBytes <= 0 {
			t.Fatalf("DiskBytes = %d, want > 0", after.DiskBytes)
		}
		if e.Name == "sqlite" && after.DiskBytes < after.Bytes {
			t.Fatalf("DiskBytes %d < Bytes %d on sqlite", after.DiskBytes, after.Bytes)
		}
		if e.Name == "postgres" {
			// The growth is the 200 messages plus the channel and its ACL rows
			// appendKiB creates: Bytes is the raw sum(size_bytes) over every
			// measured table, and the messages alone account for their share.
			db := rawDB(t, e, rawDSN)
			parts := make([]string, 0, len(sqlstore.MeasuredTables))
			for _, tbl := range sqlstore.MeasuredTables {
				parts = append(parts, "(SELECT coalesce(sum(size_bytes), 0) FROM "+tbl+")")
			}
			if want := scalar[int64](t, db, "SELECT ("+strings.Join(parts, " + ")+")::bigint"); after.Bytes != want {
				t.Fatalf("Bytes = %d, raw sum(size_bytes) over the measured tables is %d", after.Bytes, want)
			}
			msgs := scalar[int64](t, db, "SELECT coalesce(sum(size_bytes), 0)::bigint FROM messages")
			if got := after.Bytes - before.Bytes; got < msgs {
				t.Fatalf("Bytes grew by %d, less than the %d bytes of the appended messages", got, msgs)
			}
		}
	})
}

func TestUsageIgnoresBloat(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		var db *sql.DB
		if e.Name == "postgres" {
			db = rawDB(t, e, rawDSN)
			if _, err := db.Exec("ALTER TABLE messages SET (autovacuum_enabled = false, toast.autovacuum_enabled = false)"); err != nil {
				t.Fatalf("disable autovacuum: %v", err)
			}
		}
		appendKiB(t, s, 2000)
		u0 := mustUsage(t, s)
		var d int64
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			refs, err := tx.OrdinaryMessagesBefore(ctx, math.MaxInt64, 1000)
			if err != nil {
				return err
			}
			if len(refs) != 1000 {
				t.Fatalf("OrdinaryMessagesBefore returned %d rows, want 1000", len(refs))
			}
			seqs := make([]int64, len(refs))
			for i, r := range refs {
				seqs[i] = r.Seq
				d += r.Bytes
			}
			return tx.DeleteMessages(ctx, seqs)
		})
		u1 := mustUsage(t, s)
		if e.Name == "sqlite" {
			if u1.Bytes >= u0.Bytes {
				t.Fatalf("sqlite Bytes %d -> %d, want a fall", u0.Bytes, u1.Bytes)
			}
			if u1.DiskBytes != u0.DiskBytes {
				t.Fatalf("sqlite DiskBytes %d -> %d, want unchanged before Reclaim", u0.DiskBytes, u1.DiskBytes)
			}
			return
		}
		if u1.Bytes != u0.Bytes-d {
			t.Fatalf("Bytes %d, want %d - %d = %d", u1.Bytes, u0.Bytes, d, u0.Bytes-d)
		}
		if u1.DiskBytes < u0.DiskBytes {
			t.Fatalf("DiskBytes fell %d -> %d before vacuum", u0.DiskBytes, u1.DiskBytes)
		}
		if _, err := db.Exec("VACUUM messages"); err != nil {
			t.Fatalf("VACUUM: %v", err)
		}
		u2 := mustUsage(t, s)
		if u2.Bytes != u1.Bytes {
			t.Fatalf("Bytes after vacuum %d, want %d", u2.Bytes, u1.Bytes)
		}
		if u2.DiskBytes < u1.DiskBytes {
			t.Fatalf("DiskBytes after vacuum %d < %d", u2.DiskBytes, u1.DiskBytes)
		}
	})
}

// BenchmarkUsageLogical times Usage on Postgres with 2,000,000 rows of 1 KiB
// and logs the plan of the logical sum. It asserts nothing about the plan (the
// planner's choice depends on the visibility map) and is not part of verify.
func BenchmarkUsageLogical(b *testing.B) {
	e := dbtest.Engine{Name: "postgres", Driver: "pgx"}
	s, _, rawDSN := openStore(b, e, sqlstore.Options{})
	db := rawDB(b, e, rawDSN)
	if _, err := db.Exec("INSERT INTO channels (name, kind, creator, created_seq, created_at) VALUES ('bulk', 'ordinary', '', 0, 0)"); err != nil {
		b.Fatalf("channel: %v", err)
	}
	const rows, batch = 2_000_000, 50_000
	for from := 1; from <= rows; from += batch {
		if _, err := db.Exec(`INSERT INTO messages (seq, channel_id, sender, created_at, content)
			SELECT g, (SELECT id FROM channels WHERE name = 'bulk'), 'pat', 0, repeat('x', 1024) FROM generate_series($1::bigint, $2::bigint) g`,
			from, min(from+batch-1, rows)); err != nil {
			b.Fatalf("fill: %v", err)
		}
	}
	if _, err := db.Exec("VACUUM ANALYZE messages"); err != nil {
		b.Fatalf("vacuum: %v", err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := s.Usage(context.Background()); err != nil {
			b.Fatalf("Usage: %v", err)
		}
	}
	b.StopTimer()
	rs, err := db.Query("EXPLAIN SELECT coalesce(sum(size_bytes), 0) FROM messages")
	if err != nil {
		b.Fatalf("explain: %v", err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		var line string
		if err := rs.Scan(&line); err != nil {
			b.Fatal(err)
		}
		b.Log(line)
	}
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
