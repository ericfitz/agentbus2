package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func sessionX(id, identity, hash string, opened, lease int64) ab2store.Session {
	return ab2store.Session{ID: id, Identity: identity, DisplayName: identity + "-1", Hint: "h",
		ResumeTokenHash: hash, OpenedAt: opened, LeaseExpiresAt: lease}
}

func sessionOpen(t *testing.T, s ab2store.Store, sess ab2store.Session) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.OpenSession(ctx, sess) })
}

func sessionGet(t *testing.T, s ab2store.Store, id string) ab2store.Session {
	t.Helper()
	var got ab2store.Session
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		got, err = tx.Session(ctx, id)
		return err
	})
	return got
}

func sessionIDs(ss []ab2store.Session) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}

func TestSessionOpenRoundTrip(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustIdentity(t, s, "sam")
		in := sessionX("s1", "pat", "hash1", 10, 100)
		sessionOpen(t, s, in)
		sessionOpen(t, s, sessionX("s2", "sam", "hash2", 5, 100))
		sessionOpen(t, s, sessionX("s3", "pat", "hash3", 10, 100))
		if got := sessionGet(t, s, "s1"); got != in {
			t.Errorf("Session = %+v, want %+v", got, in)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.SessionByResumeTokenHash(ctx, "hash1")
			if err != nil || got != in {
				t.Errorf("ByHash = %+v, %v", got, err)
			}
			if _, err := tx.SessionByResumeTokenHash(ctx, "nope"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("unknown hash err = %v, want ErrNotFound", err)
			}
			if _, err := tx.Session(ctx, "nope"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("unknown id err = %v, want ErrNotFound", err)
			}
			pat, err := tx.ListSessions(ctx, "pat")
			if err != nil || !equalStrings(sessionIDs(pat), []string{"s1", "s3"}) {
				t.Errorf("ListSessions(pat) = %v, %v", sessionIDs(pat), err)
			}
			all, err := tx.ListSessions(ctx, "")
			if err != nil || !equalStrings(sessionIDs(all), []string{"s2", "s1", "s3"}) {
				t.Errorf("ListSessions(\"\") = %v, %v", sessionIDs(all), err)
			}
			return nil
		})
	})
}

func TestSessionOpenRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		sessionOpen(t, s, sessionX("s1", "pat", "hash1", 1, 10))
		cases := []struct {
			name string
			sess ab2store.Session
			want error
		}{
			{"duplicate id", sessionX("s1", "pat", "other", 1, 10), ab2store.ErrConflict},
			{"duplicate hash", sessionX("s2", "pat", "hash1", 1, 10), ab2store.ErrConflict},
			{"unknown identity", sessionX("s3", "ghost", "h3", 1, 10), ab2store.ErrNotFound},
			{"empty hash", sessionX("s4", "pat", "", 1, 10), ab2store.ErrInvalid},
		}
		for _, c := range cases {
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.OpenSession(ctx, c.sess) })
			if !errors.Is(err, c.want) {
				t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
			}
		}
	})
}

func TestSessionRenewLeaseAndClose(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		sessionOpen(t, s, sessionX("s1", "pat", "hash1", 1, 10))
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.RenewLease(ctx, "s1", 50) })
		if got := sessionGet(t, s, "s1"); got.LeaseExpiresAt != 50 || got.ClosedAt != 0 {
			t.Errorf("after renew = %+v", got)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.CloseSession(ctx, "s1", 60) })
		if got := sessionGet(t, s, "s1"); got.ClosedAt != 60 {
			t.Errorf("after close = %+v", got)
		}
		for name, fn := range map[string]func(ctx context.Context, tx ab2store.Tx) error{
			"RenewLease":   func(ctx context.Context, tx ab2store.Tx) error { return tx.RenewLease(ctx, "nope", 1) },
			"CloseSession": func(ctx context.Context, tx ab2store.Tx) error { return tx.CloseSession(ctx, "nope", 1) },
			"RegisterWatch": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.RegisterWatch(ctx, "nope", "w", 1)
			},
			"SetPendingToken": func(ctx context.Context, tx ab2store.Tx) error { return tx.SetPendingToken(ctx, "nope", "t") },
			"SetTagsCursor":   func(ctx context.Context, tx ab2store.Tx) error { return tx.SetTagsCursor(ctx, "nope", 1, 2) },
		} {
			if err := s.Tx(context.Background(), fn); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("%s unknown id: err = %v, want ErrNotFound", name, err)
			}
		}
	})
}

func TestSessionRegisterWatchReplaces(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		sessionOpen(t, s, sessionX("s1", "pat", "hash1", 1, 10))
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.RegisterWatch(ctx, "s1", "w1", 20) })
		older := sessionGet(t, s, "s1").WatchID
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.RegisterWatch(ctx, "s1", "w2", 30) })
		got := sessionGet(t, s, "s1")
		if older != "w1" || got.WatchID != "w2" || got.LeaseExpiresAt != 30 {
			t.Errorf("older = %q, now = %+v; want w1 then w2 with lease 30", older, got)
		}
		if got.WatchID == older {
			t.Error("the older watch id is still current")
		}
	})
}

func TestSessionPendingTokenAndTagsCursor(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		sessionOpen(t, s, sessionX("s1", "pat", "hash1", 1, 10))
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.SetPendingToken(ctx, "s1", "batch-1"); err != nil {
				return err
			}
			return tx.SetTagsCursor(ctx, "s1", 7, 9)
		})
		got := sessionGet(t, s, "s1")
		if got.PendingToken != "batch-1" || got.TagsCursorSeq != 7 || got.TagsPendingEndSeq != 9 {
			t.Errorf("got %+v", got)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.SetPendingToken(ctx, "s1", "") })
		if got := sessionGet(t, s, "s1"); got.PendingToken != "" || got.TagsCursorSeq != 7 {
			t.Errorf("after clear = %+v", got)
		}
	})
}

func TestSessionCursorsUpsertAndDelete(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "pat", nil)
		b := mustChannel(t, s, "b", ab2store.KindOrdinary, "pat", nil)
		sessionOpen(t, s, sessionX("s1", "pat", "hash1", 1, 10))
		put := func(c ab2store.Cursor) {
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.PutCursor(ctx, c) })
		}
		list := func() []ab2store.Cursor {
			var out []ab2store.Cursor
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				out, err = tx.Cursors(ctx, "s1")
				return err
			})
			return out
		}
		put(ab2store.Cursor{SessionID: "s1", ChannelID: b.ID, CursorSeq: 4, PendingEndSeq: 0})
		put(ab2store.Cursor{SessionID: "s1", ChannelID: a.ID, CursorSeq: 2, PendingEndSeq: 3})
		want := []ab2store.Cursor{
			{SessionID: "s1", ChannelID: a.ID, CursorSeq: 2, PendingEndSeq: 3},
			{SessionID: "s1", ChannelID: b.ID, CursorSeq: 4},
		}
		if got := list(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("Cursors = %+v, want %+v", got, want)
		}
		put(ab2store.Cursor{SessionID: "s1", ChannelID: a.ID, CursorSeq: 8, PendingEndSeq: 0})
		if got := list(); len(got) != 2 || got[0].CursorSeq != 8 || got[0].PendingEndSeq != 0 {
			t.Errorf("after upsert = %+v", got)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteCursor(ctx, "s1", a.ID) })
		if got := list(); len(got) != 1 || got[0].ChannelID != b.ID {
			t.Errorf("after delete = %+v", got)
		}
		// Deleting an absent cursor is a no-op.
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteCursor(ctx, "s1", a.ID) })
		// Unknown session or channel: ErrNotFound.
		for name, c := range map[string]ab2store.Cursor{
			"session": {SessionID: "nope", ChannelID: a.ID},
			"channel": {SessionID: "s1", ChannelID: 9999},
		} {
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.PutCursor(ctx, c) })
			if !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("unknown %s: err = %v, want ErrNotFound", name, err)
			}
		}
	})
}

func TestSessionDeleteLapsedBefore(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "pat", nil)
		sessionOpen(t, s, sessionX("old1", "pat", "h1", 1, 10))
		sessionOpen(t, s, sessionX("old2", "pat", "h2", 1, 20))
		sessionOpen(t, s, sessionX("live", "pat", "h3", 1, 1000))
		for _, id := range []string{"old1", "old2", "live"} {
			c := ab2store.Cursor{SessionID: id, ChannelID: ch.ID, CursorSeq: 5}
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.PutCursor(ctx, c) })
		}
		del := func(limit int) int {
			var n int
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				n, err = tx.DeleteSessions(ctx, 100, limit)
				return err
			})
			return n
		}
		for _, limit := range []int{0, -1} {
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.DeleteSessions(ctx, 100, limit)
				return err
			})
			if !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("DeleteSessions(limit %d) err = %v, want ErrInvalid", limit, err)
			}
		}
		if a, b, c := del(1), del(1), del(1); a != 1 || b != 1 || c != 0 {
			t.Errorf("deleted = %d, %d, %d; want 1, 1, 0", a, b, c)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			ss, err := tx.ListSessions(ctx, "")
			if err != nil || !equalStrings(sessionIDs(ss), []string{"live"}) {
				t.Errorf("sessions left = %v, %v", sessionIDs(ss), err)
			}
			for id, n := range map[string]int{"old1": 0, "old2": 0, "live": 1} {
				cs, err := tx.Cursors(ctx, id)
				if err != nil || len(cs) != n {
					t.Errorf("cursors of %s = %v, %v; want %d", id, cs, err, n)
				}
			}
			return nil
		})
	})
}

func TestSessionCursorsDeletedWithChannel(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "pat", nil)
		sessionOpen(t, s, sessionX("s1", "pat", "h1", 1, 10))
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutCursor(ctx, ab2store.Cursor{SessionID: "s1", ChannelID: ch.ID, CursorSeq: 3})
		})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteChannel(ctx, ch.ID) })
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			cs, err := tx.Cursors(ctx, "s1")
			if err != nil || len(cs) != 0 {
				t.Errorf("cursors = %v, %v; want none", cs, err)
			}
			return nil
		})
	})
}

// ListSessions breaks an opened_at tie by id in byte order on both engines
// (the ids are the names that make SQLite's BINARY collation and Postgres's
// database collation disagree).
func TestSessionListIsByteOrder(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		for i, id := range byteOrderNames {
			sessionOpen(t, s, sessionX(id, "pat", "h"+itoa(int64(i)), 1, 1000))
		}
		sessionOpen(t, s, sessionX("first", "pat", "hf", 0, 1000))
		want := append([]string{"first"}, byteOrderWant...)
		for _, identity := range []string{"", "pat"} {
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				ss, err := tx.ListSessions(ctx, identity)
				if err != nil || !equalStrings(sessionIDs(ss), want) {
					t.Errorf("ListSessions(%q) = %q, %v; want %q", identity, sessionIDs(ss), err, want)
				}
				return nil
			})
		}
	})
}
