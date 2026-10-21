package bus

import (
	"context"
	"reflect"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func peekOf(t *testing.T, b ab2bus.Bus, in ab2bus.PeekInput) ab2bus.PeekResult {
	t.Helper()
	r, err := b.Peek(context.Background(), in)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	return r
}

func wantPeek(t *testing.T, got ab2bus.PeekResult, count int, chans ...ab2bus.PeekChannel) {
	t.Helper()
	if chans == nil {
		chans = []ab2bus.PeekChannel{}
	}
	if got.Count != count || !reflect.DeepEqual(got.Channels, chans) {
		t.Fatalf("Peek = %+v, want Count %d Channels %+v", got, count, chans)
	}
}

func sendOn(t *testing.T, b ab2bus.Bus, channel, session string) {
	t.Helper()
	if _, err := b.Send(context.Background(), ab2bus.SendInput{Channel: channel, Content: "x", Session: session}); err != nil {
		t.Fatalf("Send(%s): %v", channel, err)
	}
}

func TestPeekUsesWatchFloor(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, watchOpts(nil))
		h.channel("general", ab2store.KindOrdinary, "pat", nil)
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "general", "dev")
		sam := h.as("sam")
		for _, c := range []string{"general", "dev"} {
			if err := sam.Subscribe(ctx, ab2bus.SubscribeInput{Channel: c, From: "oldest"}); err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
		}
		sendOn(t, sam, "general", "")
		sendOn(t, sam, "general", "")
		sendOn(t, sam, "dev", "")

		wantPeek(t, peekOf(t, b, ab2bus.PeekInput{Session: s.ID}), 3,
			ab2bus.PeekChannel{Channel: "dev", Count: 1}, ab2bus.PeekChannel{Channel: "general", Count: 2})

		// A batch held without an ack: peek says 0, Open's Pending still says 3.
		r := recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		if len(r.Messages) != 3 {
			t.Fatalf("batch = %d messages, want 3", len(r.Messages))
		}
		wantPeek(t, peekOf(t, b, ab2bus.PeekInput{Session: s.ID}), 0)
		resumed, err := b.Open(ctx, ab2bus.OpenSessionInput{ResumeToken: s.ResumeToken})
		if err != nil {
			t.Fatalf("Open resume: %v", err)
		}
		var pending int64
		for _, p := range resumed.Pending {
			pending += p.Pending
		}
		if pending != 3 {
			t.Fatalf("Open Pending = %d (%+v), want 3 while Peek says 0", pending, resumed.Pending)
		}
		// The watch agrees with the peek on the floor.
		w, err := b.Watch(ctx, ab2bus.WatchInput{Session: s.ID, Timeout: 100 * time.Millisecond})
		if err != nil || w.Woke {
			t.Fatalf("Watch = %+v, %v; want no wake over a held batch", w, err)
		}

		// Acking settles it.
		recv(t, b, ab2bus.ReceiveInput{Session: s.ID, Ack: r.Batch})
		wantPeek(t, peekOf(t, b, ab2bus.PeekInput{Session: s.ID}), 0)

		sendOn(t, sam, "general", "")
		wantPeek(t, peekOf(t, b, ab2bus.PeekInput{Session: s.ID}), 1, ab2bus.PeekChannel{Channel: "general", Count: 1})

		// The session's own send is excluded unless asked for (the watch's
		// ownFilter excludes the session, not the identity).
		sendOn(t, b, "general", s.ID)
		wantPeek(t, peekOf(t, b, ab2bus.PeekInput{Session: s.ID}), 1, ab2bus.PeekChannel{Channel: "general", Count: 1})
		wantPeek(t, peekOf(t, b, ab2bus.PeekInput{Session: s.ID, IncludeOwn: true}), 2, ab2bus.PeekChannel{Channel: "general", Count: 2})
		// A second session of pat is another session: counted by default.
		s2, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatalf("Open second: %v", err)
		}
		sendOn(t, b, "general", s2.ID)
		wantPeek(t, peekOf(t, b, ab2bus.PeekInput{Session: s.ID}), 2, ab2bus.PeekChannel{Channel: "general", Count: 2})
	})
}

type sessionState struct {
	Row     ab2store.Session
	Cursors []ab2store.Cursor
}

func stateOf(h *harness, id string) sessionState {
	h.t.Helper()
	st := sessionState{Row: sessionRow(h, id)}
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		st.Cursors, err = tx.Cursors(ctx, id)
		return err
	})
	return st
}

func TestPeekWritesNothing(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, watchOpts(nil))
		h.channel("general", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "general")
		sam := h.as("sam")
		if err := sam.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "general", From: "oldest"}); err != nil {
			t.Fatal(err)
		}
		sendOn(t, sam, "general", "")
		recv(t, b, ab2bus.ReceiveInput{Session: s.ID}) // a pending token exists
		h.clock.Advance(5 * time.Second)               // a renewal would move the lease
		before := stateOf(h, s.ID)
		for range 10 {
			peekOf(t, b, ab2bus.PeekInput{Session: s.ID})
		}
		if after := stateOf(h, s.ID); !reflect.DeepEqual(before, after) {
			t.Fatalf("Peek changed the session:\nbefore %+v\nafter  %+v", before, after)
		}

		// A watch armed through a second Bus of pat is not replaced by Peek.
		b2 := newBus(h, "pat")
		wc := startWatch(ctx, b2, ab2bus.WatchInput{Session: s.ID, After: 100, Timeout: 3 * time.Second})
		time.Sleep(100 * time.Millisecond)
		peekOf(t, b, ab2bus.PeekInput{Session: s.ID})
		sendOn(t, sam, "general", "")
		sendOn(t, sam, "general", "") // seq above After
		o := awaitWatch(t, wc, 4*time.Second)
		if o.err != nil && ab2bus.CodeOf(o.err) == ab2bus.CodeReplaced {
			t.Fatalf("Peek replaced the watch: %v", o.err)
		}

		// A lapsed session answers session_lapsed and stays lapsed.
		h.clock.Advance(time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds+1) * time.Second)
		_, err := b.Peek(ctx, ab2bus.PeekInput{Session: s.ID})
		codeOf(t, err, ab2bus.CodeSessionLapsed)
		_, err = b.Receive(ctx, ab2bus.ReceiveInput{Session: s.ID})
		codeOf(t, err, ab2bus.CodeSessionLapsed)

		// closed, unknown, someone else's.
		s3, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Close(ctx, ab2bus.CloseSessionInput{Session: s3.ID}); err != nil {
			t.Fatal(err)
		}
		_, err = b.Peek(ctx, ab2bus.PeekInput{Session: s3.ID})
		codeOf(t, err, ab2bus.CodeSessionClosed)
		_, err = b.Peek(ctx, ab2bus.PeekInput{Session: "nosuchsession"})
		codeOf(t, err, ab2bus.CodeNotFound)
		s4, err := b.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.as("kim").Peek(ctx, ab2bus.PeekInput{Session: s4.ID})
		codeOf(t, err, ab2bus.CodeForbidden)
	})
}
