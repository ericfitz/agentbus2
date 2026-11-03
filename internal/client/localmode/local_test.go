package localmode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/testkit"
)

// noMerge is core.Defaults with the wake merge window off, so a wake does not
// wait two seconds for more messages.
func noMerge() *core.Config {
	c := core.Defaults()
	c.WatchMergeSeconds = 0
	return &c
}

// opts are the client.Options of a test plus the core.Config the opener runs
// (nil: core.Defaults()).
type opts struct {
	client.Options
	Config *core.Config
}

func (o opts) open(ctx context.Context) (*client.Conn, error) {
	o.Local = Opener(Options{Config: o.Config})
	return client.Open(ctx, o.Options)
}

// openAs opens the default (local) profile of e as identity.
func openAs(t *testing.T, e *testkit.ProfileEnv, identity string, tune ...func(*opts)) *client.Conn {
	t.Helper()
	o := opts{Options: client.Options{Environ: e.Environ, Cwd: t.TempDir(), Identity: identity, PollInterval: 50 * time.Millisecond}, Config: noMerge()}
	for _, f := range tune {
		f(&o)
	}
	c, err := o.open(context.Background())
	if err != nil {
		t.Fatalf("Open as %s: %v", identity, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func localEnv(t *testing.T) *testkit.ProfileEnv {
	t.Helper()
	e := testkit.NewProfileEnv(t)
	e.Save(t, profile.Profile{Name: "default", Mode: profile.ModeLocal})
	return e
}

func TestLocalOpenRegistersAndServes(t *testing.T) {
	ctx := context.Background()
	e := localEnv(t)
	pat, sam := openAs(t, e, "pat"), openAs(t, e, "sam")

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
	if _, err := os.Stat(filepath.Join(e.Data, "agentbus2.sqlite")); err != nil {
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
	again := openAs(t, e, "pat")
	if _, err := again.Bus.ListChannels(ctx); err != nil {
		t.Fatal(err)
	}
}

// subscribed opens a session for c on a fresh channel "dev" subscription and
// returns the session id.
func subscribed(t *testing.T, c *client.Conn, create bool) string {
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

func watchAsync(c *client.Conn, session string, timeout time.Duration) <-chan watchOut {
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
	pat, sam := openAs(t, e, "pat"), openAs(t, e, "sam")
	session := subscribed(t, pat, true)
	w := watchAsync(pat, session, 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	sent := time.Now()
	if _, err := sam.Bus.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	o := await(t, w, 6*time.Second)
	if o.err != nil || !o.res.Woke || o.res.Count != 1 || len(o.res.Channels) != 1 || o.res.Channels[0] != "dev" {
		t.Fatalf("watch: %+v, %v", o.res, o.err)
	}
	// 3 s, not 1: wake bounds flake on loaded Windows runners (see
	// TestWatchWakesAcrossProcesses). The Conns poll every 50 ms and the watch
	// times out at 5 s, so only a polling wake passes.
	if d := o.at.Sub(sent); d > 3*time.Second {
		t.Fatalf("woke %v after the send, want within 3 s", d)
	}
}

// The negative half: a memory notifier on both conns cannot see the other
// process's hint, so the watch times out. This is why production never passes
// a notifier and polls.
func TestLocalWatchMemoryNotifierDoesNotCrossConns(t *testing.T) {
	ctx := context.Background()
	e := localEnv(t)
	mem := func(o *opts) { o.Notifier = notify.NewMemory() }
	pat, sam := openAs(t, e, "pat", mem), openAs(t, e, "sam", mem)
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
	a, b := openAs(t, e, "pat"), openAs(t, e, "pat")
	session := subscribed(t, a, true)
	wa := watchAsync(a, session, 10*time.Second)
	time.Sleep(300 * time.Millisecond)
	registered := time.Now()
	wb := watchAsync(b, session, time.Second)
	oa := await(t, wa, 6*time.Second)
	var be *ab2bus.Error
	if oa.err == nil || !errors.As(oa.err, &be) || be.Code != ab2bus.CodeReplaced {
		t.Fatalf("watch A: %+v, %v, want replaced", oa.res, oa.err)
	}
	// 3 s, not 0.5: the Conns poll every 50 ms; watch A would otherwise run to
	// its 10 s timeout.
	if d := oa.at.Sub(registered); d > 3*time.Second {
		t.Fatalf("watch A learned of the replacement after %v, want within 3 s", d)
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
	maint := func(o *opts) { o.Maintenance = true; o.Config = &cfg }
	a := openAs(t, e, "pat", maint)
	b := openAs(t, e, "sam", maint)
	err := Core(a).Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.PutReceipt(ctx, store.Receipt{Identity: "pat", Key: "k", Fingerprint: "f", Result: []byte("{}"), ExpiresAt: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var found bool
		err := Core(a).Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
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
	for _, c := range []*client.Conn{a, b} {
		var held bool
		now := time.Now().UnixMilli()
		err := Core(c).Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			held, err = tx.TryLease(ctx, "maintenance", Core(c).Owner, now, now+2000)
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
	a := openAs(t, e, "pat", func(o *opts) { o.Config = &cfg })
	err := Core(a).Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.PutReceipt(ctx, store.Receipt{Identity: "pat", Key: "k", Fingerprint: "f", Result: []byte("{}"), ExpiresAt: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	var found bool
	_ = Core(a).Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, found, _ = tx.Receipt(ctx, "pat", "k", 0)
		return nil
	})
	if !found {
		t.Fatal("a one-shot Conn ran maintenance")
	}
}

func TestLocalConfigDefaultsWhenNil(t *testing.T) {
	e := localEnv(t)
	c := openAs(t, e, "pat", func(o *opts) { o.Config = nil })
	if got, want := Core(c).Cfg.WatchMergeSeconds, core.Defaults().WatchMergeSeconds; got != want {
		t.Fatalf("WatchMergeSeconds %d, want %d", got, want)
	}
	pinned := openAs(t, e, "sam")
	if Core(pinned).Cfg.WatchMergeSeconds != 0 {
		t.Fatalf("pinned WatchMergeSeconds %d", Core(pinned).Cfg.WatchMergeSeconds)
	}
}

func TestCoreOfServerConnIsNil(t *testing.T) {
	if c := Core(client.NewConn(nil, client.Resolved{}, nil, nil, nil)); c != nil {
		t.Fatalf("Core of a Conn no local opener built = %v, want nil", c)
	}
}

func TestLocalRejectsInvalidConfig(t *testing.T) {
	e := localEnv(t)
	bad := core.Defaults()
	bad.SessionLeaseSeconds = -1
	_, err := opts{Options: client.Options{Environ: e.Environ, Cwd: t.TempDir(), Identity: "pat"}, Config: &bad}.open(context.Background())
	if err == nil {
		t.Fatal("Open accepted an invalid core config")
	}
}

func TestLocalRejectsBadIdentityName(t *testing.T) {
	e := localEnv(t)
	_, err := opts{Options: client.Options{Environ: e.Environ, Cwd: t.TempDir(), Identity: "a/b"}}.open(context.Background())
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
	crashed, err := opts{Options: client.Options{Environ: e.Environ, Cwd: t.TempDir(), Identity: "pat", Maintenance: true}, Config: &cfg}.open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crashed.Bus.Open(ctx, ab2bus.OpenSessionInput{Hint: "x"}); err != nil {
		t.Fatal(err)
	}
	// crashed is never closed.
	next := openAs(t, e, "pat")
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
	a := openAs(t, e, "pat", func(o *opts) { o.Maintenance = true; o.Config = &cfg })
	err := Core(a).Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.PutReceipt(ctx, store.Receipt{Identity: "pat", Key: "k", Fingerprint: "f", Result: []byte("{}"), ExpiresAt: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var found bool
		err := Core(a).Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
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
	b := openAs(t, e, "sam")
	now := time.Now().UnixMilli()
	held, err := Core(b).Store.TryLock(ctx, "maintenance", "someone-else", now, now+60_000)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("the maintenance lock is still held after the holder closed")
	}
}

func identityKind(t *testing.T, c *client.Conn, name string) store.IdentityKind {
	t.Helper()
	var k store.IdentityKind
	err := Core(c).Store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		id, err := tx.Identity(ctx, name)
		k = id.Kind
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestOpenLocalMarksOSUserHuman(t *testing.T) {
	e := localEnv(t)
	human := func(o *opts) { o.Identity, o.FallbackIdentity, o.HumanIdentity = "", "eric", true }
	for i := 0; i < 2; i++ { // a second open changes nothing
		c := openAs(t, e, "", human)
		if c.Resolved.IdentitySource != "os-user" {
			t.Fatalf("source = %q", c.Resolved.IdentitySource)
		}
		if k := identityKind(t, c, "eric"); k != store.IdentityHuman {
			t.Fatalf("OS-login kind = %q, want human", k)
		}
	}
	// A named identity is never turned human, even for the TUI.
	c := openAs(t, e, "agentx", func(o *opts) { o.FallbackIdentity, o.HumanIdentity = "eric", true })
	if k := identityKind(t, c, "agentx"); k != store.IdentityAgent {
		t.Fatalf("agentx kind = %q, want agent", k)
	}
	// Without HumanIdentity the fallback is an ordinary agent.
	c = openAs(t, e, "", func(o *opts) { o.Identity, o.FallbackIdentity = "", "plain" })
	if k := identityKind(t, c, "plain"); k != store.IdentityAgent {
		t.Fatalf("plain kind = %q, want agent", k)
	}
}

func TestOpenLocalOSUserWithKeysRefused(t *testing.T) {
	e := localEnv(t)
	c := openAs(t, e, "eric")
	pub, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := keys.MarshalPublic(pub.Public())
	kid, _ := keys.KID(pub.Public())
	err = Core(c).Store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.PutPublicKey(ctx, store.PublicKey{Identity: "eric", KID: kid, Algorithm: string(keys.EdDSA), Key: der, CreatedAt: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	o := opts{Options: client.Options{Environ: e.Environ, Cwd: t.TempDir(), FallbackIdentity: "eric", HumanIdentity: true, PollInterval: 50 * time.Millisecond}, Config: noMerge()}
	_, err = o.open(context.Background())
	var re *client.ResolveError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), `identity "eric" holds keys`) || !strings.Contains(err.Error(), "pass -identity") {
		t.Fatalf("Open = %v, want a ResolveError naming eric", err)
	}
}

func TestHumanRegisterErrorMapping(t *testing.T) {
	// The typed error, not message text, selects the mapping.
	holds := fmt.Errorf("register: %w", identities.ErrHoldsKeys)
	var re *client.ResolveError
	if err := humanRegisterError("eric", holds); !errors.As(err, &re) || !strings.Contains(err.Error(), "pass -identity") {
		t.Fatalf("holds-keys = %v, want a ResolveError", err)
	}
	// A conflict from exhausted reruns is not the user's to fix with -identity.
	// A conflict that merely reads like it is not matched either.
	lookalike := core.E(ab2bus.CodeConflict, "identity %q holds keys, so it cannot be a human", "eric")
	for _, other := range []error{core.FromStore(store.ErrConflict), errors.New("disk full"), lookalike} {
		err := humanRegisterError("eric", other)
		if errors.As(err, &re) || strings.Contains(err.Error(), "pass -identity") {
			t.Fatalf("%v mapped to %v", other, err)
		}
	}
}
