package bus

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

type watchOut struct {
	res ab2bus.WatchResult
	err error
	at  time.Duration
}

// startWatch runs Watch in a goroutine; the result arrives with its elapsed time.
func startWatch(ctx context.Context, b ab2bus.Bus, in ab2bus.WatchInput) <-chan watchOut {
	ch := make(chan watchOut, 1)
	start := time.Now()
	go func() {
		res, err := b.Watch(ctx, in)
		ch <- watchOut{res, err, time.Since(start)}
	}()
	return ch
}

func awaitWatch(t *testing.T, ch <-chan watchOut, within time.Duration) watchOut {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(within):
		t.Fatalf("the watch did not return within %s", within)
		return watchOut{}
	}
}

// sendLater sends from identity after d.
func sendLater(t *testing.T, h *harness, identity, channel string, d time.Duration, tags ...string) {
	t.Helper()
	b := h.as(identity)
	go func() {
		time.Sleep(d)
		if _, err := b.Send(context.Background(), ab2bus.SendInput{Channel: channel, Content: "x", Tags: tags}); err != nil {
			t.Errorf("Send: %v", err)
		}
	}()
}

// watchOpts is a harness configuration with no merge window unless f sets one.
func watchOpts(f func(*core.Config)) harnessOptions {
	c := core.Defaults()
	c.WatchMergeSeconds = 0
	if f != nil {
		f(&c)
	}
	return harnessOptions{cfg: &c}
}

func TestWatchWakesOnSend(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		sendLater(t, h, "kim", "dev", 100*time.Millisecond)
		o := awaitWatch(t, startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 2 * time.Second}), 2*time.Second)
		if o.err != nil || !o.res.Woke || o.res.Count != 1 || o.res.Seq != 1 || !slices.Equal(o.res.Channels, []string{"dev"}) {
			t.Fatalf("result = %+v, %v", o.res, o.err)
		}
	})
}

func TestWatchTimeout(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		o := awaitWatch(t, startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 200 * time.Millisecond}), 2*time.Second)
		if o.err != nil || o.res.Woke || o.at < 190*time.Millisecond {
			t.Fatalf("result = %+v, %v after %s", o.res, o.err, o.at)
		}
	})
}

func TestWatchIgnoresPendingBatch(t *testing.T) {
	// v1 ADR 0012: a delivered, unacknowledged batch never wakes.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		h.ensureIdentity("kim")
		h.append(ch, "kim", "", "one")
		recv(t, b, ab2bus.ReceiveInput{Session: s.ID})
		o := awaitWatch(t, startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 300 * time.Millisecond}), 2*time.Second)
		if o.err != nil || o.res.Woke {
			t.Fatalf("a pending batch woke the watch: %+v, %v", o.res, o.err)
		}
		sendLater(t, h, "kim", "dev", 100*time.Millisecond)
		o = awaitWatch(t, startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 2 * time.Second}), 2*time.Second)
		if o.err != nil || !o.res.Woke || o.res.Count != 1 || o.res.Seq != 2 {
			t.Fatalf("result = %+v, %v", o.res, o.err)
		}
	})
}

func TestWatchAfterFloor(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		h.ensureIdentity("kim")
		h.append(ch, "kim", "", "one")
		o := awaitWatch(t, startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, After: 1, Timeout: 300 * time.Millisecond}), 2*time.Second)
		if o.err != nil || o.res.Woke {
			t.Fatalf("After did not floor the watch: %+v, %v", o.res, o.err)
		}
		sendLater(t, h, "kim", "dev", 100*time.Millisecond)
		o = awaitWatch(t, startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, After: 1, Timeout: 2 * time.Second}), 2*time.Second)
		if o.err != nil || !o.res.Woke || o.res.Seq != 2 {
			t.Fatalf("result = %+v, %v", o.res, o.err)
		}
	})
}

func TestWatchReplaced(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		first := startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 5 * time.Second})
		time.Sleep(150 * time.Millisecond)
		second := startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 5 * time.Second})
		o := awaitWatch(t, first, 500*time.Millisecond)
		codeOf(t, o.err, ab2bus.CodeReplaced)
		sendLater(t, h, "kim", "dev", 100*time.Millisecond)
		o = awaitWatch(t, second, 2*time.Second)
		if o.err != nil || !o.res.Woke {
			t.Fatalf("the newer watch = %+v, %v", o.res, o.err)
		}
	})
}

func TestWatchReplacedAcrossCores(t *testing.T) {
	// Review Focus 6: the sibling's RegisterWatch publishes only into its own
	// notifier, so h's watcher learns of the replacement through its poll.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		opts := watchOpts(nil)
		opts.polling = 50 * time.Millisecond
		h := newHarness(t, e, opts)
		sib := h.sibling(opts)
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		first := startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 5 * time.Second})
		time.Sleep(200 * time.Millisecond)
		second := startWatch(context.Background(), sib.as("pat"), ab2bus.WatchInput{Session: s.ID, Timeout: 700 * time.Millisecond})
		o := awaitWatch(t, first, 500*time.Millisecond)
		codeOf(t, o.err, ab2bus.CodeReplaced)
		if o2 := awaitWatch(t, second, 2*time.Second); o2.err != nil || o2.res.Woke {
			t.Fatalf("the newer watch = %+v, %v", o2.res, o2.err)
		}
	})
}

func TestWatchMergesWakes(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(func(c *core.Config) { c.WatchMergeSeconds = 1 }))
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		sendLater(t, h, "kim", "dev", 100*time.Millisecond)
		sendLater(t, h, "kim", "dev", 200*time.Millisecond)
		o := awaitWatch(t, startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 5 * time.Second}), 4*time.Second)
		if o.err != nil || !o.res.Woke || o.res.Count != 2 || o.res.Seq != 2 {
			t.Fatalf("result = %+v, %v", o.res, o.err)
		}
		if o.at < time.Second {
			t.Fatalf("returned after %s, before the merge window ended", o.at)
		}
	})
}

func TestWatchIgnoresUnreadableChannel(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		ch := h.channel("sec", ab2store.KindOrdinary, "kim", nil)
		b, s := recvOpen(h, "pat", "sec")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.SetACL(ctx, ch.ID, []ab2store.ACLEntry{{Identity: "kim", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}}})
		})
		h.ensureIdentity("kim")
		h.append(ch, "kim", "", "hidden")
		o := awaitWatch(t, startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 300 * time.Millisecond}), 2*time.Second)
		if o.err != nil || o.res.Woke {
			t.Fatalf("an unreadable channel woke the watch: %+v, %v", o.res, o.err)
		}
	})
}

func TestWatchRenewsLease(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		before := sessionRow(h, s.ID).LeaseExpiresAt
		h.clock.Advance(30 * time.Second)
		o := awaitWatch(t, startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 100 * time.Millisecond}), 2*time.Second)
		if o.err != nil {
			t.Fatal(o.err)
		}
		row := sessionRow(h, s.ID)
		if row.LeaseExpiresAt <= before || row.WatchID == "" {
			t.Fatalf("lease %d -> %d, watch id %q", before, row.LeaseExpiresAt, row.WatchID)
		}
	})
}

func TestWatchTagMatchWakes(t *testing.T) {
	// Decision 38
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		h.channel("open", ab2store.KindOrdinary, "kim", nil)
		b := h.as("pat")
		if err := b.SubscribeTags(context.Background(), ab2bus.TagSubscribeInput{Patterns: []string{"urgent"}}); err != nil {
			t.Fatal(err)
		}
		s, err := b.Open(context.Background(), ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		sendLater(t, h, "kim", "open", 100*time.Millisecond) // untagged: no wake
		sendLater(t, h, "kim", "open", 300*time.Millisecond, "urgent")
		o := awaitWatch(t, startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 3 * time.Second}), 3*time.Second)
		if o.err != nil || !o.res.Woke || o.res.Seq != 2 || o.res.Count != 1 || !slices.Equal(o.res.Channels, []string{"open"}) {
			t.Fatalf("result = %+v, %v", o.res, o.err)
		}
	})
}

func TestWatchClampsTimeoutToCap(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(func(c *core.Config) { c.WatchMaxSeconds = 1 }))
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		_, err := b.Watch(context.Background(), ab2bus.WatchInput{Session: s.ID, Timeout: -1})
		codeOf(t, err, ab2bus.CodeValidation)
		o := awaitWatch(t, startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 10 * time.Minute}), 3*time.Second)
		if o.err != nil || o.res.Woke || o.at < time.Second || o.at > 1500*time.Millisecond {
			t.Fatalf("result = %+v, %v after %s; want a timeout between 1s and 1.5s", o.res, o.err, o.at)
		}
	})
}

func TestWatchContextCancel(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "pat", "dev")
		ctx, cancel := context.WithCancel(context.Background())
		w := startWatch(ctx, b, ab2bus.WatchInput{Session: s.ID, Timeout: 5 * time.Second})
		time.Sleep(100 * time.Millisecond)
		cancel()
		o := awaitWatch(t, w, time.Second)
		if !errors.Is(o.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", o.err)
		}
	})
}
