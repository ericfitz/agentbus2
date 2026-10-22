package tui

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/local"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// busFixture is the package-level test fixture: a core over SQLite, an
// identity "sam" with channels dev (ordinary) and dev-notes (memory), and the
// TUI's client as "eric". It never names Model (Task 3), so it compiles
// before the model exists.
type busFixture struct {
	t          *testing.T
	core       *core.Core
	c          *client
	bus        bus.Bus // the TUI's own bus, as eric
	sam        bus.Bus
	samSession bus.Session
	ack        string // batch token of the last receiveRaw (mirrors the loop's prev)
}

// newCore opens SQLite on t.TempDir() and builds a core with the general
// channel and WatchMergeSeconds 0 (else a wake sleeps 2 s).
func newCore(t *testing.T) *core.Core {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "bus.sqlite"), sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	cfg := core.Defaults()
	cfg.WatchMergeSeconds = 0
	c, err := core.New(st, cfg, core.Options{Notifier: notify.NewMemory()})
	if err != nil {
		_ = st.Close()
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := (channels.Service{Core: c}).EnsureGeneral(ctx); err != nil {
		t.Fatalf("EnsureGeneral: %v", err)
	}
	return c
}

// ensureIdentity creates the identity through the raw store path, which
// creates no general subscription (unlike identities.Service.CreateIdentity),
// so every mark in a test is set by the test itself.
func ensureIdentity(t *testing.T, c *core.Core, name string) {
	t.Helper()
	err := c.Store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		_, err := tx.Identity(ctx, name)
		if errors.Is(err, store.ErrNotFound) {
			return tx.CreateIdentity(ctx, name, c.NowMs())
		}
		return err
	})
	if err != nil {
		t.Fatalf("ensureIdentity(%q): %v", name, err)
	}
}

// identityBus is ensureIdentity, then a core-direct Bus bound to name.
func identityBus(t *testing.T, c *core.Core, name string) bus.Bus {
	t.Helper()
	ensureIdentity(t, c, name)
	return local.New(c, name)
}

// pendingFor probes how many messages wait for the identity of b on ch: it
// opens a session without a hint or token, closes it at once and returns the
// Pending entry. An absent entry means "not subscribed" and fails the test:
// reading it as 0 would let an ack assertion pass vacuously.
func pendingFor(t *testing.T, b bus.Bus, ch string) int64 {
	t.Helper()
	ctx := context.Background()
	s, err := b.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatalf("pendingFor open: %v", err)
	}
	defer func() { _ = b.Close(ctx, bus.CloseSessionInput{Session: s.ID}) }()
	for _, p := range s.Pending {
		if p.Channel == ch {
			return p.Pending
		}
	}
	t.Fatalf("pendingFor: %q is not in Session.Pending %v", ch, s.Pending)
	return 0
}

// setAdmin flags name as an administrator.
func setAdmin(t *testing.T, c *core.Core, name string) {
	t.Helper()
	ensureIdentity(t, c, name)
	err := c.Store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.SetAdmin(ctx, name, true)
	})
	if err != nil {
		t.Fatalf("setAdmin(%q): %v", name, err)
	}
}

// newBusFixture builds the core, sam with channels dev and dev-notes
// (subscribed from "oldest": nothing on main subscribes a creator or a new
// identity to anything) and an open session, and the TUI's client as eric.
func newBusFixture(t *testing.T) *busFixture {
	t.Helper()
	ctx := context.Background()
	c := newCore(t)
	sam := identityBus(t, c, "sam")
	for _, in := range []bus.CreateChannelInput{
		{Name: "dev"},
		{Name: "dev-notes", Kind: bus.KindMemory},
	} {
		if _, err := sam.CreateChannel(ctx, in); err != nil {
			t.Fatalf("CreateChannel %q: %v", in.Name, err)
		}
	}
	for _, ch := range []string{"general", "dev", "dev-notes"} {
		if err := sam.Subscribe(ctx, bus.SubscribeInput{Channel: ch, From: "oldest"}); err != nil {
			t.Fatalf("sam Subscribe %q: %v", ch, err)
		}
	}
	ss, err := sam.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatalf("sam Open: %v", err)
	}
	eric := identityBus(t, c, "eric")
	cl, err := newClient(ctx, eric, "eric", "")
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	t.Cleanup(func() { _ = cl.close() })
	return &busFixture{t: t, core: c, c: cl, bus: eric, sam: sam, samSession: ss}
}

// samSend sends as sam's session.
func (f *busFixture) samSend(ch, content string, tags ...string) bus.SendResult {
	f.t.Helper()
	r, err := f.sam.Send(context.Background(), bus.SendInput{Channel: ch, Content: content, Tags: tags, Session: f.samSession.ID})
	if err != nil {
		f.t.Fatalf("samSend %q: %v", ch, err)
	}
	return r
}

// samReply sends a reply to message to as sam's session (v1 agentReply).
func (f *busFixture) samReply(ch string, to int64, content string) bus.SendResult {
	f.t.Helper()
	r, err := f.sam.Send(context.Background(), bus.SendInput{Channel: ch, Content: content, ReplyTo: &to, Session: f.samSession.ID})
	if err != nil {
		f.t.Fatalf("samReply %q: %v", ch, err)
	}
	return r
}

// receiveRaw makes one Receive as the loop would and records the new batch
// token in f.ack.
func (f *busFixture) receiveRaw() bus.ReceiveResult {
	f.t.Helper()
	res, err := f.bus.Receive(context.Background(), bus.ReceiveInput{Session: f.c.sessionID(), Ack: f.ack, IncludeOwn: true})
	if err != nil {
		f.t.Fatalf("receiveRaw: %v", err)
	}
	f.ack = res.Batch
	return res
}

// waitFor polls cond every 10 ms and fails after d.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %v", d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stubSessions holds the per-method doubles of stubBus.
type stubSessions struct {
	open    func(context.Context, bus.OpenSessionInput) (bus.Session, error)
	watch   func(context.Context, bus.WatchInput) (bus.WatchResult, error)
	receive func(context.Context, bus.ReceiveInput) (bus.ReceiveResult, error)
	closeFn func(context.Context, bus.CloseSessionInput) error
}

// stubBus is a bus.Bus double for the loop tests: the Sessions methods go to
// the stubs and everything else panics on the nil embedded Bus (never
// reached).
type stubBus struct {
	bus.Bus
	stubSessions
}

func (s *stubBus) Open(ctx context.Context, in bus.OpenSessionInput) (bus.Session, error) {
	return s.open(ctx, in)
}

func (s *stubBus) Watch(ctx context.Context, in bus.WatchInput) (bus.WatchResult, error) {
	return s.watch(ctx, in)
}

func (s *stubBus) Receive(ctx context.Context, in bus.ReceiveInput) (bus.ReceiveResult, error) {
	return s.receive(ctx, in)
}

func (s *stubBus) Close(ctx context.Context, in bus.CloseSessionInput) error {
	if s.closeFn == nil {
		return nil
	}
	return s.closeFn(ctx, in)
}

// newStubClient builds a client over a stubBus with an already-open session,
// without the Open and ListChannels of newClient.
func newStubClient(t *testing.T, sb *stubBus, s bus.Session) *client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := &client{b: sb, identity: s.Identity, ctx: ctx, cancel: cancel, session: s, subscribed: map[string]subState{}}
	t.Cleanup(cancel)
	return c
}

// countingBus wraps a real bus and counts the calls the TUI must not repeat
// or must not make.
type countingBus struct {
	bus.Bus
	mu       sync.Mutex
	calls    map[string]int
	channels map[string][]string
}

func newCountingBus(b bus.Bus) *countingBus {
	return &countingBus{Bus: b, calls: map[string]int{}, channels: map[string][]string{}}
}

func (b *countingBus) note(name, ch string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls[name]++
	if ch != "" {
		b.channels[name] = append(b.channels[name], ch)
	}
}

// count is the number of calls of the named method.
func (b *countingBus) count(name string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[name]
}

// channelsOf lists the channel of every Subscribe or History call, in order.
func (b *countingBus) channelsOf(name string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.channels[name]...)
}

func (b *countingBus) ListChannels(ctx context.Context) ([]bus.Channel, error) {
	b.note("ListChannels", "")
	return b.Bus.ListChannels(ctx)
}

func (b *countingBus) Identities(ctx context.Context) ([]bus.IdentityInfo, error) {
	b.note("Identities", "")
	return b.Bus.Identities(ctx)
}

func (b *countingBus) CreateChannel(ctx context.Context, in bus.CreateChannelInput) (bus.ChannelInfo, error) {
	b.note("CreateChannel", "")
	return b.Bus.CreateChannel(ctx, in)
}

func (b *countingBus) Subscribe(ctx context.Context, in bus.SubscribeInput) error {
	b.note("Subscribe", in.Channel)
	return b.Bus.Subscribe(ctx, in)
}

func (b *countingBus) History(ctx context.Context, in bus.HistoryInput) ([]bus.Message, error) {
	b.note("History", in.Channel)
	return b.Bus.History(ctx, in)
}

func (b *countingBus) Status(ctx context.Context) (bus.Status, error) {
	b.note("Status", "")
	return b.Bus.Status(ctx)
}
