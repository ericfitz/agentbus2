package bus

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// recvOpen opens a session for identity after subscribing it to the channels
// from the oldest message.
func recvOpen(h *harness, identity string, channels ...string) (ab2bus.Bus, ab2bus.Session) {
	h.t.Helper()
	b := h.as(identity)
	for _, c := range channels {
		if err := b.Subscribe(context.Background(), ab2bus.SubscribeInput{Channel: c, From: "oldest"}); err != nil {
			h.t.Fatalf("Subscribe(%s): %v", c, err)
		}
	}
	s, err := b.Open(context.Background(), ab2bus.OpenSessionInput{})
	if err != nil {
		h.t.Fatalf("Open(%s): %v", identity, err)
	}
	return b, s
}

func recv(t *testing.T, b ab2bus.Bus, in ab2bus.ReceiveInput) ab2bus.ReceiveResult {
	t.Helper()
	r, err := b.Receive(context.Background(), in)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	return r
}

func recvSeqs(r ab2bus.ReceiveResult) []int64 {
	out := []int64{}
	for _, m := range r.Messages {
		out = append(out, m.Seq)
	}
	return out
}

func wantSeqs(t *testing.T, r ab2bus.ReceiveResult, want ...int64) {
	t.Helper()
	if got := recvSeqs(r); !slices.Equal(got, want) {
		t.Fatalf("seqs = %v, want %v", got, want)
	}
}

// cursorOf is the session's cursor row on the channel (zero when absent).
func cursorOf(h *harness, session string, channelID int64) ab2store.Cursor {
	h.t.Helper()
	var out ab2store.Cursor
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		cs, err := tx.Cursors(ctx, session)
		for _, c := range cs {
			if c.ChannelID == channelID {
				out = c
			}
		}
		return err
	})
	return out
}

func sessionRow(h *harness, id string) ab2store.Session {
	h.t.Helper()
	var s ab2store.Session
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		s, err = tx.Session(ctx, id)
		return err
	})
	return s
}

func TestReceiveBatchAndAck(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		h.ensureIdentity("kim")
		h.append(ch, "kim", "", "one")
		h.append(ch, "kim", "", "two")

		r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		wantSeqs(t, r, 1, 2)
		if r.Batch == "" || r.Redelivered || r.AckIgnored || !strings.Contains(r.Instruction, r.Batch) {
			t.Fatalf("first result = %+v", r)
		}
		if !strings.HasSuffix(r.Batch, ":o0") || len(r.Batch) != 27 {
			t.Fatalf("batch token %q: want 24 hex characters and :o0", r.Batch)
		}
		again := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		wantSeqs(t, again, 1, 2)
		if !again.Redelivered || again.Batch != r.Batch {
			t.Fatalf("second result = %+v, want a redelivery under %q", again, r.Batch)
		}
		if c := cursorOf(h, s.ID, ch.ID); c.CursorSeq != 0 || c.PendingEndSeq != 2 {
			t.Fatalf("cursor before ack = %+v", c)
		}
		acked := recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Ack: r.Batch})
		wantSeqs(t, acked)
		if acked.Batch != "" || acked.AckIgnored || acked.Redelivered {
			t.Fatalf("ack result = %+v", acked)
		}
		if c := cursorOf(h, s.ID, ch.ID); c.CursorSeq != 2 || c.PendingEndSeq != 0 {
			t.Fatalf("cursor after ack = %+v", c)
		}
		if got := ackedOf(h, "pat", ch.ID); got != 2 {
			t.Fatalf("acked mark = %d, want 2", got)
		}
		if row := sessionRow(h, s.ID); row.PendingToken != "" {
			t.Fatalf("pending token %q survived the ack", row.PendingToken)
		}
	})
}

func TestReceiveAckReplayIgnored(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		_, other := recvOpen(h, "pat")
		h.ensureIdentity("kim")
		h.append(ch, "kim", "", "one")
		r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		if first := recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Ack: r.Batch}); first.AckIgnored {
			t.Fatal("the first ack was ignored")
		}
		h.append(ch, "kim", "", "two")
		// A replay of the old token must not touch the new pending batch.
		next := recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Ack: r.Batch})
		if !next.AckIgnored {
			t.Fatal("a replayed ack was not ignored")
		}
		wantSeqs(t, next, 2)
		if next.Batch == "" || next.Batch == r.Batch {
			t.Fatalf("new batch %q", next.Batch)
		}
		// Another session's id (and so its token shape) matches nothing here.
		if foreign := recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Ack: other.ID}); !foreign.AckIgnored || !foreign.Redelivered {
			t.Fatalf("foreign token result = %+v", foreign)
		}
		// A plain receive is not an ignored ack.
		if plain := recv(t, b, ab2bus.ReceiveInput{Session: s.ID}); plain.AckIgnored {
			t.Fatal("a receive without Ack reported AckIgnored")
		}
		if c := cursorOf(h, s.ID, ch.ID); c.CursorSeq != 1 || c.PendingEndSeq != 2 {
			t.Fatalf("cursor = %+v, want cursor 1 pending 2", c)
		}
	})
}

func TestReceiveAckSurvivesRename(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		h.ensureIdentity("kim")
		h.append(ch, "kim", "", "one")
		r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		if err := b.RenameChannel(context.Background(), ab2bus.RenameChannelInput{Name: "dev", NewName: "dev2"}); err != nil {
			t.Fatal(err)
		}
		acked := recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Ack: r.Batch})
		if acked.AckIgnored {
			t.Fatal("the ack after a rename was ignored")
		}
		if c := cursorOf(h, s.ID, ch.ID); c.CursorSeq != 1 || c.PendingEndSeq != 0 {
			t.Fatalf("cursor = %+v", c)
		}
	})
}

func TestReceiveExcludesOwnSessionOnly(t *testing.T) {
	// Decision (Eric, 2026-10-21): Milestone 3 item 1
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		a, sa := recvOpen(h, "pat", "dev")
		_, sb := recvOpen(h, "pat")
		ctx := context.Background()
		if _, err := a.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "from A", Session: sa.ID}); err != nil {
			t.Fatal(err)
		}
		wantSeqs(t, recv(t, a, ab2bus.ReceiveInput{Session: sa.ID}))
		wantSeqs(t, recv(t, a, ab2bus.ReceiveInput{Session: sb.ID}), 1)
		inc := recv(t, a, ab2bus.ReceiveInput{Session: sa.ID, IncludeOwn: true})
		wantSeqs(t, inc, 1)
		if !strings.HasSuffix(inc.Batch, ":o1") {
			t.Fatalf("include_own batch %q lacks :o1", inc.Batch)
		}
	})
}

func TestReceiveNewSessionContinuesFromAckedMark(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, sa := recvOpen(h, "pat", "dev")
		h.ensureIdentity("kim")
		for _, c := range []string{"1", "2", "3", "4"} {
			h.append(ch, "kim", "", c)
		}
		r := recv(t, b, ab2bus.ReceiveInput{Session: sa.ID, Count: 3})
		wantSeqs(t, r, 1, 2, 3)
		// The ack advances the mark to 3; the same call delivers 4 unacked.
		wantSeqs(t, recv(t, b, ab2bus.ReceiveInput{Session: sa.ID, Ack: r.Batch, Count: 1}), 4)
		if err := b.Close(context.Background(), ab2bus.CloseSessionInput{Session: sa.ID}); err != nil {
			t.Fatal(err)
		}
		sb, err := b.Open(context.Background(), ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		wantSeqs(t, recv(t, b, ab2bus.ReceiveInput{Session: sb.ID}), 4)
	})
}

func TestReceiveResumedSessionKeepsPendingBatch(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		h.ensureIdentity("kim")
		h.append(ch, "kim", "", "one")
		r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		h.clock.Advance(time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds+1) * time.Second)
		_, err := b.Receive(context.Background(), ab2bus.ReceiveInput{Session: s.ID})
		codeOf(t, err, ab2bus.CodeSessionLapsed)
		res, err := b.Open(context.Background(), ab2bus.OpenSessionInput{ResumeToken: s.ResumeToken})
		if err != nil || !res.Resumed {
			t.Fatalf("resume = %+v, %v", res, err)
		}
		again := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		wantSeqs(t, again, 1)
		if !again.Redelivered || again.Batch != r.Batch {
			t.Fatalf("after resume = %+v, want a redelivery under %q", again, r.Batch)
		}
	})
}

func TestReceiveSkipsUnreadableSubscription(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ch := h.channel("sec", ab2store.KindOrdinary, "kim", nil)
		b, s := recvOpen(h, "pat", "sec")
		h.ensureIdentity("kim")
		h.append(ch, "kim", "", "one")
		rw := []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}
		setACL := func(entries ...ab2store.ACLEntry) {
			h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.SetACL(ctx, ch.ID, entries) })
		}
		setACL(ab2store.ACLEntry{Identity: "kim", Permissions: rw})
		before := cursorOf(h, s.ID, ch.ID)
		r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		wantSeqs(t, r)
		if r.Batch != "" || cursorOf(h, s.ID, ch.ID) != before {
			t.Fatalf("an unreadable subscription was touched: %+v, cursor %+v vs %+v", r, cursorOf(h, s.ID, ch.ID), before)
		}
		setACL(ab2store.ACLEntry{Identity: "kim", Permissions: rw}, ab2store.ACLEntry{Identity: "pat", Permissions: rw})
		wantSeqs(t, recv(t, b, ab2bus.ReceiveInput{Session: s.ID}), 1)
	})
}

func TestReceiveTagMatchNeedsRead(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		direct := h.channel("direct", ab2store.KindOrdinary, "kim", nil)
		open := h.channel("open", ab2store.KindOrdinary, "kim", nil)
		closed := h.channel("closed", ab2store.KindOrdinary, "kim", []ab2store.ACLEntry{
			{Identity: "kim", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}},
		})
		b := h.as("pat")
		if err := b.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "direct", From: "oldest"}); err != nil {
			t.Fatal(err)
		}
		if err := b.SubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"env:*"}}); err != nil {
			t.Fatal(err)
		}
		s, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		dseq := h.append(direct, "kim", "", "d", "env:prod")
		oseq := h.append(open, "kim", "", "o", "env:prod", "team")
		h.append(closed, "kim", "", "c", "env:prod")
		h.append(open, "kim", "", "untagged")
		r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		wantSeqs(t, r, dseq, oseq)
		if len(r.Messages[0].MatchedTags) != 0 {
			t.Fatalf("directly subscribed message has MatchedTags %v", r.Messages[0].MatchedTags)
		}
		if got := r.Messages[1].MatchedTags; !slices.Equal(got, []string{"env:prod"}) {
			t.Fatalf("MatchedTags = %v", got)
		}
		if row := sessionRow(h, s.ID); row.TagsPendingEndSeq != oseq {
			t.Fatalf("tags pending end = %d, want %d", row.TagsPendingEndSeq, oseq)
		}
		recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Ack: r.Batch})
		if row := sessionRow(h, s.ID); row.TagsCursorSeq != oseq || row.TagsPendingEndSeq != 0 {
			t.Fatalf("tags cursor after ack = %d/%d", row.TagsCursorSeq, row.TagsPendingEndSeq)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			id, err := tx.Identity(ctx, "pat")
			if id.TagsAckedSeq != oseq {
				t.Errorf("identity tags acked mark = %d, want %d", id.TagsAckedSeq, oseq)
			}
			return err
		})
	})
}

func TestReceiveChannelsFilterAndCount(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		a := h.channel("a", ab2store.KindOrdinary, "pat", nil)
		bch := h.channel("b", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "a", "b")
		h.ensureIdentity("kim")
		h.append(a, "kim", "", "a1")
		h.append(bch, "kim", "", "b1")
		h.append(a, "kim", "", "a2")

		// Count -1 is refused and mints nothing.
		_, err := b.Receive(ctx, ab2bus.ReceiveInput{Session: s.ID, Count: -1})
		codeOf(t, err, ab2bus.CodeValidation)
		if row := sessionRow(h, s.ID); row.PendingToken != "" {
			t.Fatalf("a refused receive left token %q", row.PendingToken)
		}
		_, err = b.Receive(ctx, ab2bus.ReceiveInput{Session: s.ID, Channels: []string{"nope"}})
		codeOf(t, err, ab2bus.CodeNotFound)

		r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Channels: []string{"a"}, Count: 1})
		wantSeqs(t, r, 1)
		r = recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Channels: []string{"a"}, Count: 1, Ack: r.Batch})
		wantSeqs(t, r, 3)
		r = recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Channels: []string{"a"}, Ack: r.Batch})
		wantSeqs(t, r)
		// Count 0 defaults: the one message on b arrives.
		r = recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		wantSeqs(t, r, 2)
		// A pending batch outside the filter is still redelivered.
		again := recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Channels: []string{"a"}})
		wantSeqs(t, again, 2)
		if !again.Redelivered || again.Batch != r.Batch {
			t.Fatalf("redelivery = %+v", again)
		}
	})
}

func TestReceiveGaps(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		h.ensureIdentity("kim")
		for i := 0; i < 5; i++ {
			h.append(ch, "kim", "", "m")
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.SetEvictedBefore(ctx, ch.ID, 4) })
		r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		if len(r.Gaps) != 1 || r.Gaps[0] != (ab2bus.Gap{Channel: "dev", From: 1, To: 3}) {
			t.Fatalf("Gaps = %+v", r.Gaps)
		}
		wantSeqs(t, r, 4, 5)
		if c := cursorOf(h, s.ID, ch.ID); c.CursorSeq != 3 || c.PendingEndSeq != 5 {
			t.Fatalf("cursor = %+v", c)
		}
		again := recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Ack: r.Batch})
		if len(again.Gaps) != 0 {
			t.Fatalf("gap reported twice: %+v", again.Gaps)
		}
	})
}

func TestReceiveNeedsSession(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		b, s := recvOpen(h, "pat")
		_, err := b.Receive(ctx, ab2bus.ReceiveInput{Session: "ghost"})
		codeOf(t, err, ab2bus.CodeNotFound)
		_, err = h.as("kim").Receive(ctx, ab2bus.ReceiveInput{Session: s.ID})
		codeOf(t, err, ab2bus.CodeNotFound)
		_, err = b.Receive(ctx, ab2bus.ReceiveInput{})
		codeOf(t, err, ab2bus.CodeNotFound)
		h.clock.Advance(time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds+1) * time.Second)
		_, err = b.Receive(ctx, ab2bus.ReceiveInput{Session: s.ID})
		codeOf(t, err, ab2bus.CodeSessionLapsed)
	})
}

func TestReceiveTrimsAndRedeliveryCeiling(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		cfg := core.Defaults()
		cfg.ResultDefaultKiB = 1
		h := newHarness(t, e, harnessOptions{cfg: &cfg})
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		h.ensureIdentity("kim")
		body := strings.Repeat("x", 600)
		for i := 0; i < 3; i++ {
			h.append(ch, "kim", "", body)
		}
		r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		wantSeqs(t, r, 1)
		if c := cursorOf(h, s.ID, ch.ID); c.PendingEndSeq != 1 {
			t.Fatalf("pending end = %d, want 1", c.PendingEndSeq)
		}
		again := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		wantSeqs(t, again, 1)
		if !again.Redelivered || again.Batch != r.Batch {
			t.Fatalf("redelivery = %+v", again)
		}
		wantSeqs(t, recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Ack: r.Batch}), 2)
	})
}
