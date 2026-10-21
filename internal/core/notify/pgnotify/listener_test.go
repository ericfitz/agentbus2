package pgnotify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core/notify"
)

type event struct {
	payload string
	err     error
}

type fakeConn struct {
	channel string
	events  chan event
	mu      sync.Mutex
	closed  bool
	listens []string
}

func newFakeConn() *fakeConn { return &fakeConn{channel: "agentbus2_x", events: make(chan event, 16)} }

func (f *fakeConn) Channel(context.Context) (string, error) { return f.channel, nil }
func (f *fakeConn) Listen(_ context.Context, ch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listens = append(f.listens, ch)
	return nil
}
func (f *fakeConn) Wait(ctx context.Context) (string, error) {
	select {
	case e := <-f.events:
		return e.payload, e.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (f *fakeConn) Close(context.Context) error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}
func (f *fakeConn) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// step is one scripted Connect result; gate, when non-nil, delays it.
type step struct {
	c    *fakeConn
	err  error
	gate chan struct{}
}

type fakeConnector struct {
	mu    sync.Mutex
	steps []step
	calls int
}

func (f *fakeConnector) Connect(ctx context.Context, _ string) (conn, error) {
	f.mu.Lock()
	f.calls++
	var s step
	have := len(f.steps) > 0
	if have {
		s = f.steps[0]
		f.steps = f.steps[1:]
	}
	f.mu.Unlock()
	if !have {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.c, nil
}

func (f *fakeConnector) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type recHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *recHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := r.Level.String() + " " + r.Message
	r.Attrs(func(a slog.Attr) bool {
		m += " " + a.Key + "=" + a.Value.String()
		return true
	})
	h.msgs = append(h.msgs, m)
	return nil
}
func (h *recHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recHandler) WithGroup(string) slog.Handler      { return h }
func (h *recHandler) count(sub string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

func next(t *testing.T, ch <-chan notify.Hint) notify.Hint {
	t.Helper()
	select {
	case h := <-ch:
		return h
	case <-time.After(time.Second):
		t.Fatal("no hint within 1 s")
		return notify.Hint{}
	}
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestListenerPublishesParsedHints(t *testing.T) {
	c := newFakeConn()
	fc := &fakeConnector{steps: []step{{c: c}}}
	l := New(context.Background(), "dsn", Options{Connector: fc})
	hints, cancel := l.Subscribe()
	defer cancel()
	defer l.Close()
	// The subscriber may join after the first catch-up hint; either way the
	// listener ends up connected.
	eventually(t, "connected", l.Connected)
	select {
	case h := <-hints:
		if h != (notify.Hint{}) {
			t.Fatalf("first hint = %+v", h)
		}
	default:
	}
	c.events <- event{payload: "7:42"}
	if h := next(t, hints); h != (notify.Hint{ChannelID: 7, Seq: 42}) {
		t.Fatalf("hint = %+v", h)
	}
	if len(c.listens) != 1 || c.listens[0] != "agentbus2_x" {
		t.Fatalf("listens = %v", c.listens)
	}
}

func TestListenerMalformedPayload(t *testing.T) {
	c := newFakeConn()
	rh := &recHandler{}
	fc := &fakeConnector{steps: []step{{c: c}}}
	l := New(context.Background(), "dsn", Options{Connector: fc, Logger: slog.New(rh)})
	hints, cancel := l.Subscribe()
	defer cancel()
	defer l.Close()
	eventually(t, "connected", l.Connected)
	select {
	case <-hints:
	default:
	}
	for _, p := range []string{"garbage", ""} {
		c.events <- event{payload: p}
		if h := next(t, hints); h != (notify.Hint{}) {
			t.Fatalf("payload %q: hint = %+v", p, h)
		}
	}
	if n := rh.count("malformed payload"); n != 2 {
		t.Fatalf("malformed warnings = %d, want 2", n)
	}
	// Anyone who can NOTIFY on the channel controls the payload: the warning quotes a bounded prefix only.
	c.events <- event{payload: strings.Repeat("x", 8000)}
	if h := next(t, hints); h != (notify.Hint{}) {
		t.Fatalf("long payload: hint = %+v", h)
	}
	rh.mu.Lock()
	last := rh.msgs[len(rh.msgs)-1]
	rh.mu.Unlock()
	if !strings.Contains(last, "malformed payload") || len(last) > 200 {
		t.Fatalf("long payload warning is %d bytes, want a bounded prefix", len(last))
	}
	c.events <- event{payload: "1:2"}
	if h := next(t, hints); h != (notify.Hint{ChannelID: 1, Seq: 2}) {
		t.Fatalf("hint after malformed = %+v", h)
	}
}

func TestListenerReconnectsWithBackoffAndCatchUp(t *testing.T) {
	c1, c2 := newFakeConn(), newFakeConn()
	gate := make(chan struct{})
	fc := &fakeConnector{steps: []step{{c: c1}, {c: c2, gate: gate}}}
	l := New(context.Background(), "dsn", Options{Connector: fc, MinBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond})
	hints, cancel := l.Subscribe()
	defer cancel()
	defer l.Close()
	eventually(t, "first connect", l.Connected)
	select {
	case <-hints:
	default:
	}
	c1.events <- event{payload: "1:2"}
	if h := next(t, hints); h != (notify.Hint{ChannelID: 1, Seq: 2}) {
		t.Fatalf("hint = %+v", h)
	}
	c1.events <- event{err: errors.New("boom")}
	eventually(t, "disconnected", func() bool { return !l.Connected() })
	eventually(t, "first conn closed", c1.isClosed)
	eventually(t, "second Connect call", func() bool { return fc.Calls() == 2 })
	if l.Connected() {
		t.Fatal("Connected true before the re-LISTEN")
	}
	close(gate)
	if h := next(t, hints); h != (notify.Hint{}) {
		t.Fatalf("catch-up hint = %+v", h)
	}
	eventually(t, "reconnected", l.Connected)
	c2.events <- event{payload: "3:4"}
	if h := next(t, hints); h != (notify.Hint{ChannelID: 3, Seq: 4}) {
		t.Fatalf("hint on second conn = %+v", h)
	}
}

func TestListenerBackoffGrows(t *testing.T) {
	var mu sync.Mutex
	var sleeps []time.Duration
	sleeper := func(ctx context.Context, d time.Duration) {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
		if ctx.Err() == nil {
			time.Sleep(time.Millisecond)
		}
	}
	steps := make([]step, 4)
	for i := range steps {
		steps[i] = step{err: errors.New("down")}
	}
	fc := &fakeConnector{steps: steps}
	min, max := 10*time.Millisecond, 35*time.Millisecond
	l := newListener(context.Background(), "dsn", Options{Connector: fc, MinBackoff: min, MaxBackoff: max}, sleeper)
	defer l.Close()
	eventually(t, "five Connect calls", func() bool { return fc.Calls() == 5 })
	mu.Lock()
	defer mu.Unlock()
	if len(sleeps) != 4 {
		t.Fatalf("sleeps = %v", sleeps)
	}
	bases := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 35 * time.Millisecond, 35 * time.Millisecond}
	for i, d := range sleeps {
		if d < bases[i]/2 || d > bases[i] {
			t.Errorf("sleep %d = %v, want within [%v, %v]", i, d, bases[i]/2, bases[i])
		}
	}
}

func TestListenerOptionDefaultsAndValidation(t *testing.T) {
	l := New(context.Background(), "dsn", Options{Connector: &fakeConnector{}})
	if l.min != 100*time.Millisecond || l.max != 5*time.Second {
		t.Fatalf("defaults = %v, %v", l.min, l.max)
	}
	l.Close()

	for _, c := range []struct {
		name string
		o    Options
		want string
	}{
		{"negative min", Options{MinBackoff: -time.Millisecond}, "MinBackoff"},
		{"negative max", Options{MaxBackoff: -time.Millisecond}, "MaxBackoff"},
		{"min above max", Options{MinBackoff: 2 * time.Second, MaxBackoff: time.Second}, "MinBackoff"},
		{"default max below min", Options{MinBackoff: 10 * time.Second}, "MaxBackoff"},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("New did not panic")
				}
				if msg := fmt.Sprint(r); !strings.Contains(msg, c.want) {
					t.Fatalf("panic %q does not name %s", msg, c.want)
				}
			}()
			c.o.Connector = &fakeConnector{}
			New(context.Background(), "dsn", c.o)
		})
	}
}

func TestListenerCloseIsIdempotent(t *testing.T) {
	c := newFakeConn()
	fc := &fakeConnector{steps: []step{{c: c}}}
	l := New(context.Background(), "dsn", Options{Connector: fc, MinBackoff: time.Millisecond})
	eventually(t, "connected", l.Connected)
	l.Close()
	l.Close()
	select {
	case <-l.done:
	default:
		t.Fatal("goroutine still running after Close")
	}
	if !c.isClosed() {
		t.Fatal("connection not closed")
	}
	if l.Connected() {
		t.Fatal("Connected after Close")
	}
	n := fc.Calls()
	time.Sleep(20 * time.Millisecond)
	if fc.Calls() != n {
		t.Fatal("Connect called after Close")
	}
}

func TestListenerStartsWithoutServer(t *testing.T) {
	steps := make([]step, 100)
	for i := range steps {
		steps[i] = step{err: errors.New("refused")}
	}
	fc := &fakeConnector{steps: steps}
	start := time.Now()
	l := New(context.Background(), "dsn", Options{Connector: fc, MinBackoff: time.Second, MaxBackoff: 2 * time.Second})
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("New blocked")
	}
	if l.Connected() {
		t.Fatal("Connected without a server")
	}
	hints, cancel := l.Subscribe()
	defer cancel()
	l.Publish(notify.Hint{ChannelID: 5, Seq: 6})
	if h := next(t, hints); h != (notify.Hint{ChannelID: 5, Seq: 6}) {
		t.Fatalf("in-process hint = %+v", h)
	}
	eventually(t, "first Connect", func() bool { return fc.Calls() >= 1 })
	time.Sleep(10 * time.Millisecond) // now mid-backoff
	start = time.Now()
	l.Close()
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("Close took %v mid-backoff", time.Since(start))
	}
}

func TestListenerBadDSNIsRetriedNotReturned(t *testing.T) {
	// The real pgx connector with an unparseable DSN: New still returns.
	l := New(context.Background(), "postgres://%zz", Options{MinBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond})
	if l.Connected() {
		t.Fatal("Connected on a bad DSN")
	}
	l.Close()
}

// A session that reached LISTEN resets the backoff: the sleep after its Wait
// error is drawn from MinBackoff again, not from the grown backoff.
func TestListenerBackoffResetsAfterListen(t *testing.T) {
	var mu sync.Mutex
	var sleeps []time.Duration
	sleeper := func(_ context.Context, d time.Duration) {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
	}
	c := newFakeConn()
	c.events <- event{err: errors.New("dropped")}
	down := errors.New("down")
	fc := &fakeConnector{steps: []step{{err: down}, {err: down}, {err: down}, {c: c}}}
	minB, maxB := 10*time.Millisecond, time.Second
	l := newListener(context.Background(), "dsn", Options{Connector: fc, MinBackoff: minB, MaxBackoff: maxB}, sleeper)
	defer l.Close()
	eventually(t, "fifth Connect call", func() bool { return fc.Calls() == 5 })
	mu.Lock()
	defer mu.Unlock()
	if len(sleeps) != 4 {
		t.Fatalf("sleeps = %v", sleeps)
	}
	bases := []time.Duration{minB, 2 * minB, 4 * minB, minB}
	for i, d := range sleeps {
		if d < bases[i]/2 || d > bases[i] {
			t.Errorf("sleep %d = %v, want within [%v, %v]", i, d, bases[i]/2, bases[i])
		}
	}
}
