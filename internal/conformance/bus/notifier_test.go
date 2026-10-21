package bus

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/core/notify/pgnotify"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// randHex8 is 8 random hex digits, for application names.
func randHex8(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// crossProcessNotifier is the Notifier a replica in another process uses for
// the database at dsn: the polling form on sqlite (Decision 68), the LISTEN
// listener on postgres (Decision 52). The distinct application name keeps a
// pg_terminate_backend in another test away from it.
func crossProcessNotifier(t *testing.T, e dbtest.Engine, dsn string) notify.Notifier {
	t.Helper()
	switch e.Name {
	case "sqlite":
		return notify.NewPolling(100 * time.Millisecond)
	case "postgres":
		l := pgnotify.New(t.Context(), dsn, pgnotify.Options{
			MinBackoff:      50 * time.Millisecond,
			ApplicationName: "agentbus2-listen-test-" + randHex8(t),
		})
		t.Cleanup(l.Close)
		return l
	}
	t.Fatalf("unknown engine %q", e.Name)
	return nil
}

// harnessOverDSN builds a harness over a fresh database and the cross-process
// notifier over the same DSN. On postgres it waits for the listener's connect
// hint to be spent before returning. Tests subscribe on n, never on
// h.notifier.
func harnessOverDSN(t *testing.T, e dbtest.Engine) (h *harness, n notify.Notifier, dsn string) {
	t.Helper()
	s, reopen, dsn := openStoreDSN(t, e)
	h = &harness{t: t, e: e, reopen: reopen}
	h.build(s, harnessOptions{})
	n = crossProcessNotifier(t, e, dsn)
	if e.Name == "postgres" {
		waitConnected(t, n.(*pgnotify.Listener), 3*time.Second)
	}
	return h, n, dsn
}

func waitConnected(t *testing.T, l *pgnotify.Listener, d time.Duration) {
	t.Helper()
	waitFor(t, d, "listener connected", l.Connected)
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// drain discards hints for d.
func drain(hints <-chan notify.Hint, d time.Duration) {
	timer := time.After(d)
	for {
		select {
		case <-hints:
		case <-timer:
			return
		}
	}
}

// nextHint returns the next hint within d, or false.
func nextHint(hints <-chan notify.Hint, d time.Duration) (notify.Hint, bool) {
	select {
	case h := <-hints:
		return h, true
	case <-time.After(d):
		return notify.Hint{}, false
	}
}

// TestNotifierCrossProcessLiveness is the one both-engine property of the two
// forms: an armed watch in another process is woken at all. A ticker that
// stopped, or a listener whose NOTIFY never arrives, fails it.
func TestNotifierCrossProcessLiveness(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h, n, _ := harnessOverDSN(t, e)
		hints, cancel := n.Subscribe()
		defer cancel()
		if e.Name == "postgres" {
			ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
			h.sibling(harnessOptions{}).append(ch, "pat", "", "hello")
		} // sqlite: no append; the 100 ms polling tick is the wake-up
		if _, ok := nextHint(hints, time.Second); !ok {
			t.Fatal("no hint within 1 s")
		}
	})
}

// TestNotifierPublishFanOut: Publish reaches this process's subscribers on the
// listener (the in-process fan-out the core calls after commit) and is a no-op
// on the polling form, because a writer in another process cannot reach this
// one; this half fails if Polling ever starts forwarding Publish.
func TestNotifierPublishFanOut(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		_, n, _ := harnessOverDSN(t, e)
		hints, cancel := n.Subscribe()
		defer cancel()
		drain(hints, 300*time.Millisecond)
		want := notify.Hint{ChannelID: 5, Seq: 6}
		n.Publish(want)
		if e.Name == "postgres" {
			got, ok := nextHint(hints, time.Second)
			if !ok || got != want {
				t.Fatalf("got %v, %v; want %v", got, ok, want)
			}
			return
		}
		deadline := time.After(500 * time.Millisecond)
		for {
			select {
			case got := <-hints:
				if got != (notify.Hint{}) {
					t.Fatalf("polling form forwarded Publish: got %v", got)
				}
			case <-deadline:
				return
			}
		}
	})
}
