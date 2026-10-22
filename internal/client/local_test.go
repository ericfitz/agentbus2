package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/store"
)

// noMerge is core.Defaults with the wake merge window off, so a wake does not
// wait two seconds for more messages.
func noMerge() *core.Config {
	c := core.Defaults()
	c.WatchMergeSeconds = 0
	return &c
}

// openAs opens the default (local) profile of e as identity.
func (e *env) openAs(t *testing.T, identity string, tune ...func(*Options)) *Conn {
	t.Helper()
	o := Options{Environ: e.environ, Cwd: t.TempDir(), Identity: identity, PollInterval: 50 * time.Millisecond, Config: noMerge()}
	for _, f := range tune {
		f(&o)
	}
	c, err := Open(context.Background(), o)
	if err != nil {
		t.Fatalf("Open as %s: %v", identity, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func localEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.save(t, profile.Profile{Name: "default", Mode: profile.ModeLocal})
	return e
}

func TestLocalOpenRegistersAndServes(t *testing.T) {
	ctx := context.Background()
	e := localEnv(t)
	pat, sam := e.openAs(t, "pat"), e.openAs(t, "sam")

	chs, err := pat.Bus.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range chs {
		found = found || c.Name == "general"
	}
	if !found {
		t.Fatalf("channels %+v lack general", chs)
	}

	ps, err := pat.Bus.Open(ctx, ab2bus.OpenSessionInput{Hint: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := pat.Bus.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "general"}); err != nil {
		t.Logf("subscribe general: %v", err)
	}
	if _, err := sam.Bus.Send(ctx, ab2bus.SendInput{Channel: "general", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	r, err := pat.Bus.Receive(ctx, ab2bus.ReceiveInput{Session: ps.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 1 || r.Messages[0].Content != "hello" || r.Messages[0].Sender != "sam" {
		t.Fatalf("receive: %+v", r.Messages)
	}
	if _, err := os.Stat(filepath.Join(e.data, "agentbus2.sqlite")); err != nil {
		t.Fatalf("store file: %v", err)
	}
	if err := pat.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pat.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := sam.Close(); err != nil {
		t.Fatal(err)
	}
	// A third Open as pat is idempotent: the identity exists, general exists.
	again := e.openAs(t, "pat")
	if _, err := again.Bus.ListChannels(ctx); err != nil {
		t.Fatal(err)
	}
}

// subscribed opens a session for c on a fresh channel "dev" subscription and
// returns the session id.
func subscribed(t *testing.T, c *Conn, create bool) string {
	t.Helper()
	ctx := context.Background()
	s, err := c.Bus.Open(ctx, ab2bus.OpenSessionInput{Hint: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if create {
		if _, err := c.Bus.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: "dev"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Bus.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "dev"}); err != nil {
		t.Fatal(err)
	}
	return s.ID
}

type watchOut struct {
	res ab2bus.WatchResult
	err error
	at  time.Time
}

func watchAsync(c *Conn, session string, timeout time.Duration) <-chan watchOut {
	ch := make(chan watchOut, 1)
	go func() {
		r, err := c.Bus.Watch(context.Background(), ab2bus.WatchInput{Session: session, Timeout: timeout})
		ch <- watchOut{r, err, time.Now()}
	}()
	return ch
}

func await(t *testing.T, ch <-chan watchOut, d time.Duration) watchOut {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(d):
		t.Fatalf("watch did not return within %v", d)
		return watchOut{}
	}
}

func TestLocalWatchWakesAcrossConns(t *testing.T) {
	ctx := context.Background()
	e := localEnv(t)
	pat, sam := e.openAs(t, "pat"), e.openAs(t, "sam")
	session := subscribed(t, pat, true)
	w := watchAsync(pat, session, 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	sent := time.Now()
	if _, err := sam.Bus.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	o := await(t, w, 4*time.Second)
	if o.err != nil || !o.res.Woke || o.res.Count != 1 || len(o.res.Channels) != 1 || o.res.Channels[0] != "dev" {
		t.Fatalf("watch: %+v, %v", o.res, o.err)
	}
	if d := o.at.Sub(sent); d > time.Second {
		t.Fatalf("woke %v after the send, want within 1 s", d)
	}
}

// The negative half: a memory notifier on both conns cannot see the other
// process's hint, so the watch times out. This is why production never passes
// a notifier and polls.
func TestLocalWatchMemoryNotifierDoesNotCrossConns(t *testing.T) {
	ctx := context.Background()
	e := localEnv(t)
	mem := func(o *Options) { o.Notifier = notify.NewMemory() }
	pat, sam := e.openAs(t, "pat", mem), e.openAs(t, "sam", mem)
	session := subscribed(t, pat, true)
	w := watchAsync(pat, session, time.Second)
	time.Sleep(200 * time.Millisecond)
	if _, err := sam.Bus.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	// The send landed after the watch armed, so only a hint could wake it.
	o := await(t, w, 3*time.Second)
	if o.err != nil || o.res.Woke {
		t.Fatalf("a memory notifier crossed Conns: %+v, %v, want a timeout", o.res, o.err)
	}
}

func TestLocalReplacedAcrossConns(t *testing.T) {
	e := localEnv(t)
	a, b := e.openAs(t, "pat"), e.openAs(t, "pat")
	session := subscribed(t, a, true)
	wa := watchAsync(a, session, 10*time.Second)
	time.Sleep(300 * time.Millisecond)
	registered := time.Now()
	wb := watchAsync(b, session, time.Second)
	oa := await(t, wa, 3*time.Second)
	var be *ab2bus.Error
	if oa.err == nil || !errors.As(oa.err, &be) || be.Code != ab2bus.CodeReplaced {
		t.Fatalf("watch A: %+v, %v, want replaced", oa.res, oa.err)
	}
	if d := oa.at.Sub(registered); d > 500*time.Millisecond+50*time.Millisecond {
		t.Fatalf("watch A learned of the replacement after %v, want within 500 ms", d)
	}
	ob := await(t, wb, 4*time.Second)
	if ob.err != nil || ob.res.Woke {
		t.Fatalf("watch B: %+v, %v, want a quiet timeout", ob.res, ob.err)
	}
}

func TestLocalMaintenanceRuns(t *testing.T) {
	ctx := context.Background()
	e := localEnv(t)
	cfg := core.Defaults()
	cfg.WatchMergeSeconds = 0
	cfg.MaintenanceIntervalSeconds = 1
	maint := func(o *Options) { o.Maintenance = true; o.Config = &cfg }
	a := e.openAs(t, "pat", maint)
	b := e.openAs(t, "sam", maint)
	err := a.Core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.PutReceipt(ctx, store.Receipt{Identity: "pat", Key: "k", Fingerprint: "f", Result: []byte("{}"), ExpiresAt: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var found bool
		err := a.Core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			_, found, err = tx.Receipt(ctx, "pat", "k", 0)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the expired receipt survived 10 s of maintenance")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Exactly one of the two holds the lease.
	holders := 0
	for _, c := range []*Conn{a, b} {
		var held bool
		now := time.Now().UnixMilli()
		err := c.Core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			held, err = tx.TryLease(ctx, "maintenance", c.Core.Owner, now, now+2000)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if held {
			holders++
		}
	}
	if holders != 1 {
		t.Fatalf("%d conns hold the maintenance lease, want 1", holders)
	}
}

func TestLocalNoMaintenanceByDefault(t *testing.T) {
	ctx := context.Background()
	e := localEnv(t)
	cfg := core.Defaults()
	cfg.MaintenanceIntervalSeconds = 1
	a := e.openAs(t, "pat", func(o *Options) { o.Config = &cfg })
	err := a.Core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.PutReceipt(ctx, store.Receipt{Identity: "pat", Key: "k", Fingerprint: "f", Result: []byte("{}"), ExpiresAt: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	var found bool
	_ = a.Core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, found, _ = tx.Receipt(ctx, "pat", "k", 0)
		return nil
	})
	if !found {
		t.Fatal("a one-shot Conn ran maintenance")
	}
}

func TestLocalConfigDefaultsWhenNil(t *testing.T) {
	e := localEnv(t)
	c := e.openAs(t, "pat", func(o *Options) { o.Config = nil })
	if got, want := c.Core.Cfg.WatchMergeSeconds, core.Defaults().WatchMergeSeconds; got != want {
		t.Fatalf("WatchMergeSeconds %d, want %d", got, want)
	}
	pinned := e.openAs(t, "sam")
	if pinned.Core.Cfg.WatchMergeSeconds != 0 {
		t.Fatalf("pinned WatchMergeSeconds %d", pinned.Core.Cfg.WatchMergeSeconds)
	}
}

func TestLocalRejectsInvalidConfig(t *testing.T) {
	e := localEnv(t)
	bad := core.Defaults()
	bad.SessionLeaseSeconds = -1
	_, err := Open(context.Background(), Options{Environ: e.environ, Cwd: t.TempDir(), Identity: "pat", Config: &bad})
	if err == nil {
		t.Fatal("Open accepted an invalid core config")
	}
}

func TestLocalRejectsBadIdentityName(t *testing.T) {
	e := localEnv(t)
	_, err := Open(context.Background(), Options{Environ: e.environ, Cwd: t.TempDir(), Identity: "a/b"})
	if err == nil {
		t.Fatal("Open accepted identity a/b")
	}
}

// Definition of Done D: a Conn abandoned without Close (a crashed process
// leaves the file and, with Maintenance, a lease) does not stop a new Open.
func TestLocalSecondOpenAfterCrash(t *testing.T) {
	ctx := context.Background()
	e := localEnv(t)
	cfg := core.Defaults()
	cfg.MaintenanceIntervalSeconds = 1
	crashed, err := Open(ctx, Options{Environ: e.environ, Cwd: t.TempDir(), Identity: "pat", Maintenance: true, Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crashed.Bus.Open(ctx, ab2bus.OpenSessionInput{Hint: "x"}); err != nil {
		t.Fatal(err)
	}
	// crashed is never closed.
	next := e.openAs(t, "pat")
	if _, err := next.Bus.Open(ctx, ab2bus.OpenSessionInput{Hint: "y"}); err != nil {
		t.Fatalf("Open after the abandoned Conn: %v", err)
	}
	t.Cleanup(func() { _ = crashed.Close() })
}

// A local process that stops releases the maintenance lock, so the next
// process takes it at once instead of waiting out the lease.
func TestLocalMaintenanceReleasesLockOnClose(t *testing.T) {
	ctx := context.Background()
	e := localEnv(t)
	cfg := core.Defaults()
	cfg.WatchMergeSeconds = 0
	cfg.MaintenanceIntervalSeconds = 1
	a := e.openAs(t, "pat", func(o *Options) { o.Maintenance = true; o.Config = &cfg })
	err := a.Core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.PutReceipt(ctx, store.Receipt{Identity: "pat", Key: "k", Fingerprint: "f", Result: []byte("{}"), ExpiresAt: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var found bool
		err := a.Core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			_, found, err = tx.Receipt(ctx, "pat", "k", 0)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("maintenance did not tick within 10 s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b := e.openAs(t, "sam")
	now := time.Now().UnixMilli()
	held, err := b.Core.Store.TryLock(ctx, "maintenance", "someone-else", now, now+60_000)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("the maintenance lock is still held after the holder closed")
	}
}
