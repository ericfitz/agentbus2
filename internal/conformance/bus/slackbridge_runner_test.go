package bus

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/maintenance"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/slackbridge"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// bridgeCfg is the Config of the runner tests: fast pacing and renewal.
func bridgeCfg(c *slackbridge.Config) {
	c.PostInterval = time.Millisecond
	c.LockRenew = 50 * time.Millisecond
	c.LifecycleInterval = 20 * time.Millisecond
	c.RetryBase = 5 * time.Millisecond
}

func newOutFix(t *testing.T, e dbtest.Engine, mod func(*slackbridge.Config)) *inFix {
	t.Helper()
	return newInFixOpts(t, e, harnessOptions{}, func(c *slackbridge.Config) {
		bridgeCfg(c)
		if mod != nil {
			mod(c)
		}
	})
}

// send writes a bus message as sender and answers its seq.
func (f *inFix) send(sender string, in ab2bus.SendInput) int64 {
	f.h.t.Helper()
	in.Channel = "job1-chat"
	res, err := newBus(f.h, sender).Send(context.Background(), in)
	if err != nil {
		f.h.t.Fatalf("Send as %s: %v", sender, err)
	}
	return res.Seq
}

func (f *inFix) drainAll() {
	f.h.t.Helper()
	if err := f.svc.Drain(context.Background()); err != nil {
		f.h.t.Fatalf("Drain: %v", err)
	}
}

// bridged lists the posts that carry a bus seq (the notices have none), in
// the order Slack received them.
func (f *inFix) bridged() []slackapi.PostInput {
	f.h.t.Helper()
	ch, ok := f.fake.Channel(f.sl)
	if !ok {
		f.h.t.Fatal("no Slack channel")
	}
	var out []slackapi.PostInput
	for _, p := range ch.Posts {
		if p.Seq != 0 {
			out = append(out, p)
		}
	}
	return out
}

// acked is the slack-bridge subscription's mark on the channel.
func (f *inFix) acked() int64 {
	f.h.t.Helper()
	var mark int64 = -1
	f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		subs, err := tx.Subscriptions(ctx, ab2store.SlackBridge)
		for _, s := range subs {
			if s.ChannelID == f.ch.ID {
				mark = s.AckedSeq
			}
		}
		return err
	})
	return mark
}

func (f *inFix) mapBySeq(seq int64) (ab2store.BridgeMessage, bool) {
	f.h.t.Helper()
	var row ab2store.BridgeMessage
	err := f.h.raw.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		row, err = tx.BridgeMessageBySeq(ctx, f.bridge().ID, seq)
		return err
	})
	if errors.Is(err, ab2store.ErrNotFound) {
		return row, false
	}
	if err != nil {
		f.h.t.Fatalf("BridgeMessageBySeq: %v", err)
	}
	return row, true
}

func TestSlackBridgeOutbound(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		t.Run("agent-message-posted", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			seq := f.send("job1-coder", ab2bus.SendInput{Subject: "Plan", Tags: []string{"build"}, Content: "**done**"})
			f.drainAll()
			got := f.bridged()
			if len(got) != 1 {
				t.Fatalf("posts = %+v", got)
			}
			p := got[0]
			if p.Seq != seq || p.Username != "coder" || p.IconEmoji != slackbridge.IconEmoji("job1-coder") || p.ThreadTS != "" ||
				!strings.HasPrefix(p.Section, "*Plan*") || len(p.Context) != 1 || p.Context[0] != "`build`" {
				t.Errorf("post = %+v", p)
			}
			row, ok := f.mapBySeq(seq)
			if !ok || row.RootTS != row.SlackTS || row.SlackTS == "" {
				t.Errorf("map row = %+v, %v", row, ok)
			}
			if f.acked() != seq {
				t.Errorf("acked = %d, want %d", f.acked(), seq)
			}
			// A second drain posts nothing.
			f.drainAll()
			if n := len(f.bridged()); n != 1 {
				t.Errorf("posts after a second drain = %d", n)
			}
		})

		t.Run("reply-threaded", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			root := f.send("job1-coder", ab2bus.SendInput{Content: "root"})
			f.drainAll()
			row, _ := f.mapBySeq(root)
			f.send("job1-coder", ab2bus.SendInput{Content: "reply", ReplyTo: &root})
			f.drainAll()
			got := f.bridged()
			if len(got) != 2 || got[1].ThreadTS != row.SlackTS {
				t.Fatalf("posts = %+v, root ts %s", got, row.SlackTS)
			}
			if r2, ok := f.mapBySeq(got[1].Seq); !ok || r2.RootTS != row.SlackTS {
				t.Errorf("reply map row = %+v, %v", r2, ok)
			}
		})

		t.Run("unmapped-parent-quoted", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			parent := f.send("job1-coder", ab2bus.SendInput{Content: "parent text"})
			// The parent predates the bridge as far as the mark goes.
			f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				return tx.AdvanceAckedMark(ctx, ab2store.SlackBridge, f.ch.ID, parent)
			})
			f.send("job1-coder", ab2bus.SendInput{Content: "child", ReplyTo: &parent})
			f.drainAll()
			got := f.bridged()
			if len(got) != 1 || got[0].ThreadTS != "" || !strings.Contains(got[0].Section, "> parent text") {
				t.Fatalf("posts = %+v", got)
			}
		})

		t.Run("human-origin-not-echoed", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			m := f.fake.Say(f.sl, "U1", "from slack", "")
			f.want(m, "sent")
			seq := f.human()[0].Seq
			f.drainAll()
			if n := len(f.bridged()); n != 0 {
				t.Errorf("a human's message was posted back: %+v", f.bridged())
			}
			if f.acked() < seq {
				t.Errorf("acked = %d, want at least %d", f.acked(), seq)
			}
		})

		t.Run("notices-not-echoed", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			var notice int64
			for _, m := range f.messages(f.ch) {
				if m.Sender == ab2store.SlackBridge {
					notice = m.Seq
				}
			}
			if notice == 0 {
				t.Fatal("the create notice is not on the bus")
			}
			before, _ := f.fake.Channel(f.sl)
			f.drainAll()
			after, _ := f.fake.Channel(f.sl)
			if len(after.Posts) != len(before.Posts) {
				t.Errorf("the notice was posted again: %+v", after.Posts[len(before.Posts):])
			}
			if f.acked() < notice {
				t.Errorf("acked = %d, want at least %d", f.acked(), notice)
			}
		})

		t.Run("memory-not-posted", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			seq := f.send("job1-coder", ab2bus.SendInput{Content: "visible"})
			// A memory-kind row on a bridged channel (impossible through the
			// Bus, which refuses memory channels) is skipped, not posted.
			var mem int64
			id := int64(77)
			err := f.h.raw.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				var err error
				mem, err = tx.AppendMessage(ctx, ab2store.NewMessage{ChannelID: f.ch.ID, Sender: "job1-coder", CreatedAt: 1, Content: "memory", MemoryID: &id})
				return err
			})
			if err != nil {
				t.Fatalf("append: %v", err)
			}
			f.drainAll()
			got := f.bridged()
			if len(got) != 1 || got[0].Seq != seq || f.acked() != mem {
				t.Errorf("posts = %+v, acked = %d, memory seq %d", got, f.acked(), mem)
			}
		})

		t.Run("truncated", func(t *testing.T) {
			f := newOutFix(t, e, func(c *slackbridge.Config) { c.MaxMessageChars = 200 })
			seq := f.send("job1-coder", ab2bus.SendInput{Content: strings.Repeat("word ", 400)})
			f.drainAll()
			got := f.bridged()
			if len(got) != 1 {
				t.Fatalf("posts = %+v", got)
			}
			if n := len([]rune(got[0].Section)); n > 200 || !strings.HasSuffix(got[0].Section, "the full message is seq "+itoa(seq)+" on agentbus2 channel job1-chat)") {
				t.Errorf("section has %d runes: %q", n, got[0].Section)
			}
		})

		t.Run("rate-limited-drain", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			var seqs []int64
			for i := 0; i < 5; i++ {
				seqs = append(seqs, f.send("job1-coder", ab2bus.SendInput{Content: "m" + itoa(int64(i))}))
			}
			f.fake.RateLimit("Post", 3, 10*time.Millisecond)
			f.drainAll()
			got := f.bridged()
			if len(got) != 5 {
				t.Fatalf("posts = %d, want 5: %+v", len(got), got)
			}
			for i, p := range got {
				if p.Seq != seqs[i] {
					t.Errorf("post %d has seq %d, want %d", i, p.Seq, seqs[i])
				}
			}
			if f.acked() != seqs[4] {
				t.Errorf("acked = %d, want %d", f.acked(), seqs[4])
			}
		})

		t.Run("failed-post-does-not-advance", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			seq := f.send("job1-coder", ab2bus.SendInput{Content: "one"})
			start := f.acked() // the create notice is already behind the mark
			f.fake.FailNext("Post", &slackapi.APIError{Method: "Post", Code: "internal_error"})
			if err := f.svc.Drain(context.Background()); err == nil {
				t.Error("Drain hid the failed post")
			}
			if f.acked() != seq-1 || f.acked() < start || len(f.bridged()) != 0 {
				t.Errorf("acked = %d (was %d), posts = %+v", f.acked(), start, f.bridged())
			}
			f.drainAll()
			if got := f.bridged(); len(got) != 1 || got[0].Seq != seq || f.acked() != seq {
				t.Errorf("after the retry: posts = %+v, acked = %d", got, f.acked())
			}
		})

		t.Run("archived-by-hand", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			f.fake.ArchiveByHand(f.sl)
			f.send("job1-coder", ab2bus.SendInput{Content: "into the void"})
			f.drainAll()
			if b := f.bridge(); b.State != ab2store.BridgeArchived {
				t.Fatalf("bridge state = %s, want archived", b.State)
			}
			n := f.fake.Calls("Post")
			f.drainAll()
			f.send("job1-coder", ab2bus.SendInput{Content: "again"})
			f.drainAll()
			if got := f.fake.Calls("Post"); got != n {
				t.Errorf("Post called %d more times after the bridge was archived", got-n)
			}
		})
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// runBridge starts svc.Run and answers a stop that waits for it to return.
func runBridge(t *testing.T, svc *slackbridge.Service) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := svc.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("Run did not return")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// noUnlockStore is a Store whose Unlock does nothing: a runner killed
// without releasing its lock.
type noUnlockStore struct{ ab2store.Store }

func (noUnlockStore) Unlock(context.Context, string, string) error { return nil }

func (f *inFix) newService(h *harness, mod func(*slackbridge.Config)) *slackbridge.Service {
	f.h.t.Helper()
	cfg := slackbridge.Config{ArchiveAfterDone: time.Hour, MaxMessageChars: 3000, Logger: f.svcLogger()}
	bridgeCfg(&cfg)
	if mod != nil {
		mod(&cfg)
	}
	svc, err := slackbridge.New(context.Background(), h.core, f.fake, cfg)
	if err != nil {
		f.h.t.Fatalf("New: %v", err)
	}
	return svc
}

func (f *inFix) svcLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (f *inFix) humanTexts() []string {
	var out []string
	for _, m := range f.human() {
		out = append(out, m.Content)
	}
	return out
}

func TestSlackBridgeRunner(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		t.Run("one-holder", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			h2 := f.h.sibling(harnessOptions{})
			svc2 := f.newService(h2, nil)
			stop1, stop2 := runBridge(t, f.svc), runBridge(t, svc2)
			waitFor(t, 10*time.Second, "a socket", func() bool { return f.fake.MaxConcurrentSockets() >= 1 })
			time.Sleep(time.Second)
			if n := f.fake.MaxConcurrentSockets(); n != 1 {
				t.Errorf("MaxConcurrentSockets = %d, want 1", n)
			}
			stop1()
			stop2()
		})

		t.Run("takeover-catches-up", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			h1 := f.h.sibling(harnessOptions{})
			c1, err := core.New(noUnlockStore{h1.raw}, h1.cfg, core.Options{Now: h1.clock.Now, Notifier: h1.notifier})
			if err != nil {
				t.Fatal(err)
			}
			svc1, err := slackbridge.New(context.Background(), c1, f.fake, slackbridge.Config{
				ArchiveAfterDone: time.Hour, MaxMessageChars: 3000, PostInterval: time.Millisecond,
				LockRenew: 50 * time.Millisecond, LifecycleInterval: 20 * time.Millisecond, RetryBase: 5 * time.Millisecond, Logger: f.svcLogger(),
			})
			if err != nil {
				t.Fatal(err)
			}
			stop1 := runBridge(t, svc1)
			waitFor(t, 10*time.Second, "the first runner's socket", func() bool { return f.fake.MaxConcurrentSockets() == 1 })
			stop1()
			// The first runner dies holding its lock: its Postgres session is
			// gone at once; its SQLite lease runs out with the clock.
			_ = h1.raw.Close()

			var seqs []int64
			for _, c := range []string{"b1", "b2", "b3"} {
				seqs = append(seqs, f.send("job1-coder", ab2bus.SendInput{Content: c}))
			}
			f.fake.DropNext(2)
			f.fake.Say(f.sl, "U1", "s1", "")
			f.fake.Say(f.sl, "U1", "s2", "")

			h2 := f.h.sibling(harnessOptions{})
			h2.clock.Advance(time.Second)
			svc2 := f.newService(h2, nil)
			runBridge(t, svc2)
			waitFor(t, 15*time.Second, "the takeover", func() bool { return len(f.bridged()) == 3 && len(f.human()) == 2 })
			time.Sleep(300 * time.Millisecond)
			got := f.bridged()
			if len(got) != 3 || len(f.human()) != 2 {
				t.Fatalf("posts = %+v, human = %v", got, f.humanTexts())
			}
			for i, p := range got {
				if p.Seq != seqs[i] {
					t.Errorf("post %d has seq %d, want %d", i, p.Seq, seqs[i])
				}
			}
			if f.fake.MaxConcurrentSockets() != 1 {
				t.Errorf("MaxConcurrentSockets = %d", f.fake.MaxConcurrentSockets())
			}
		})

		t.Run("disconnect-catches-up", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			runBridge(t, f.svc)
			waitFor(t, 10*time.Second, "the socket", func() bool { return f.fake.MaxConcurrentSockets() == 1 })
			time.Sleep(200 * time.Millisecond)
			f.fake.DropNext(1)
			f.fake.Say(f.sl, "U1", "lost event", "")
			f.fake.Disconnect()
			waitFor(t, 10*time.Second, "the catch-up", func() bool { return len(f.human()) == 1 })
			time.Sleep(300 * time.Millisecond)
			if got := f.humanTexts(); len(got) != 1 || got[0] != "lost event" {
				t.Errorf("human messages = %v", got)
			}
		})

		t.Run("repeat-and-reorder", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			runBridge(t, f.svc)
			waitFor(t, 10*time.Second, "the socket", func() bool { return f.fake.MaxConcurrentSockets() == 1 })
			f.fake.RepeatNext()
			f.fake.Say(f.sl, "U1", "twice", "")
			f.fake.ReorderNext(3)
			for _, s := range []string{"r1", "r2", "r3"} {
				f.fake.Say(f.sl, "U1", s, "")
			}
			waitFor(t, 15*time.Second, "four messages", func() bool { return len(f.human()) == 4 })
			time.Sleep(300 * time.Millisecond)
			seen := map[string]int{}
			for _, s := range f.humanTexts() {
				seen[s]++
			}
			if len(f.human()) != 4 || seen["twice"] != 1 || seen["r1"] != 1 || seen["r2"] != 1 || seen["r3"] != 1 {
				t.Errorf("human messages = %v", f.humanTexts())
			}
		})

		t.Run("retry-owned-by-runner", func(t *testing.T) {
			cfg := core0()
			f := newInFixOpts(t, e, harnessOptions{cfg: &cfg}, func(c *slackbridge.Config) {
				bridgeCfg(c)
				c.RetryBase = 100 * time.Millisecond // retries reach past the clock step below
			})
			runBridge(t, f.svc)
			waitFor(t, 10*time.Second, "the socket", func() bool { return f.fake.MaxConcurrentSockets() == 1 })
			// The second message of the same second is rate limited by the
			// Core (a retry outcome); the runner's worker requeues it.
			f.fake.Say(f.sl, "U1", "one", "")
			waitFor(t, 10*time.Second, "the first message", func() bool { return len(f.human()) == 1 })
			f.fake.Say(f.sl, "U1", "two", "")
			time.Sleep(30 * time.Millisecond)
			if len(f.human()) != 1 {
				t.Fatalf("the second message was not limited: %v", f.humanTexts())
			}
			f.h.clock.Advance(2 * time.Second)
			waitFor(t, 15*time.Second, "both messages", func() bool { return len(f.human()) == 2 })
		})

		t.Run("stops-on-shutdown", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			stop := runBridge(t, f.svc)
			waitFor(t, 10*time.Second, "the socket", func() bool { return f.fake.MaxConcurrentSockets() == 1 })
			stop()
			select {
			case <-f.fake.SocketDone():
			case <-time.After(5 * time.Second):
				t.Error("the socket outlived Run")
			}
			// The lock was released: a sibling takes it at once, with no clock
			// advance.
			h2 := f.h.sibling(harnessOptions{})
			now := h2.core.NowMs()
			held, err := h2.raw.TryLock(context.Background(), slackbridge.LockName, "other", now, now+1000)
			if err != nil || !held {
				t.Errorf("TryLock after shutdown = %v, %v", held, err)
			}
		})
	})
}

func TestSlackBridgeLifecycle(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		type lcFix struct {
			*slackFix
			ch ab2store.Channel
			sl string
		}
		newLC := func(t *testing.T) *lcFix {
			t.Helper()
			x := newSlackFixOpts(t, e, harnessOptions{}, func(c *slackbridge.Config) {
				bridgeCfg(c)
				c.ArchiveAfterDone = 168 * time.Hour
			})
			ch := x.slChannel("job1-chat", "job1", nil)
			got, err := x.create("job1-chat", "")
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			return &lcFix{slackFix: x, ch: ch, sl: got.Bridge.SlackChannelID}
		}
		lifecycle := func(t *testing.T, x *lcFix) {
			t.Helper()
			if err := x.svc.Lifecycle(context.Background()); err != nil {
				t.Fatalf("Lifecycle: %v", err)
			}
		}
		state := func(x *lcFix) ab2store.BridgeState {
			b, ok := x.bridgeRow(x.ch)
			if !ok {
				return "deleted"
			}
			return b.State
		}
		finals := func(x *lcFix) int {
			c, _ := x.fake.Channel(x.sl)
			n := 0
			for _, p := range c.Posts {
				if p.Section == slackbridge.FinalLine {
					n++
				}
			}
			return n
		}

		t.Run("done-after-hours", func(t *testing.T) {
			x := newLC(t)
			x.h.rtDone("job1", ab2bus.RetireWithBridge)
			lifecycle(t, x)
			if state(x) != ab2store.BridgeLive {
				t.Fatalf("archived at once: %s", state(x))
			}
			x.h.clock.Advance(168*time.Hour + time.Minute)
			lifecycle(t, x)
			if state(x) != ab2store.BridgeArchived || finals(x) != 1 {
				t.Fatalf("state = %s, final lines = %d", state(x), finals(x))
			}
			if c, _ := x.fake.Channel(x.sl); !c.Archived {
				t.Error("the Slack channel is not archived")
			}
			m := maintenance.New(x.h.core)
			m.Archiver = x.svc
			x.h.retireRun(m)
			x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				sc, err := tx.Scope(ctx, "job1")
				if err != nil || sc.State != ab2store.ScopeRetired {
					t.Errorf("scope = %+v, %v", sc, err)
				}
				return nil
			})
			x.noSecretsLogged(t)
		})

		t.Run("done-no-humans", func(t *testing.T) {
			x := newLC(t)
			x.h.rtDone("job1", ab2bus.RetireWithBridge)
			lifecycle(t, x)
			x.fake.Leave(x.sl, "U1")
			x.svc.HandleEvent(context.Background(), slackapi.Event{Kind: slackapi.EventMemberLeft, Channel: x.sl, User: "U1"})
			if state(x) != ab2store.BridgeArchived || finals(x) != 1 {
				t.Errorf("state = %s, final lines = %d", state(x), finals(x))
			}
		})

		t.Run("member-left-open-scope-ignored", func(t *testing.T) {
			x := newLC(t)
			x.fake.Leave(x.sl, "U1")
			x.svc.HandleEvent(context.Background(), slackapi.Event{Kind: slackapi.EventMemberLeft, Channel: x.sl, User: "U1"})
			lifecycle(t, x)
			if state(x) != ab2store.BridgeLive {
				t.Errorf("an open scope's bridge is %s after its humans left", state(x))
			}
		})

		t.Run("done-never-joined", func(t *testing.T) {
			x := newLC(t)
			x.fake.Leave(x.sl, "U1") // invited, never stayed
			x.h.rtDone("job1", ab2bus.RetireWithBridge)
			lifecycle(t, x)
			if state(x) != ab2store.BridgeArchived {
				t.Errorf("state = %s", state(x))
			}
		})

		t.Run("bots-and-deleted-do-not-count", func(t *testing.T) {
			x := newLC(t)
			x.fake.AddUser(slackapi.User{ID: "UB", TeamID: slTeam, DisplayName: "robot", IsBot: true})
			x.fake.AddUser(slackapi.User{ID: "UD", TeamID: slTeam, DisplayName: "gone", Deleted: true})
			x.fake.Join(x.sl, "UB")
			x.fake.Join(x.sl, "UD")
			x.fake.Leave(x.sl, "U1")
			x.h.rtDone("job1", ab2bus.RetireWithBridge)
			lifecycle(t, x)
			if state(x) != ab2store.BridgeArchived {
				t.Errorf("state = %s", state(x))
			}
		})

		t.Run("unknown-member-is-not-no-humans", func(t *testing.T) {
			x := newLC(t)
			x.fake.Join(x.sl, "UGHOST") // users.info does not know this member
			x.fake.Leave(x.sl, "U1")
			x.h.rtDone("job1", ab2bus.RetireWithBridge)
			lifecycle(t, x)
			if state(x) != ab2store.BridgeLive || x.fake.Calls("Archive") != 0 {
				t.Fatalf("archived because of a user lookup anomaly: state = %s", state(x))
			}
			lifecycle(t, x) // the check is repeated, not marked done
			if state(x) != ab2store.BridgeLive || x.fake.Calls("Members") < 2 {
				t.Fatalf("state = %s, Members calls = %d", state(x), x.fake.Calls("Members"))
			}
			if logs := x.logs.String(); !strings.Contains(logs, "unknown") || !strings.Contains(logs, x.sl) || strings.Contains(logs, "UGHOST") {
				t.Errorf("warning must name the Slack channel and a count, not the user; logs:\n%s", logs)
			}
			// The member resolves as deleted: no human remains.
			x.fake.AddUser(slackapi.User{ID: "UGHOST", TeamID: slTeam, DisplayName: "gone", Deleted: true})
			lifecycle(t, x)
			if state(x) != ab2store.BridgeArchived {
				t.Errorf("state = %s after the member resolved as deleted", state(x))
			}
		})

		t.Run("unknown-member-leaves", func(t *testing.T) {
			x := newLC(t)
			x.fake.Join(x.sl, "UGHOST")
			x.fake.Leave(x.sl, "U1")
			x.h.rtDone("job1", ab2bus.RetireWithBridge)
			lifecycle(t, x)
			if state(x) != ab2store.BridgeLive {
				t.Fatalf("state = %s", state(x))
			}
			x.fake.Leave(x.sl, "UGHOST")
			lifecycle(t, x)
			if state(x) != ab2store.BridgeArchived {
				t.Errorf("state = %s after the unknown member left", state(x))
			}
		})

		t.Run("unknown-member-with-a-human-present", func(t *testing.T) {
			x := newLC(t)
			x.fake.Join(x.sl, "UGHOST")
			x.h.rtDone("job1", ab2bus.RetireWithBridge)
			lifecycle(t, x)
			if state(x) != ab2store.BridgeLive {
				t.Errorf("state = %s with a human (U1) still in the channel", state(x))
			}
		})

		t.Run("missed-member-left", func(t *testing.T) {
			x := newLC(t)
			x.h.rtDone("job1", ab2bus.RetireWithBridge)
			runBridge(t, x.svc)
			waitFor(t, 10*time.Second, "the socket", func() bool { return x.fake.MaxConcurrentSockets() == 1 })
			// Let the checks of the connection settle.
			last, stable := -1, 0
			waitFor(t, 10*time.Second, "settled membership checks", func() bool {
				n := x.fake.Calls("Members")
				if n == last && n > 0 {
					stable++
				} else {
					last, stable = n, 0
				}
				return stable >= 15
			})
			x.fake.DropNext(1)
			x.fake.Leave(x.sl, "U1")
			time.Sleep(200 * time.Millisecond)
			if state(x) != ab2store.BridgeLive {
				t.Fatalf("state = %s before the reconnect", state(x))
			}
			x.fake.Disconnect()
			waitFor(t, 10*time.Second, "the archive", func() bool { return state(x) == ab2store.BridgeArchived })
			if finals(x) != 1 {
				t.Errorf("final lines = %d", finals(x))
			}
		})

		t.Run("idle", func(t *testing.T) {
			x := newLC(t)
			x.h.clock.Advance(337 * time.Hour)
			lifecycle(t, x)
			if state(x) != ab2store.BridgeArchived || finals(x) != 1 {
				t.Errorf("state = %s, final lines = %d", state(x), finals(x))
			}
		})

		t.Run("channel-gone", func(t *testing.T) {
			x := newLC(t)
			x.h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.ResetData(ctx) })
			lifecycle(t, x)
			if c, _ := x.fake.Channel(x.sl); !c.Archived {
				t.Error("the Slack channel is not archived")
			}
			if _, ok := x.bridgeRow(x.ch); ok {
				t.Error("the bridge row is still there")
			}
			lifecycle(t, x) // a second run finds nothing to do
		})

		t.Run("archive-twice", func(t *testing.T) {
			x := newLC(t)
			b, _ := x.bridgeRow(x.ch)
			x.h.rtDone("job1", ab2bus.RetireWithBridge)
			x.h.clock.Advance(169 * time.Hour)
			lifecycle(t, x)
			// Maintenance holds a stale live snapshot of the same bridge.
			if err := x.svc.ArchiveBridge(context.Background(), b, scopes.ReasonDone); err != nil {
				t.Fatalf("second ArchiveBridge: %v", err)
			}
			lifecycle(t, x)
			if n := x.fake.Calls("Archive"); n != 1 || finals(x) != 1 {
				t.Errorf("Archive called %d times, final lines %d, want 1 and 1", n, finals(x))
			}
		})

		t.Run("retired-scope-left-alone", func(t *testing.T) {
			x := newLC(t)
			x.h.rtDone("job1", ab2bus.RetireWithBridge)
			x.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				return tx.MarkScopeRetired(ctx, "job1", x.h.core.NowMs())
			})
			x.h.clock.Advance(1000 * time.Hour)
			lifecycle(t, x)
			if state(x) != ab2store.BridgeLive || x.fake.Calls("Archive") != 0 {
				t.Errorf("state = %s", state(x))
			}
		})
	})
}

// faultyStore steers the runner's Store calls: it can refuse the lock, fail
// the map-row insert a number of times and run a hook at the start of the
// next write transaction (another runner getting there first).
type faultyStore struct {
	ab2store.Store
	mu        sync.Mutex
	denyLock  bool
	failPuts  int
	puts      int // PutBridgeMessage calls that reached the Store
	nextTxHok func(context.Context, ab2store.Tx) error
}

func (s *faultyStore) TryLock(ctx context.Context, name, owner string, now, until int64) (bool, error) {
	s.mu.Lock()
	deny := s.denyLock
	s.mu.Unlock()
	if deny {
		return false, nil
	}
	return s.Store.TryLock(ctx, name, owner, now, until)
}

func (s *faultyStore) set(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

func (s *faultyStore) Tx(ctx context.Context, fn func(context.Context, ab2store.Tx) error) error {
	return s.Store.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error {
		s.mu.Lock()
		hook := s.nextTxHok
		s.nextTxHok = nil
		s.mu.Unlock()
		if hook != nil {
			if err := hook(ctx, tx); err != nil {
				return err
			}
		}
		return fn(ctx, faultyTx{tx, s})
	})
}

type faultyTx struct {
	ab2store.Tx
	s *faultyStore
}

func (t faultyTx) PutBridgeMessage(ctx context.Context, m ab2store.BridgeMessage) error {
	t.s.mu.Lock()
	t.s.puts++
	fail := t.s.failPuts > 0
	if fail {
		t.s.failPuts--
	}
	t.s.mu.Unlock()
	if fail {
		return errors.New("injected record failure")
	}
	return t.Tx.PutBridgeMessage(ctx, m)
}

// faulty builds a second Service over the same database through a faultyStore.
func (f *inFix) faulty() (*faultyStore, *slackbridge.Service) {
	f.h.t.Helper()
	fs := &faultyStore{Store: f.h.raw}
	c, err := core.New(fs, f.h.cfg, core.Options{Now: f.h.clock.Now, Notifier: f.h.notifier})
	if err != nil {
		f.h.t.Fatal(err)
	}
	cfg := slackbridge.Config{ArchiveAfterDone: time.Hour, MaxMessageChars: 3000, Logger: f.svcLogger()}
	bridgeCfg(&cfg)
	svc, err := slackbridge.New(context.Background(), c, f.fake, cfg)
	if err != nil {
		f.h.t.Fatal(err)
	}
	return fs, svc
}

func TestSlackBridgeState(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()

		t.Run("record-retried-without-repost", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			fs, svc := f.faulty()
			seq := f.send("job1-coder", ab2bus.SendInput{Content: "once"})
			fs.set(func() { fs.failPuts = 1000 })
			if err := svc.Drain(ctx); err == nil {
				t.Error("Drain hid the failed record")
			}
			if n := len(f.bridged()); n != 1 {
				t.Fatalf("posts = %d, want 1", n)
			}
			if _, ok := f.mapBySeq(seq); ok || f.acked() >= seq {
				t.Errorf("recorded despite the failure: acked %d", f.acked())
			}
			fs.set(func() { fs.failPuts = 0 })
			if err := svc.Drain(ctx); err != nil {
				t.Fatalf("second Drain: %v", err)
			}
			posts := f.bridged()
			row, ok := f.mapBySeq(seq)
			if len(posts) != 1 || !ok || row.SlackTS == "" || f.acked() != seq {
				t.Errorf("posts = %d, row = %+v %v, acked = %d", len(posts), row, ok, f.acked())
			}
			if c, _ := f.fake.Channel(f.sl); len(c.Messages) == 0 || row.SlackTS != c.Messages[len(c.Messages)-1].TS {
				t.Errorf("map row ts %s is not the posted message", row.SlackTS)
			}
		})

		t.Run("overlap-row-already-written", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			fs, svc := f.faulty()
			seq := f.send("job1-coder", ab2bus.SendInput{Content: "shared"})
			bid := f.bridge().ID
			fs.set(func() {
				fs.nextTxHok = func(ctx context.Context, tx ab2store.Tx) error {
					// A second runner recorded the same message first.
					return tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: bid, Seq: seq, SlackTS: "1700000500.000001", RootTS: "1700000500.000001", CreatedAt: 1})
				}
			})
			if err := svc.Drain(ctx); err != nil {
				t.Fatalf("Drain: %v", err)
			}
			row, ok := f.mapBySeq(seq)
			if !ok || row.SlackTS != "1700000500.000001" {
				t.Errorf("map row = %+v, %v; the other runner's row must stay", row, ok)
			}
			fs.mu.Lock()
			puts := fs.puts
			fs.mu.Unlock()
			if puts != 0 { // the hook wrote the row below the wrapper; the bridge must not write
				t.Errorf("the bridge wrote %d map rows over the other runner's", puts)
			}
			if f.acked() != seq {
				t.Errorf("acked = %d, want %d", f.acked(), seq)
			}
		})

		t.Run("lock-loss-stops-and-resumes", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			fs, svc := f.faulty()
			runBridge(t, svc)
			waitFor(t, 10*time.Second, "the socket", func() bool { return f.fake.MaxConcurrentSockets() == 1 })
			f.send("job1-coder", ab2bus.SendInput{Content: "before"})
			waitFor(t, 10*time.Second, "the first post", func() bool { return len(f.bridged()) == 1 })

			fs.set(func() { fs.denyLock = true })
			select {
			case <-f.fake.SocketDone():
			case <-time.After(5 * time.Second):
				t.Fatal("the socket outlived the lock")
			}
			n := f.fake.Calls("Post")
			second := f.send("job1-coder", ab2bus.SendInput{Content: "during"})
			time.Sleep(300 * time.Millisecond)
			if f.fake.Calls("Post") != n || len(f.bridged()) != 1 {
				t.Fatalf("posted without the lock: %+v", f.bridged())
			}

			fs.set(func() { fs.denyLock = false })
			waitFor(t, 10*time.Second, "the resumed post", func() bool { return len(f.bridged()) == 2 })
			time.Sleep(200 * time.Millisecond)
			got := f.bridged()
			if len(got) != 2 || got[1].Seq != second || f.acked() != second {
				t.Errorf("posts = %+v, acked = %d", got, f.acked())
			}
		})

		t.Run("crash-repair-before-drain", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			// A runner posted the message and died before its map row.
			seq := f.send("job1-coder", ab2bus.SendInput{Content: "posted before the crash"})
			f.fake.SayAsBot(f.sl, "BBOT", "posted before the crash", "", seq)
			before := f.fake.Calls("Post")
			h2 := f.h.sibling(harnessOptions{})
			svc2 := f.newService(h2, nil)
			runBridge(t, svc2)
			waitFor(t, 10*time.Second, "the repair", func() bool { _, ok := f.mapBySeq(seq); return ok && f.acked() >= seq })
			time.Sleep(300 * time.Millisecond)
			if f.fake.Calls("Post") != before || len(f.bridged()) != 0 {
				t.Errorf("the repaired message was posted again: %+v", f.bridged())
			}
		})
	})
}

// errTransport is an error no Slack answer can have produced: a request that
// may or may not have reached Slack.
var errTransport = errors.New("read tcp: connection reset by peer")

func (f *inFix) sendAs(content string, replyTo *int64) int64 {
	f.h.t.Helper()
	return f.send("job1-coder", ab2bus.SendInput{Content: content, ReplyTo: replyTo})
}

func TestSlackBridgeDuplicateGuards(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()

		t.Run("lock-loss-keeps-pending-record", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post") // the bridge notices
			fs, svc := f.faulty()
			runBridge(t, svc)
			waitFor(t, 10*time.Second, "the socket", func() bool { return f.fake.MaxConcurrentSockets() == 1 })
			fs.set(func() { fs.failPuts = 1000 })
			seq := f.sendAs("posted, record fails", nil)
			waitFor(t, 10*time.Second, "the post", func() bool { return len(f.bridged()) == 1 })
			waitFor(t, 10*time.Second, "the failed record", func() bool { fs.mu.Lock(); defer fs.mu.Unlock(); return fs.puts >= 1 })
			first := f.bridged()
			if len(first) != 1 {
				t.Fatalf("posts = %d", len(first))
			}
			fs.set(func() { fs.denyLock = true })
			select {
			case <-f.fake.SocketDone():
			case <-time.After(5 * time.Second):
				t.Fatal("the socket outlived the lock")
			}
			// The same Service regains the lock with its pending post intact. Its
			// record still fails here, which also keeps the catch-up from
			// repairing the row through the message metadata: only the memory of
			// the first post stands between the drain and a second post.
			fs.set(func() { fs.denyLock = false })
			waitFor(t, 10*time.Second, "the catch-up after the lock came back", func() bool { return f.fake.Calls("History") >= 2 })
			time.Sleep(300 * time.Millisecond)
			fs.set(func() { fs.failPuts = 0 })
			waitFor(t, 10*time.Second, "the record", func() bool { _, ok := f.mapBySeq(seq); return ok && f.acked() >= seq })
			time.Sleep(200 * time.Millisecond)
			if n := f.fake.Calls("Post") - base; n != 1 {
				t.Errorf("Post called %d times, want 1", n)
			}
			c, _ := f.fake.Channel(f.sl)
			var firstTS string
			for _, m := range c.Messages {
				if m.MetadataSeq == seq && firstTS == "" {
					firstTS = m.TS
				}
			}
			if row, _ := f.mapBySeq(seq); row.SlackTS != firstTS || firstTS == "" {
				t.Errorf("map row ts %q, first post ts %q", row.SlackTS, firstTS)
			}
		})

		t.Run("stale-pending-dropped", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post") // the bridge notices
			fs, svc := f.faulty()
			seq := f.sendAs("recorded by another runner", nil)
			fs.set(func() { fs.failPuts = 1000 })
			if err := svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed record")
			}
			if n := svc.PendingPosts(); n != 1 {
				t.Fatalf("PendingPosts = %d, want 1", n)
			}
			// Another runner records the message and moves the mark.
			bid := f.bridge().ID
			f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				if err := tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: bid, Seq: seq, SlackTS: "1700000500.000001", RootTS: "1700000500.000001", CreatedAt: 1}); err != nil {
					return err
				}
				return tx.AdvanceAckedMark(ctx, ab2store.SlackBridge, f.ch.ID, seq)
			})
			fs.set(func() { fs.failPuts = 0 })
			fs.mu.Lock()
			putsBefore := fs.puts
			fs.mu.Unlock()
			if err := svc.Drain(ctx); err != nil {
				t.Fatalf("second Drain: %v", err)
			}
			fs.mu.Lock()
			puts := fs.puts
			fs.mu.Unlock()
			if puts != putsBefore || f.fake.Calls("Post")-base != 1 {
				t.Errorf("recorded or posted again: puts %d -> %d, Post calls %d", putsBefore, puts, f.fake.Calls("Post")-base)
			}
			if n := svc.PendingPosts(); n != 0 {
				t.Errorf("PendingPosts = %d after the mark passed it, want 0", n)
			}
			if row, _ := f.mapBySeq(seq); row.SlackTS != "1700000500.000001" {
				t.Errorf("the other runner's row was replaced: %+v", row)
			}
		})

		t.Run("uncertain-post-found-not-reposted", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post") // the bridge notices
			seq := f.sendAs("answer lost", nil)
			f.fake.PostThenFail(errTransport)
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed post")
			}
			if f.acked() >= seq {
				t.Fatal("acked a message whose post failed")
			}
			if n := f.svc.PendingPosts(); n != 1 {
				t.Errorf("PendingPosts = %d, want 1 (uncertain)", n)
			}
			if err := f.svc.Drain(ctx); err != nil {
				t.Fatalf("second Drain: %v", err)
			}
			if n := f.fake.Calls("Post") - base; n != 1 {
				t.Errorf("Post called %d times, want 1", n)
			}
			c, _ := f.fake.Channel(f.sl)
			var ts string
			for _, m := range c.Messages {
				if m.MetadataSeq == seq {
					ts = m.TS
				}
			}
			row, ok := f.mapBySeq(seq)
			if !ok || ts == "" || row.SlackTS != ts || row.RootTS != ts || f.acked() != seq {
				t.Errorf("row = %+v %v, found ts %q, acked %d", row, ok, ts, f.acked())
			}
			if n := f.svc.PendingPosts(); n != 0 {
				t.Errorf("PendingPosts = %d after the record, want 0", n)
			}
		})

		t.Run("uncertain-http-5xx-is-searched", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post") // the bridge notices
			seq := f.sendAs("gateway timeout", nil)
			f.fake.PostThenFail(&slackapi.APIError{Method: "chat.postMessage", Code: "http_504"})
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed post")
			}
			if err := f.svc.Drain(ctx); err != nil {
				t.Fatalf("second Drain: %v", err)
			}
			if _, ok := f.mapBySeq(seq); !ok || f.fake.Calls("Post")-base != 1 {
				t.Errorf("Post calls = %d, recorded = %v", f.fake.Calls("Post"), ok)
			}
		})

		t.Run("uncertain-post-not-found-reposts", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post") // the bridge notices
			seq := f.sendAs("never reached Slack", nil)
			f.fake.FailNext("Post", errTransport)
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed post")
			}
			if err := f.svc.Drain(ctx); err != nil {
				t.Fatalf("second Drain: %v", err)
			}
			if n := f.fake.Calls("Post") - base; n != 2 {
				t.Errorf("Post called %d times, want 2", n)
			}
			if got := f.bridged(); len(got) != 1 || got[0].Seq != seq {
				t.Errorf("posts = %+v", got)
			}
			if _, ok := f.mapBySeq(seq); !ok || f.acked() != seq || f.svc.PendingPosts() != 0 {
				t.Errorf("recorded = %v, acked %d, pending %d", ok, f.acked(), f.svc.PendingPosts())
			}
		})

		t.Run("uncertain-thread-reply-found", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post") // the bridge notices
			root := f.sendAs("root", nil)
			f.drainAll()
			rootRow, ok := f.mapBySeq(root)
			if !ok {
				t.Fatal("root not recorded")
			}
			reply := f.sendAs("reply", &root)
			f.fake.PostThenFail(errTransport)
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed post")
			}
			history, replies := f.fake.Calls("History"), f.fake.Calls("Replies")
			if err := f.svc.Drain(ctx); err != nil {
				t.Fatalf("second Drain: %v", err)
			}
			if n := f.fake.Calls("Post") - base; n != 2 {
				t.Errorf("Post called %d times, want 2 (root, one reply)", n)
			}
			if f.fake.Calls("Replies") <= replies || f.fake.Calls("History") != history {
				t.Errorf("a thread reply is searched with Replies only: Replies %d -> %d, History %d -> %d",
					replies, f.fake.Calls("Replies"), history, f.fake.Calls("History"))
			}
			c, _ := f.fake.Channel(f.sl)
			var ts string
			for _, m := range c.Messages {
				if m.MetadataSeq == reply {
					ts = m.TS
				}
			}
			row, ok := f.mapBySeq(reply)
			if !ok || ts == "" || row.SlackTS != ts || row.RootTS != rootRow.SlackTS {
				t.Errorf("row = %+v %v, found ts %q, root %q", row, ok, ts, rootRow.SlackTS)
			}
		})

		t.Run("certain-failure-no-search", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post") // the bridge notices
			seq := f.sendAs("refused by Slack", nil)
			history, replies := f.fake.Calls("History"), f.fake.Calls("Replies")
			f.fake.FailNext("Post", &slackapi.APIError{Method: "chat.postMessage", Code: "invalid_blocks"})
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed post")
			}
			if n := f.svc.PendingPosts(); n != 0 {
				t.Errorf("PendingPosts = %d after a certain failure", n)
			}
			if err := f.svc.Drain(ctx); err != nil {
				t.Fatalf("second Drain: %v", err)
			}
			if f.fake.Calls("History") != history || f.fake.Calls("Replies") != replies {
				t.Errorf("searched after a certain failure: History %d -> %d, Replies %d -> %d",
					history, f.fake.Calls("History"), replies, f.fake.Calls("Replies"))
			}
			if _, ok := f.mapBySeq(seq); !ok || f.fake.Calls("Post")-base != 2 {
				t.Errorf("recorded = %v, Post calls %d", ok, f.fake.Calls("Post")-base)
			}
		})

		t.Run("search-failure-does-not-post", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post") // the bridge notices
			seq := f.sendAs("answer lost, search fails", nil)
			f.fake.PostThenFail(errTransport)
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed post")
			}
			f.fake.FailNext("History", errTransport)
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed search")
			}
			if n := f.fake.Calls("Post") - base; n != 1 {
				t.Fatalf("posted blind: Post called %d times", n)
			}
			if err := f.svc.Drain(ctx); err != nil {
				t.Fatalf("third Drain: %v", err)
			}
			if _, ok := f.mapBySeq(seq); !ok || f.fake.Calls("Post")-base != 1 || f.acked() != seq {
				t.Errorf("recorded = %v, Post calls %d, acked %d", ok, f.fake.Calls("Post"), f.acked())
			}
		})

		t.Run("search-ignores-other-apps-and-humans", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post")
			seq := f.sendAs("ours was lost", nil)
			f.fake.FailNext("Post", errTransport) // nothing reached Slack
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed post")
			}
			// Same metadata seq, but another app's bot, and no bot at all.
			f.fake.SayAsBot(f.sl, "BOTHERAPP", "another app", "", seq)
			f.fake.SayAsBot(f.sl, "", "a human", "", seq)
			if err := f.svc.Drain(ctx); err != nil {
				t.Fatalf("second Drain: %v", err)
			}
			if n := f.fake.Calls("Post") - base; n != 2 {
				t.Errorf("Post called %d times, want 2 (the failed one and one repost)", n)
			}
			c, _ := f.fake.Channel(f.sl)
			row, ok := f.mapBySeq(seq)
			if !ok || len(c.Messages) == 0 || row.SlackTS != c.Messages[len(c.Messages)-1].TS {
				t.Errorf("the row must hold the bridge's own repost: %+v %v", row, ok)
			}
		})

		t.Run("search-reads-every-page", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post")
			f.fake.SayAsBot(f.sl, "BOTHERAPP", "filler 1", "", 0)
			f.fake.SayAsBot(f.sl, "BOTHERAPP", "filler 2", "", 0)
			seq := f.sendAs("on the second page", nil)
			f.fake.PostThenFail(errTransport)
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed post")
			}
			f.fake.SetPageSize(2)
			history := f.fake.Calls("History")
			if err := f.svc.Drain(ctx); err != nil {
				t.Fatalf("second Drain: %v", err)
			}
			if f.fake.Calls("History")-history < 2 {
				t.Errorf("History called %d times, want at least 2 pages", f.fake.Calls("History")-history)
			}
			if n := f.fake.Calls("Post") - base; n != 1 {
				t.Errorf("Post called %d times, want 1", n)
			}
			if _, ok := f.mapBySeq(seq); !ok {
				t.Error("the found post was not recorded")
			}
		})

		t.Run("search-window-excludes-older-posts", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post")
			seq := f.sendAs("old look-alike", nil)
			// A post with this seq from before the window (another life of the
			// bridge): not the lost one.
			f.fake.SayAsBot(f.sl, "BBOT", "old", "", seq)
			f.h.clock.Advance(2 * time.Minute)
			f.fake.FailNext("Post", errTransport)
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed post")
			}
			if err := f.svc.Drain(ctx); err != nil {
				t.Fatalf("second Drain: %v", err)
			}
			if n := f.fake.Calls("Post") - base; n != 2 {
				t.Errorf("Post called %d times, want 2 (an older post is outside the window)", n)
			}
		})

		t.Run("archived-bridge-remembers-nothing", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			f.sendAs("racing the archive", nil)
			f.fake.FailNext("Post", errTransport)
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed post")
			}
			if f.svc.PendingPosts() != 1 {
				t.Fatalf("PendingPosts = %d", f.svc.PendingPosts())
			}
			if err := f.svc.ArchiveBridge(ctx, f.bridge(), "test"); err != nil {
				t.Fatal(err)
			}
			if f.svc.PendingPosts() != 0 {
				t.Errorf("PendingPosts = %d after the archive", f.svc.PendingPosts())
			}
		})

		t.Run("uncertain-entry-dropped-when-mark-passes", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			base := f.fake.Calls("Post") // the bridge notices
			seq := f.sendAs("another runner got there", nil)
			f.fake.FailNext("Post", errTransport)
			if err := f.svc.Drain(ctx); err == nil {
				t.Fatal("Drain hid the failed post")
			}
			bid := f.bridge().ID
			f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				if err := tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: bid, Seq: seq, SlackTS: "1700000500.000001", RootTS: "1700000500.000001", CreatedAt: 1}); err != nil {
					return err
				}
				return tx.AdvanceAckedMark(ctx, ab2store.SlackBridge, f.ch.ID, seq)
			})
			if err := f.svc.Drain(ctx); err != nil {
				t.Fatalf("second Drain: %v", err)
			}
			if f.fake.Calls("Post")-base != 1 || f.svc.PendingPosts() != 0 {
				t.Errorf("Post calls %d, pending %d", f.fake.Calls("Post"), f.svc.PendingPosts())
			}
		})
	})
}
