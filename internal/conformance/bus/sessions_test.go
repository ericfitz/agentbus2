package bus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/sessions"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// sessionSubscribe subscribes identity to ch at acked through the Store.
func sessionSubscribe(h *harness, identity string, ch ab2store.Channel, acked int64) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		return tx.Subscribe(ctx, identity, ch.ID, acked, h.core.NowMs())
	})
}

func sessionRows(h *harness, identity string) []ab2store.Session {
	h.t.Helper()
	var rows []ab2store.Session
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		rows, err = tx.ListSessions(ctx, identity)
		return err
	})
	return rows
}

func sessionCursors(h *harness, id string) map[int64]ab2store.Cursor {
	h.t.Helper()
	out := map[int64]ab2store.Cursor{}
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		cs, err := tx.Cursors(ctx, id)
		for _, c := range cs {
			out[c.ChannelID] = c
		}
		return err
	})
	return out
}

func sessionPending(s ab2bus.Session, channel string) (int64, bool) {
	for _, p := range s.Pending {
		if p.Channel == channel {
			return p.Pending, true
		}
	}
	return 0, false
}

// sessionLapse advances the clock past the lease and the skew.
func sessionLapse(h *harness) {
	h.clock.Advance(time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds+1) * time.Second)
}

func TestSessionOpenAssignsNamesAndToken(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("work", ab2store.KindOrdinary, "", nil)
		h.append(ch, "kim", "", "one")
		h.append(ch, "kim", "", "two")
		h.append(ch, "kim", "", "three")
		quiet := h.channel("quiet", ab2store.KindOrdinary, "", nil)
		b := h.as("pat")
		sessionSubscribe(h, "pat", ch, 1)
		sessionSubscribe(h, "pat", quiet, 0)

		s1, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		s2, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		s3, err := b.Open(ctx, ab2bus.OpenSessionInput{Hint: "impl"})
		if err != nil {
			t.Fatal(err)
		}
		for i, got := range []ab2bus.Session{s1, s2, s3} {
			want := []string{"pat", "pat-2", "pat/impl"}[i]
			if got.DisplayName != want || got.Identity != "pat" || got.Resumed || got.ID == "" {
				t.Fatalf("session %d = %+v, want display name %q, identity pat, not resumed", i, got, want)
			}
			if len(got.ResumeToken) != 43 {
				t.Fatalf("session %d token %q has %d characters, want 43", i, got.ResumeToken, len(got.ResumeToken))
			}
			if n, ok := sessionPending(got, "work"); !ok || n != 2 {
				t.Fatalf("session %d pending on work = %d, %v; want 2 (messages above acked 1)", i, n, ok)
			}
			if n, ok := sessionPending(got, "quiet"); !ok || n != 0 {
				t.Fatalf("session %d pending on quiet = %d, %v; want 0, listed", i, n, ok)
			}
		}
		if s1.ID == s2.ID || s2.ID == s3.ID || s1.ResumeToken == s2.ResumeToken || s2.ResumeToken == s3.ResumeToken {
			t.Fatal("session ids and tokens must be distinct")
		}
		// The row stores only the token's SHA-256.
		sum := sha256.Sum256([]byte(s1.ResumeToken))
		found := false
		for _, r := range sessionRows(h, "pat") {
			if r.ID == s1.ID {
				found = true
				if r.ResumeTokenHash != hex.EncodeToString(sum[:]) {
					t.Fatalf("stored hash %q is not the SHA-256 hex of the token", r.ResumeTokenHash)
				}
			}
		}
		if !found {
			t.Fatal("session row not found")
		}
	})
}

func TestSessionOpenRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		_, err := newBus(h, "ghost").Open(ctx, ab2bus.OpenSessionInput{})
		codeOf(t, err, ab2bus.CodeNotRegistered)
		b := h.as("pat")
		for name, in := range map[string]ab2bus.OpenSessionInput{
			"slash in hint":   {Hint: "a/b"},
			"hint too long":   {Hint: strings.Repeat("x", 129)},
			"malformed token": {ResumeToken: "not a token"},
			"short token":     {ResumeToken: "abc"},
			"negative cursor": {Cursors: []ab2bus.OfferedCursor{{Channel: "general", Seq: -1}}},
			"duplicate cursor": {Cursors: []ab2bus.OfferedCursor{
				{Channel: "general", Seq: 1}, {Channel: "general", Seq: 2}}},
		} {
			_, err := b.Open(ctx, in)
			if ab2bus.CodeOf(err) != ab2bus.CodeValidation {
				t.Fatalf("%s: code %q (err %v), want validation", name, ab2bus.CodeOf(err), err)
			}
		}
		if rows := sessionRows(h, "pat"); len(rows) != 0 {
			t.Fatalf("rejected opens left %d session rows", len(rows))
		}
	})
}

func TestSessionStartsFromAckedMark(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ch := h.channel("work", ab2store.KindOrdinary, "", nil)
		for i := 0; i < 6; i++ {
			h.append(ch, "kim", "", "m")
		}
		b := h.as("pat")
		sessionSubscribe(h, "pat", ch, 0)
		h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.AdvanceAckedMark(ctx, "pat", ch.ID, 5) })
		s, err := b.Open(context.Background(), ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		cur := sessionCursors(h, s.ID)
		if c, ok := cur[ch.ID]; !ok || c.CursorSeq != 5 {
			t.Fatalf("cursor = %+v, %v; want seq 5 (Decision 75)", c, ok)
		}
	})
}

func TestSessionOfferedCursors(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.ensureIdentity("kim")
		mk := func(name string, acl []ab2store.ACLEntry) ab2store.Channel {
			ch := h.channel(name, ab2store.KindOrdinary, "", acl)
			for i := 0; i < 3; i++ {
				h.append(ch, "kim", "", "m")
			}
			return ch
		}
		ok := mk("ok", nil)
		old := mk("old", nil)
		hidden := mk("hidden", []ab2store.ACLEntry{{Identity: "kim", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}}})
		unsub := mk("unsub", nil)
		ahead := mk("ahead", nil)
		h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.SetEvictedBefore(ctx, old.ID, 10) })
		b := h.as("pat")
		for _, ch := range []ab2store.Channel{ok, old, hidden, ahead} {
			sessionSubscribe(h, "pat", ch, 2)
		}
		s, err := b.Open(ctx, ab2bus.OpenSessionInput{Cursors: []ab2bus.OfferedCursor{
			{Channel: "ok", Seq: 3},
			{Channel: "old", Seq: 5},
			{Channel: "hidden", Seq: 3},
			{Channel: "unsub", Seq: 3},
			{Channel: "nosuch", Seq: 3},
			{Channel: "ahead", Seq: 99999},
		}})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, r := range s.RejectedCursors {
			got[r.Channel] = r.Reason
		}
		want := map[string]string{"old": "before_retention", "hidden": "unreadable", "unsub": "unsubscribed", "nosuch": "unsubscribed", "ahead": "ahead"}
		if len(got) != len(want) {
			t.Fatalf("rejected = %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("rejected = %v, want %v", got, want)
			}
		}
		cur := sessionCursors(h, s.ID)
		if cur[ok.ID].CursorSeq != 3 {
			t.Fatalf("honored cursor = %d, want the offered 3", cur[ok.ID].CursorSeq)
		}
		for _, ch := range []ab2store.Channel{old, hidden, ahead} {
			if cur[ch.ID].CursorSeq != 2 {
				t.Fatalf("rejected cursor on %s = %d, want the server's 2", ch.Name, cur[ch.ID].CursorSeq)
			}
		}
		if _, has := cur[unsub.ID]; has {
			t.Fatal("an unsubscribed channel got a cursor")
		}
		if _, listed := sessionPending(s, "hidden"); listed {
			t.Fatal("pending lists a channel the identity cannot read")
		}
	})
}

func TestSessionResumeRestores(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("work", ab2store.KindOrdinary, "", nil)
		b := h.as("pat")
		sessionSubscribe(h, "pat", ch, 0)
		s, err := b.Open(ctx, ab2bus.OpenSessionInput{Hint: "impl"})
		if err != nil {
			t.Fatal(err)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutCursor(ctx, ab2store.Cursor{SessionID: s.ID, ChannelID: ch.ID, CursorSeq: 4, PendingEndSeq: 7}); err != nil {
				return err
			}
			return tx.SetPendingToken(ctx, s.ID, "batch-1")
		})
		sessionLapse(h)
		for round := 0; round < 2; round++ {
			r, err := b.Open(ctx, ab2bus.OpenSessionInput{ResumeToken: s.ResumeToken})
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
			if !r.Resumed || r.ID != s.ID || r.DisplayName != "pat/impl" || r.ResumeToken != s.ResumeToken {
				t.Fatalf("round %d: resumed = %+v, want same id, name and token", round, r)
			}
			c := sessionCursors(h, s.ID)[ch.ID]
			if c.CursorSeq != 4 || c.PendingEndSeq != 7 {
				t.Fatalf("round %d: cursor = %+v, want untouched", round, c)
			}
			rows := sessionRows(h, "pat")
			if len(rows) != 1 || rows[0].PendingToken != "batch-1" {
				t.Fatalf("round %d: rows = %+v, want the one row with its pending token", round, rows)
			}
			if rows[0].LeaseExpiresAt != h.core.LeaseUntil(h.core.NowMs()) {
				t.Fatalf("round %d: lease %d not renewed", round, rows[0].LeaseExpiresAt)
			}
		}
	})
}

func TestSessionResumeAfterRetention(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		b := h.as("pat")
		s, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			n, err := tx.DeleteSessions(ctx, h.core.LeaseUntil(h.core.NowMs())+1, 10)
			if err == nil && n != 1 {
				t.Fatalf("DeleteSessions removed %d rows, want 1", n)
			}
			return err
		})
		r, err := b.Open(ctx, ab2bus.OpenSessionInput{ResumeToken: s.ResumeToken})
		if err != nil {
			t.Fatalf("a token of a swept row must open a fresh session: %v", err)
		}
		if r.Resumed || r.ID == s.ID || r.ResumeToken == s.ResumeToken {
			t.Fatalf("got %+v, want a fresh session with a new id and token", r)
		}
	})
}

func TestSessionResumeClosed(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		b := h.as("pat")
		s, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Close(ctx, ab2bus.CloseSessionInput{Session: s.ID}); err != nil {
			t.Fatal(err)
		}
		_, err = b.Open(ctx, ab2bus.OpenSessionInput{ResumeToken: s.ResumeToken})
		codeOf(t, err, ab2bus.CodeSessionClosed)
	})
}

func TestSessionResumeOtherIdentity(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		s, err := h.as("pat").Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.as("kim").Open(ctx, ab2bus.OpenSessionInput{ResumeToken: s.ResumeToken})
		codeOf(t, err, ab2bus.CodeNotFound)
	})
}

func TestSessionLapsedRefusesAndResumes(t *testing.T) {
	// Decision (Eric, 2026-10-21): Milestone 3 item 2
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		b := h.as("pat")
		h.as("kim")
		s, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		svc := sessions.Service{Core: h.core}
		require := func(identity, id string) error {
			return h.core.Store.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error {
				_, err := svc.Require(ctx, tx, identity, id)
				return err
			})
		}
		// Within the skew the call succeeds and renews.
		h.clock.Advance(time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds) * time.Second)
		if err := require("pat", s.ID); err != nil {
			t.Fatalf("within skew: %v", err)
		}
		if rows := sessionRows(h, "pat"); rows[0].LeaseExpiresAt != h.core.LeaseUntil(h.core.NowMs()) {
			t.Fatalf("lease %d not renewed", rows[0].LeaseExpiresAt)
		}
		sessionLapse(h)
		codeOf(t, require("pat", s.ID), ab2bus.CodeSessionLapsed)
		codeOf(t, require("kim", s.ID), ab2bus.CodeNotFound)
		codeOf(t, require("pat", "nosuch"), ab2bus.CodeNotFound)
		// The token revives the lapsed row.
		r, err := b.Open(ctx, ab2bus.OpenSessionInput{ResumeToken: s.ResumeToken})
		if err != nil || !r.Resumed || r.ID != s.ID {
			t.Fatalf("resume = %+v, %v", r, err)
		}
		if err := require("pat", s.ID); err != nil {
			t.Fatalf("after resume: %v", err)
		}
		if err := b.Close(ctx, ab2bus.CloseSessionInput{Session: s.ID}); err != nil {
			t.Fatal(err)
		}
		codeOf(t, require("pat", s.ID), ab2bus.CodeSessionClosed)
	})
}

func TestSessionDisplayNameUniqueAcrossResumable(t *testing.T) {
	// Decision (Eric, 2026-10-21): Milestone 3 item 11
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		b := h.as("pat")
		first, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		sessionLapse(h)
		second, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil || second.DisplayName != "pat-2" {
			t.Fatalf("a lapsed retained pat must hold its name: %+v, %v", second, err)
		}
		if err := b.Close(ctx, ab2bus.CloseSessionInput{Session: first.ID}); err != nil {
			t.Fatalf("closing a lapsed row: %v", err)
		}
		third, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil || third.DisplayName != "pat" {
			t.Fatalf("closing the lapsed row must free pat: %+v, %v", third, err)
		}
		for _, id := range []string{second.ID, third.ID} {
			if err := b.Close(ctx, ab2bus.CloseSessionInput{Session: id}); err != nil {
				t.Fatal(err)
			}
		}
		// Open and close in turn: pat every time (closing frees at once).
		for i := 0; i < 3; i++ {
			s, err := b.Open(ctx, ab2bus.OpenSessionInput{})
			if err != nil || s.DisplayName != "pat" {
				t.Fatalf("round %d: %+v, %v; want pat", i, s, err)
			}
			if err := b.Close(ctx, ab2bus.CloseSessionInput{Session: s.ID}); err != nil {
				t.Fatal(err)
			}
		}
		// A closed row past the retention window behaves the same.
		h.clock.Advance(time.Duration(h.cfg.SessionRetentionHours+1) * time.Hour)
		s, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil || s.DisplayName != "pat" {
			t.Fatalf("after retention: %+v, %v; want pat", s, err)
		}
	})
}

func TestSessionOpenRetryLeavesLapsedRow(t *testing.T) {
	// Decision (Eric, 2026-10-21): Milestone 3 item 12
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		b := h.as("pat")
		a, err := b.Open(ctx, ab2bus.OpenSessionInput{Hint: "impl"})
		if err != nil {
			t.Fatal(err)
		}
		c, err := b.Open(ctx, ab2bus.OpenSessionInput{Hint: "impl"})
		if err != nil {
			t.Fatal(err)
		}
		if a.ID == c.ID || a.DisplayName == c.DisplayName || a.ResumeToken == c.ResumeToken {
			t.Fatalf("two keyless opens must be two rows: %+v %+v", a, c)
		}
		if rows := sessionRows(h, "pat"); len(rows) != 2 {
			t.Fatalf("rows = %d, want 2", len(rows))
		}
		sessionLapse(h)
		for _, r := range sessionRows(h, "pat") {
			if r.LeaseExpiresAt+int64(h.cfg.LeaseSkewSeconds)*1000 >= h.core.NowMs() {
				t.Fatalf("orphan row %s not lapsed on the clock", r.ID)
			}
		}
	})
}

func TestSessionCloseIdempotent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		b := h.as("pat")
		s, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := b.Close(ctx, ab2bus.CloseSessionInput{Session: s.ID}); err != nil {
				t.Fatalf("close %d: %v", i, err)
			}
		}
		if err := b.Close(ctx, ab2bus.CloseSessionInput{Session: s.ID, IdempotencyKey: "k1"}); err != nil {
			t.Fatal(err)
		}
		codeOf(t, h.as("kim").Close(ctx, ab2bus.CloseSessionInput{Session: s.ID}), ab2bus.CodeNotFound)
		codeOf(t, b.Close(ctx, ab2bus.CloseSessionInput{Session: "nosuch"}), ab2bus.CodeNotFound)
		codeOf(t, b.Close(ctx, ab2bus.CloseSessionInput{}), ab2bus.CodeValidation)
		codeOf(t, newBus(h, "ghost").Close(ctx, ab2bus.CloseSessionInput{Session: s.ID}), ab2bus.CodeNotRegistered)
		rows := sessionRows(h, "pat")
		if len(rows) != 1 || rows[0].ClosedAt == 0 {
			t.Fatalf("rows = %+v, want one closed row", rows)
		}
		// The lease is set to the close time so the retention sweep removes it.
		if rows[0].LeaseExpiresAt > h.core.NowMs() {
			t.Fatalf("closed row lease %d is in the future", rows[0].LeaseExpiresAt)
		}
	})
}

func TestSessionIdentitiesListsLive(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		root := h.admin("root")
		h.as("idle") // registered, no session
		if _, err := h.as("gone").Open(ctx, ab2bus.OpenSessionInput{}); err != nil {
			t.Fatal(err)
		}
		sessionLapse(h)
		p1, _ := pat.Open(ctx, ab2bus.OpenSessionInput{})
		h.clock.Advance(time.Second)
		p2, _ := pat.Open(ctx, ab2bus.OpenSessionInput{Hint: "impl"})
		r1, _ := root.Open(ctx, ab2bus.OpenSessionInput{})
		closed, _ := pat.Open(ctx, ab2bus.OpenSessionInput{})
		if err := pat.Close(ctx, ab2bus.CloseSessionInput{Session: closed.ID}); err != nil {
			t.Fatal(err)
		}
		list, err := h.as("idle").Identities(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, i := range list {
			names = append(names, i.Name)
		}
		if !slices.Equal(names, []string{"pat", "root"}) {
			t.Fatalf("identities = %v, want [pat root]", names)
		}
		if list[0].Admin || !list[1].Admin {
			t.Fatalf("admin flags = %v %v, want false true", list[0].Admin, list[1].Admin)
		}
		var ids []string
		for _, s := range list[0].Sessions {
			ids = append(ids, s.ID)
			if s.Identity != "pat" || s.OpenedAt == 0 {
				t.Fatalf("session info = %+v", s)
			}
		}
		if !slices.Equal(ids, []string{p1.ID, p2.ID}) {
			t.Fatalf("pat sessions = %v, want [%s %s] by OpenedAt", ids, p1.ID, p2.ID)
		}
		if len(list[1].Sessions) != 1 || list[1].Sessions[0].ID != r1.ID {
			t.Fatalf("root sessions = %+v", list[1].Sessions)
		}
		_, err = newBus(h, "ghost").Identities(ctx)
		codeOf(t, err, ab2bus.CodeNotRegistered)
	})
}

// An empty session id: Close is the core's required-field validation, Receive
// and Watch look the session up and find none (not_found). On the wire the id
// is an empty path segment, the router's 404, so the remote Bus judges Close
// itself (as for Send's empty channel); the other two agree without a guard.
// Decision (Eric, 2026-10-21): Milestone 4 item 6
func TestEmptySessionIDAgreesAcrossTransports(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		b := h.as("pat")
		codeOf(t, b.Close(ctx, ab2bus.CloseSessionInput{}), ab2bus.CodeValidation)
		_, err := b.Receive(ctx, ab2bus.ReceiveInput{})
		codeOf(t, err, ab2bus.CodeNotFound)
		_, err = b.Watch(ctx, ab2bus.WatchInput{Timeout: 10 * time.Millisecond})
		codeOf(t, err, ab2bus.CodeNotFound)
	})
}
