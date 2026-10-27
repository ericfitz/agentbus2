package tui

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// setBackoff pins retryBackoff for a test and restores it (no test in the
// package calls t.Parallel).
func setBackoff(t *testing.T, d time.Duration) {
	t.Helper()
	old := retryBackoff
	retryBackoff = d
	t.Cleanup(func() { retryBackoff = old })
}

// loopRun runs c.receiveLoop in a goroutine; every message it sends lands on
// the returned channel, and done closes when the loop returns. A send never
// blocks past the client's cancel (a runaway loop that fills msgs must not
// hang the test), and t.Cleanup cancels the client and waits for the loop
// with a bound, so a failing assertion ends the test instead of hanging the
// package.
func loopRun(t *testing.T, c *client, gate func(any)) (msgs chan any, done chan struct{}) {
	t.Helper()
	msgs = make(chan any, 64)
	done = make(chan struct{})
	go func() {
		defer close(done)
		c.receiveLoop(func(m any) {
			select {
			case msgs <- m:
			case <-c.ctx.Done():
				return
			}
			if gate != nil {
				gate(m)
			}
		})
	}()
	t.Cleanup(func() { stopLoop(t, c, done) })
	return msgs, done
}

// stopLoop cancels the client and waits up to 5 s for the loop to return.
func stopLoop(t *testing.T, c *client, done chan struct{}) {
	t.Helper()
	c.cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("the receive loop did not return within 5s of cancel")
	}
}

// nextMsg waits up to d for the next message of the loop.
func nextMsg(t *testing.T, msgs chan any, d time.Duration) any {
	t.Helper()
	select {
	case m := <-msgs:
		return m
	case <-time.After(d):
		t.Fatalf("no message within %v", d)
		return nil
	}
}

func contents(res bus.ReceiveResult) []string {
	var out []string
	for _, m := range res.Messages {
		out = append(out, m.Content)
	}
	return out
}

func sessionNames(t *testing.T, b bus.Bus, identity string) []string {
	t.Helper()
	ids, err := b.Identities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, id := range ids {
		if id.Name != identity {
			continue
		}
		for _, s := range id.Sessions {
			out = append(out, s.DisplayName)
		}
	}
	slices.Sort(out)
	return out
}

func TestNewClientOpensTuiSessionAndSubscribesAll(t *testing.T) {
	f := newBusFixture(t)
	if got := f.c.displayName(); got != "eric/tui" {
		t.Fatalf("display name = %q, want eric/tui", got)
	}
	if got := sessionNames(t, f.sam, "eric"); !slices.Equal(got, []string{"eric/tui"}) {
		t.Fatalf("eric's sessions = %v", got)
	}
	for _, ch := range []string{"general", "dev", "dev-notes"} {
		if !f.c.isSubscribed(ch) {
			t.Errorf("not subscribed to %q", ch)
		}
	}
	c2, err := newClient(context.Background(), f.bus, "eric", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c2.close() })
	if got := c2.displayName(); got != "eric/tui-2" {
		t.Fatalf("second display name = %q, want eric/tui-2", got)
	}
}

func TestNewClientClosesSessionWhenListChannelsFails(t *testing.T) {
	f := newBusFixture(t)
	failing := &failListBus{Bus: identityBus(t, f.core, "zed")}
	if _, err := newClient(context.Background(), failing, "zed", ""); err == nil {
		t.Fatal("newClient succeeded though ListChannels failed")
	}
	if got := sessionNames(t, f.sam, "zed"); len(got) != 0 {
		t.Fatalf("zed's session was left open: %v", got)
	}
}

type failListBus struct{ bus.Bus }

func (failListBus) ListChannels(context.Context) ([]bus.Channel, error) {
	return nil, errors.New("boom")
}

func TestSubscribeIsIdempotentAndKeepsOff(t *testing.T) {
	f := newBusFixture(t)
	cb := newCountingBus(identityBus(t, f.core, "zed"))
	c, err := newClient(context.Background(), cb, "zed", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	base := cb.count("Subscribe")
	if base != 3 {
		t.Fatalf("newClient made %d Subscribe calls, want 3 (general, dev, dev-notes)", base)
	}
	if err := c.subscribe(bus.Channel{Name: "dev"}, "now"); err != nil {
		t.Fatal(err)
	}
	if got := cb.count("Subscribe"); got != base {
		t.Fatalf("second subscribe made a call: %d, want %d", got, base)
	}
	c.forget("dev")
	if !c.known("dev") || c.isSubscribed("dev") {
		t.Fatalf("after forget: known=%v subscribed=%v", c.known("dev"), c.isSubscribed("dev"))
	}
	if c.known("nope") {
		t.Fatal("an unseen channel is known")
	}
	if err := c.subscribe(bus.Channel{Name: "dev"}, "oldest"); err != nil {
		t.Fatal(err)
	}
	if got := cb.count("Subscribe"); got != base+1 || !c.isSubscribed("dev") {
		t.Fatalf("explicit resubscribe: %d calls, subscribed=%v", got, c.isSubscribed("dev"))
	}
}

func TestSubscribeReturnsOtherErrorsAndKeepsState(t *testing.T) {
	f := newBusFixture(t)
	err := f.c.subscribe(bus.Channel{Name: "ghost"}, "now")
	if bus.CodeOf(err) != bus.CodeNotFound {
		t.Fatalf("subscribe to a missing channel: %v, want not_found", err)
	}
	if f.c.known("ghost") {
		t.Fatal("a failed subscribe changed the state")
	}
}

func TestNoAccessChannelIsMarkedOnce(t *testing.T) {
	f := newBusFixture(t)
	_, err := f.sam.CreateChannel(context.Background(), bus.CreateChannelInput{
		Name: "private",
		ACL: []bus.ACLEntry{{Identity: "sam", Permissions: []bus.Permission{
			bus.PermRead, bus.PermWrite, bus.PermAdmin,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	setAdmin(t, f.core, "ann")
	cb := newCountingBus(identityBus(t, f.core, "ann"))
	c, err := newClient(context.Background(), cb, "ann", "")
	if err != nil {
		t.Fatalf("newClient failed on a no-access channel: %v", err)
	}
	t.Cleanup(func() { _ = c.close() })
	if !c.noAccess("private") || c.isSubscribed("private") || !c.known("private") {
		t.Fatalf("private: noAccess=%v subscribed=%v known=%v", c.noAccess("private"), c.isSubscribed("private"), c.known("private"))
	}
	if err := c.subscribe(bus.Channel{Name: "private"}, "now"); err != nil {
		t.Fatalf("second subscribe: %v", err)
	}
	n := 0
	for _, ch := range cb.channelsOf("Subscribe") {
		if ch == "private" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Subscribe(private) called %d times, want 1", n)
	}
}

func TestReceiveLoopWatchesThenReceivesAndAcks(t *testing.T) {
	f := newBusFixture(t)
	first := true
	gate := make(chan struct{})
	probed := make(chan struct{})
	msgs, done := loopRun(t, f.c, func(any) {
		if first {
			first = false
			close(probed)
			select {
			case <-gate:
			case <-f.c.ctx.Done():
			}
		}
	})
	f.samSend("dev", "one")
	m := nextMsg(t, msgs, 5*time.Second)
	<-probed
	b1, ok := m.(batchMsg)
	if !ok || !slices.Equal(contents(b1.res), []string{"one"}) {
		t.Fatalf("first message = %#v", m)
	}
	if got := pendingFor(t, f.bus, "dev"); got != 1 {
		t.Fatalf("pending while the first batch is unacked = %d, want 1", got)
	}
	close(gate)
	waitFor(t, 5*time.Second, func() bool { return pendingFor(t, f.bus, "dev") == 0 })
	f.samSend("dev", "two")
	b2, ok := nextMsg(t, msgs, 5*time.Second).(batchMsg)
	if !ok || !slices.Equal(contents(b2.res), []string{"two"}) || b2.res.Redelivered {
		t.Fatalf("second batch = %#v", b2)
	}
	stopLoop(t, f.c, done)
}

func TestReceiveLoopIncludesOwnSends(t *testing.T) {
	f := newBusFixture(t)
	msgs, _ := loopRun(t, f.c, nil)
	if _, err := f.bus.Send(context.Background(), bus.SendInput{Channel: "dev", Content: "mine", Session: f.c.sessionID()}); err != nil {
		t.Fatal(err)
	}
	b, ok := nextMsg(t, msgs, 5*time.Second).(batchMsg)
	if !ok || len(b.res.Messages) != 1 {
		t.Fatalf("batch = %#v", b)
	}
	if got := b.res.Messages[0]; got.Sender != "eric" || got.Display != "eric/tui" {
		t.Fatalf("own message sender=%q display=%q", got.Sender, got.Display)
	}
}

// oneMessage is a Receive double: the first call returns one message, later
// calls are empty.
func oneMessage() func(context.Context, bus.ReceiveInput) (bus.ReceiveResult, error) {
	var n atomic.Int32
	return func(context.Context, bus.ReceiveInput) (bus.ReceiveResult, error) {
		if n.Add(1) == 1 {
			return bus.ReceiveResult{Messages: []bus.Message{{Seq: 1, Content: "hi"}}, Batch: "b1"}, nil
		}
		return bus.ReceiveResult{}, nil
	}
}

func blockUntilDone(ctx context.Context) (bus.WatchResult, error) {
	<-ctx.Done()
	return bus.WatchResult{}, ctx.Err()
}

func TestReceiveLoopResumesLapsedSessionOnce(t *testing.T) {
	setBackoff(t, 10*time.Millisecond)
	var (
		mu       sync.Mutex
		tokens   []string
		watches  int
		entered  = make(chan struct{})
		release  = make(chan struct{})
		openedAt atomic.Int32
	)
	sb := &stubBus{}
	sb.open = func(_ context.Context, in bus.OpenSessionInput) (bus.Session, error) {
		mu.Lock()
		tokens = append(tokens, in.ResumeToken)
		n := len(tokens)
		mu.Unlock()
		openedAt.Store(int32(n))
		if n == 1 {
			close(entered)
			<-release
			return bus.Session{ID: "s2", Identity: "eric", DisplayName: "eric/tui", ResumeToken: "t2"}, nil
		}
		return bus.Session{ID: "s3", Identity: "eric", DisplayName: "eric/tui", ResumeToken: "t3"}, nil
	}
	sb.watch = func(ctx context.Context, _ bus.WatchInput) (bus.WatchResult, error) {
		mu.Lock()
		watches++
		n := watches
		mu.Unlock()
		switch n {
		case 1, 3:
			return bus.WatchResult{}, bus.Errorf(bus.CodeSessionLapsed, false, "lapsed")
		case 2:
			return bus.WatchResult{Woke: true, Seq: 1}, nil
		}
		return blockUntilDone(ctx)
	}
	sb.receive = oneMessage()
	c := newStubClient(t, sb, bus.Session{ID: "s1", Identity: "eric", DisplayName: "eric/tui", ResumeToken: "t1"})
	msgs, done := loopRun(t, c, nil)
	<-entered
	if got := c.sessionID(); got != "s1" {
		t.Fatalf("session id during the resume = %q, want s1", got)
	}
	close(release)
	waitFor(t, 5*time.Second, func() bool { return c.sessionID() != "s1" })
	if b, ok := nextMsg(t, msgs, 5*time.Second).(batchMsg); !ok || len(b.res.Messages) != 1 {
		t.Fatalf("no batch after the resume")
	}
	waitFor(t, 5*time.Second, func() bool { return openedAt.Load() == 2 })
	waitFor(t, 5*time.Second, func() bool { return c.sessionID() == "s3" })
	mu.Lock()
	got := slices.Clone(tokens)
	mu.Unlock()
	if !slices.Equal(got, []string{"t1", "t2"}) {
		t.Fatalf("resume tokens = %v, want [t1 t2]: a later lapse must present the new token", got)
	}
	stopLoop(t, c, done)
}

func TestReceiveLoopLapsedTwiceInARowIsAnError(t *testing.T) {
	setBackoff(t, 10*time.Millisecond)
	var opens atomic.Int32
	sb := &stubBus{}
	sb.open = func(context.Context, bus.OpenSessionInput) (bus.Session, error) {
		opens.Add(1)
		return bus.Session{ID: "s2", Identity: "eric", ResumeToken: "t2"}, nil
	}
	sb.watch = func(context.Context, bus.WatchInput) (bus.WatchResult, error) {
		return bus.WatchResult{}, bus.Errorf(bus.CodeSessionLapsed, false, "lapsed")
	}
	c := newStubClient(t, sb, bus.Session{ID: "s1", Identity: "eric", ResumeToken: "t1"})
	msgs, done := loopRun(t, c, nil)
	for range 2 {
		if _, ok := nextMsg(t, msgs, 5*time.Second).(receiveErrMsg); !ok {
			t.Fatal("want receiveErrMsg for the second lapse")
		}
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("Open called %d times without a success between, want 1", got)
	}
	stopLoop(t, c, done)
}

func TestReceiveLoopStopsOnReplacedAndClosed(t *testing.T) {
	for _, code := range []bus.Code{bus.CodeReplaced, bus.CodeSessionClosed} {
		t.Run(string(code), func(t *testing.T) {
			sb := &stubBus{}
			sb.watch = func(context.Context, bus.WatchInput) (bus.WatchResult, error) {
				return bus.WatchResult{}, bus.Errorf(code, false, "gone")
			}
			c := newStubClient(t, sb, bus.Session{ID: "s1", Identity: "eric"})
			msgs, done := loopRun(t, c, nil)
			lost, ok := nextMsg(t, msgs, 5*time.Second).(sessionLostMsg)
			if !ok || !errors.Is(lost.err, errSessionLost) || bus.CodeOf(lost.err) != code {
				t.Fatalf("message = %#v", lost)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the loop did not return")
			}
			if len(msgs) != 0 {
				t.Fatalf("extra messages after sessionLostMsg: %d", len(msgs))
			}
		})
	}
}

func TestReceiveLoopBacksOffAndRecovers(t *testing.T) {
	setBackoff(t, 100*time.Millisecond)
	var watches atomic.Int32
	sb := &stubBus{}
	sb.watch = func(ctx context.Context, _ bus.WatchInput) (bus.WatchResult, error) {
		switch n := watches.Add(1); {
		case n <= 3:
			return bus.WatchResult{}, errors.New("connection refused")
		case n == 4:
			return bus.WatchResult{Woke: true, Seq: 1}, nil
		}
		return blockUntilDone(ctx)
	}
	sb.receive = oneMessage()
	c := newStubClient(t, sb, bus.Session{ID: "s1", Identity: "eric"})
	var stamps []time.Time
	msgs, done := loopRun(t, c, nil)
	for range 3 {
		if _, ok := nextMsg(t, msgs, 5*time.Second).(receiveErrMsg); !ok {
			t.Fatal("want one receiveErrMsg per failure")
		}
		stamps = append(stamps, time.Now())
	}
	for i := 1; i < len(stamps); i++ {
		if d := stamps[i].Sub(stamps[i-1]); d < 90*time.Millisecond {
			t.Errorf("failures %d and %d only %v apart", i-1, i, d)
		}
	}
	if b, ok := nextMsg(t, msgs, 5*time.Second).(batchMsg); !ok || len(b.res.Messages) != 1 {
		t.Fatal("no batch after recovery")
	}
	stopLoop(t, c, done)
	if len(msgs) != 0 {
		t.Fatalf("%d unexpected extra messages (one toast per failure, none per success)", len(msgs))
	}
}

func TestRetryBackoffIsOneSecond(t *testing.T) {
	if retryBackoff != time.Second {
		t.Fatalf("retryBackoff = %v, want 1s", retryBackoff)
	}
}

func TestWatchTimeoutKeepsFloor(t *testing.T) {
	var (
		mu     sync.Mutex
		afters []int64
	)
	sb := &stubBus{}
	sb.watch = func(ctx context.Context, in bus.WatchInput) (bus.WatchResult, error) {
		mu.Lock()
		afters = append(afters, in.After)
		n := len(afters)
		mu.Unlock()
		switch n {
		case 1:
			return bus.WatchResult{}, nil // a timeout: the zero WatchResult
		case 2:
			return bus.WatchResult{Woke: true, Seq: 7}, nil
		}
		return blockUntilDone(ctx)
	}
	sb.receive = func(context.Context, bus.ReceiveInput) (bus.ReceiveResult, error) {
		return bus.ReceiveResult{}, nil
	}
	c := newStubClient(t, sb, bus.Session{ID: "s1", Identity: "eric"})
	c.floor = 3
	_, done := loopRun(t, c, nil)
	waitFor(t, 5*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(afters) == 3 })
	stopLoop(t, c, done)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(afters, []int64{3, 3, 7}) {
		t.Fatalf("Watch.After sequence = %v, want [3 3 7]", afters)
	}
}

func TestReceiveLoopReportsGapsOnlyAndKeepsDraining(t *testing.T) {
	var n atomic.Int32
	var acks []string
	var mu sync.Mutex
	sb := &stubBus{}
	sb.watch = func(ctx context.Context, _ bus.WatchInput) (bus.WatchResult, error) {
		if n.Add(1) == 1 {
			return bus.WatchResult{Woke: true, Seq: 9}, nil
		}
		return blockUntilDone(ctx)
	}
	sb.receive = func(_ context.Context, in bus.ReceiveInput) (bus.ReceiveResult, error) {
		mu.Lock()
		defer mu.Unlock()
		acks = append(acks, in.Ack)
		switch len(acks) {
		case 1:
			return bus.ReceiveResult{Gaps: []bus.Gap{{Channel: "dev", From: 1, To: 3}}, Batch: "g1"}, nil
		case 2:
			return bus.ReceiveResult{Messages: []bus.Message{{Seq: 4, Content: "x"}}, Batch: "b2"}, nil
		}
		return bus.ReceiveResult{}, nil
	}
	c := newStubClient(t, sb, bus.Session{ID: "s1", Identity: "eric"})
	msgs, done := loopRun(t, c, nil)
	g, ok := nextMsg(t, msgs, 5*time.Second).(batchMsg)
	if !ok || len(g.res.Gaps) != 1 {
		t.Fatalf("gaps-only batch = %#v", g)
	}
	if b, ok := nextMsg(t, msgs, 5*time.Second).(batchMsg); !ok || len(b.res.Messages) != 1 {
		t.Fatal("the drain stopped at a gaps-only result")
	}
	waitFor(t, 5*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(acks) == 3 })
	stopLoop(t, c, done)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(acks, []string{"", "g1", "b2"}) {
		t.Fatalf("ack sequence = %q, want [\"\" g1 b2]", acks)
	}
}

// watchSpy signals when the first Watch is in flight.
type watchSpy struct {
	bus.Bus
	once    sync.Once
	started chan struct{}
}

func (s *watchSpy) Watch(ctx context.Context, in bus.WatchInput) (bus.WatchResult, error) {
	s.once.Do(func() { close(s.started) })
	return s.Bus.Watch(ctx, in)
}

func TestCloseFreesTheName(t *testing.T) {
	f := newBusFixture(t)
	spy := &watchSpy{Bus: identityBus(t, f.core, "bob"), started: make(chan struct{})}
	c, err := newClient(context.Background(), spy, "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	_, done := loopRun(t, c, nil)
	<-spy.started
	if got := sessionNames(t, f.sam, "bob"); !slices.Equal(got, []string{"bob/tui"}) {
		t.Fatalf("before close: %v", got)
	}
	start := time.Now()
	if err := c.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the Watch did not end within 3 s of close")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("close took %v", d)
	}
	if got := sessionNames(t, f.sam, "bob"); len(got) != 0 {
		t.Fatalf("bob's name is still taken: %v", got)
	}
	if err := c.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestDrainAcksLastBatch(t *testing.T) {
	f := newBusFixture(t)
	f.samSend("dev", "one")
	f.samSend("dev", "two")
	msgs, _ := loopRun(t, f.c, nil)
	var got []string
	for len(got) < 2 {
		b, ok := nextMsg(t, msgs, 5*time.Second).(batchMsg)
		if !ok || b.res.Redelivered {
			t.Fatalf("unexpected message %#v", b)
		}
		got = append(got, contents(b.res)...)
	}
	if !slices.Equal(got, []string{"one", "two"}) {
		t.Fatalf("delivered %v", got)
	}
	// A drain that does not ack redelivers the batch at once: fail on the
	// first extra message rather than wait out the pending probe.
	select {
	case m := <-msgs:
		t.Fatalf("extra message after the drain (an unacked batch redelivered?): %#v", m)
	case <-time.After(200 * time.Millisecond):
	}
	waitFor(t, 5*time.Second, func() bool { return pendingFor(t, f.bus, "dev") == 0 })
}

// TestFixtureHelpers is the fixture's own test (Definition of Done C): the
// helpers Tasks 3 to 7 rely on do what their comments say.
func TestFixtureHelpers(t *testing.T) {
	f := newBusFixture(t)
	root := f.samSend("dev", "root")
	reply := f.samReply("dev", root.Seq, "reply")
	if reply.Seq <= root.Seq {
		t.Fatalf("reply seq %d <= root seq %d", reply.Seq, root.Seq)
	}
	res := f.receiveRaw()
	if got := contents(res); !slices.Equal(got, []string{"root", "reply"}) {
		t.Fatalf("receiveRaw = %v", got)
	}
	if rt := res.Messages[1].ReplyTo; rt == nil || *rt != root.Seq {
		t.Fatalf("reply_to = %v, want %d", rt, root.Seq)
	}
	if res.Messages[0].Display == "" {
		t.Fatal("samSend carried no session: Display is empty")
	}
	if f.ack != res.Batch || f.ack == "" {
		t.Fatalf("ack = %q, want the batch token %q", f.ack, res.Batch)
	}
	if got := f.receiveRaw(); len(got.Messages) != 0 || f.ack != got.Batch {
		t.Fatalf("second receiveRaw = %#v (ack %q)", got, f.ack)
	}
	if got := pendingFor(t, f.bus, "dev"); got != 0 {
		t.Fatalf("pending after the ack = %d", got)
	}
	setAdmin(t, f.core, "eric")
}

// TestResumeOfSweptSessionKeepsTheHint: a session swept by the retention job
// while the laptop slept makes Open{ResumeToken} open a fresh session
// (Decision 18, sessions/open.go). That fresh session must still be
// <identity>/tui, so the resume carries the hint too.
func TestResumeOfSweptSessionKeepsTheHint(t *testing.T) {
	f := newBusFixture(t)
	old := f.c.sessionID()
	err := f.core.Store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		_, err := tx.DeleteSessions(ctx, f.core.NowMs()+24*time.Hour.Milliseconds(), 100)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.resumeLapsed(); err != nil {
		t.Fatalf("resumeLapsed: %v", err)
	}
	if got := f.c.sessionID(); got == old {
		t.Fatalf("session id %q unchanged: the row was not swept", got)
	}
	if got := f.c.displayName(); got != "eric/tui" {
		t.Fatalf("display name after resuming a swept session = %q, want eric/tui", got)
	}
}

// TestResumeResetsAck: a lapse in the middle of a drain resets the ack, so
// the first Receive after the resume carries none (the pending batch comes
// back Redelivered and the model dedupes it by seq).
func TestResumeResetsAck(t *testing.T) {
	var (
		mu   sync.Mutex
		acks []string
		n    atomic.Int32
	)
	sb := &stubBus{}
	sb.open = func(context.Context, bus.OpenSessionInput) (bus.Session, error) {
		return bus.Session{ID: "s1", Identity: "eric", DisplayName: "eric/tui", ResumeToken: "t1"}, nil
	}
	sb.watch = func(ctx context.Context, _ bus.WatchInput) (bus.WatchResult, error) {
		if n.Add(1) == 1 {
			return bus.WatchResult{Woke: true, Seq: 1}, nil
		}
		return blockUntilDone(ctx)
	}
	sb.receive = func(_ context.Context, in bus.ReceiveInput) (bus.ReceiveResult, error) {
		mu.Lock()
		defer mu.Unlock()
		acks = append(acks, in.Ack)
		switch len(acks) {
		case 1:
			return bus.ReceiveResult{Messages: []bus.Message{{Seq: 1}}, Batch: "b1"}, nil
		case 2:
			return bus.ReceiveResult{}, bus.Errorf(bus.CodeSessionLapsed, false, "lapsed")
		}
		return bus.ReceiveResult{}, nil
	}
	c := newStubClient(t, sb, bus.Session{ID: "s1", Identity: "eric", DisplayName: "eric/tui", ResumeToken: "t1"})
	_, done := loopRun(t, c, nil)
	waitFor(t, 5*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(acks) == 3 })
	stopLoop(t, c, done)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(acks, []string{"", "b1", ""}) {
		t.Fatalf("ack sequence = %q, want [\"\" b1 \"\"]", acks)
	}
}

// TestReceiveLoopResumesLapsedSessionOnRealCore checks the stub's assumptions
// against sessions/open.go: past the lease the real Watch answers
// session_lapsed, the resume keeps the session id and the display name, and
// the loop goes on to deliver.
func TestReceiveLoopResumesLapsedSessionOnRealCore(t *testing.T) {
	ctx := context.Background()
	var offset atomic.Int64
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "bus.sqlite"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := core.Defaults()
	cfg.WatchMergeSeconds = 0
	co, err := core.New(st, cfg, core.Options{
		Notifier: notify.NewMemory(),
		Now:      func() time.Time { return time.Now().Add(time.Duration(offset.Load())) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (channels.Service{Core: co}).EnsureGeneral(ctx); err != nil {
		t.Fatal(err)
	}
	sam := identityBus(t, co, "sam")
	c, err := newClient(ctx, identityBus(t, co, "eric"), "eric", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	id := c.sessionID()
	offset.Store(int64(time.Hour)) // far past the lease plus the skew
	msgs, _ := loopRun(t, c, nil)
	waitFor(t, 5*time.Second, func() bool {
		ids, err := sam.Identities(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range ids {
			if in.Name == "eric" && len(in.Sessions) == 1 {
				return true
			}
		}
		return false
	})
	if _, err := sam.Send(ctx, bus.SendInput{Channel: "general", Content: "after the nap"}); err != nil {
		t.Fatal(err)
	}
	b, ok := nextMsg(t, msgs, 5*time.Second).(batchMsg)
	if !ok || !slices.Equal(contents(b.res), []string{"after the nap"}) {
		t.Fatalf("first message after the lapse = %#v", b)
	}
	if got := c.sessionID(); got != id {
		t.Fatalf("session id after the resume = %q, want %q (a real resume keeps it)", got, id)
	}
	if got := c.displayName(); got != "eric/tui" {
		t.Fatalf("display name after the resume = %q", got)
	}
}

// TestSweptSessionResumes: a session row the retention sweep deleted answers
// not_found; the loop resumes it like a lapse (Open with the old token starts
// a fresh session, Decision 18) instead of retrying forever (post-build
// decision 7). Watch and Receive are both session-scoped.
func TestSweptSessionResumes(t *testing.T) {
	for _, where := range []string{"watch", "receive"} {
		t.Run(where, func(t *testing.T) {
			setBackoff(t, 10*time.Millisecond)
			var (
				mu      sync.Mutex
				opens   []bus.OpenSessionInput
				watches atomic.Int32
				recvs   atomic.Int32
			)
			sb := &stubBus{}
			sb.open = func(_ context.Context, in bus.OpenSessionInput) (bus.Session, error) {
				mu.Lock()
				opens = append(opens, in)
				mu.Unlock()
				return bus.Session{ID: "s2", Identity: "eric", DisplayName: "eric/tui", ResumeToken: "t2"}, nil
			}
			notFound := bus.Errorf(bus.CodeNotFound, false, "session not found")
			sb.watch = func(ctx context.Context, _ bus.WatchInput) (bus.WatchResult, error) {
				n := watches.Add(1)
				if where == "watch" && n == 1 {
					return bus.WatchResult{}, notFound
				}
				if n <= 2 {
					return bus.WatchResult{Woke: true, Seq: 1}, nil
				}
				return blockUntilDone(ctx)
			}
			sb.receive = func(_ context.Context, _ bus.ReceiveInput) (bus.ReceiveResult, error) {
				n := recvs.Add(1)
				if where == "receive" && n == 1 {
					return bus.ReceiveResult{}, notFound
				}
				if n <= 2 {
					return bus.ReceiveResult{Messages: []bus.Message{{Seq: 1}}, Batch: "b1"}, nil
				}
				return bus.ReceiveResult{}, nil
			}
			c := newStubClient(t, sb, bus.Session{ID: "s1", Identity: "eric", DisplayName: "eric/tui", ResumeToken: "t1"})
			msgs, done := loopRun(t, c, nil)
			if _, ok := nextMsg(t, msgs, 5*time.Second).(batchMsg); !ok {
				t.Fatal("first message after the resume is not a batch (a receiveErrMsg means no resume)")
			}
			waitFor(t, 5*time.Second, func() bool { return c.sessionID() == "s2" })
			stopLoop(t, c, done)
			mu.Lock()
			defer mu.Unlock()
			if len(opens) != 1 || opens[0].ResumeToken != "t1" || opens[0].Hint != sessionHint {
				t.Fatalf("Open calls = %+v, want one with ResumeToken t1 and hint %q", opens, sessionHint)
			}
		})
	}
}

// TestSweptSessionNotFoundTwiceIsReported: not_found again after the resume,
// with no successful call between, is reported (the lapsedOnce guard), not an
// endless resume loop.
func TestSweptSessionNotFoundTwiceIsReported(t *testing.T) {
	for _, where := range []string{"watch", "receive"} {
		t.Run(where, func(t *testing.T) {
			setBackoff(t, 10*time.Millisecond)
			var opens atomic.Int32
			sb := &stubBus{}
			sb.open = func(context.Context, bus.OpenSessionInput) (bus.Session, error) {
				opens.Add(1)
				return bus.Session{ID: "s2", Identity: "eric", ResumeToken: "t2"}, nil
			}
			notFound := bus.Errorf(bus.CodeNotFound, false, "session not found")
			sb.watch = func(context.Context, bus.WatchInput) (bus.WatchResult, error) {
				if where == "watch" {
					return bus.WatchResult{}, notFound
				}
				return bus.WatchResult{Woke: true, Seq: 1}, nil
			}
			sb.receive = func(context.Context, bus.ReceiveInput) (bus.ReceiveResult, error) {
				return bus.ReceiveResult{}, notFound
			}
			c := newStubClient(t, sb, bus.Session{ID: "s1", Identity: "eric", ResumeToken: "t1"})
			msgs, done := loopRun(t, c, nil)
			for range 2 {
				if _, ok := nextMsg(t, msgs, 5*time.Second).(receiveErrMsg); !ok {
					t.Fatal("want receiveErrMsg for the second not_found")
				}
			}
			if got := opens.Load(); got != 1 {
				t.Fatalf("Open called %d times without a success between, want 1", got)
			}
			stopLoop(t, c, done)
		})
	}
}

// TestNewClientUsesHint: Options.Hint names the session (TUI decision 12), an
// empty one means tui, and a second client on the same host is -2.
func TestNewClientUsesHint(t *testing.T) {
	f := newBusFixture(t)
	c, err := newClient(context.Background(), identityBus(t, f.core, "zed"), "zed", "macmini")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	if got := c.displayName(); got != "zed/macmini" {
		t.Fatalf("display name = %q, want zed/macmini", got)
	}
	c2, err := newClient(context.Background(), identityBus(t, f.core, "zed"), "zed", "macmini")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c2.close() })
	if got := c2.displayName(); got != "zed/macmini-2" {
		t.Fatalf("second display name = %q, want zed/macmini-2", got)
	}
}

// openSpy records every Open input of the bus it wraps.
type openSpy struct {
	bus.Bus
	opens []bus.OpenSessionInput
}

func (o *openSpy) Open(ctx context.Context, in bus.OpenSessionInput) (bus.Session, error) {
	o.opens = append(o.opens, in)
	return o.Bus.Open(ctx, in)
}

// TestHintSentOnOpenAndResume: the first Open and the Open after a lapse send
// the client's hint; an empty hint sends tui.
func TestHintSentOnOpenAndResume(t *testing.T) {
	for _, tc := range []struct{ hint, want string }{{"macmini", "macmini"}, {"", "tui"}} {
		t.Run(tc.want, func(t *testing.T) {
			f := newBusFixture(t)
			spy := &openSpy{Bus: identityBus(t, f.core, "zed")}
			c, err := newClient(context.Background(), spy, "zed", tc.hint)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.close() })
			if err := c.resumeLapsed(); err != nil {
				t.Fatal(err)
			}
			if len(spy.opens) != 2 || spy.opens[0].Hint != tc.want || spy.opens[1].Hint != tc.want || spy.opens[1].ResumeToken == "" {
				t.Fatalf("Open calls = %+v, want two with hint %q, the second with the resume token", spy.opens, tc.want)
			}
		})
	}
}
