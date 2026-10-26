package bus

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// deliveryChannel creates a channel through the Store with an open ACL
// (everyone read and write), so that only the scope gate and the memory rule
// decide what the tests below see.
func (h *harness) deliveryChannel(name string, kind ab2store.Kind, scope string) ab2store.Channel {
	h.t.Helper()
	var ch ab2store.Channel
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		ch, err = tx.CreateChannel(ctx, ab2store.ChannelCreate{
			Name: name, Kind: kind, Scope: scope, CreatedSeq: seq, CreatedAt: h.core.NowMs(),
			ACL: []ab2store.ACLEntry{{Identity: ab2store.Everyone, Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}}},
		})
		return err
	})
	return ch
}

// rawSubscribe subscribes identity to the channel at the start, past the Bus
// (the Bus refuses some of these subscriptions, which is the point).
func (h *harness) rawSubscribe(identity string, ch ab2store.Channel) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		return tx.Subscribe(ctx, identity, ch.ID, 0, h.core.NowMs())
	})
}

func channelsOf(msgs []ab2bus.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Channel)
	}
	return out
}

// TestReceiveAfterIdentityDeleted: Receive, Peek and Watch resolve the
// caller's identity first (accesscheck spec 3.5), so a deleted identity is
// not_registered whatever the session says.
func TestReceiveAfterIdentityDeleted(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b, s := recvOpen(h, "kim", "dev")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.RetireIdentity(ctx, "kim", h.core.NowMs())
			return err
		})
		_, err := b.Receive(ctx, ab2bus.ReceiveInput{Session: s.ID})
		codeOf(t, err, ab2bus.CodeNotRegistered)
		_, err = b.Peek(ctx, ab2bus.PeekInput{Session: s.ID})
		codeOf(t, err, ab2bus.CodeNotRegistered)
		_, err = b.Watch(ctx, ab2bus.WatchInput{Session: s.ID, Timeout: time.Second})
		codeOf(t, err, ab2bus.CodeNotRegistered)
	})
}

// TestSessionNotOwnedIsNotFound: another identity's session answers exactly as
// a session that never existed, on every session-scoped call (pins).
func TestSessionNotOwnedIsNotFound(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		_, ps := recvOpen(h, "pat", "dev")
		kim := h.as("kim")
		never := strings.Repeat("0", len(ps.ID))
		calls := map[string]func(id string) error{
			"Receive": func(id string) error { _, err := kim.Receive(ctx, ab2bus.ReceiveInput{Session: id}); return err },
			"Peek":    func(id string) error { _, err := kim.Peek(ctx, ab2bus.PeekInput{Session: id}); return err },
			"Watch": func(id string) error {
				_, err := kim.Watch(ctx, ab2bus.WatchInput{Session: id, Timeout: time.Second})
				return err
			},
			"Send": func(id string) error {
				_, err := kim.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "x", Session: id})
				return err
			},
			"CloseSession": func(id string) error { return kim.Close(ctx, ab2bus.CloseSessionInput{Session: id}) },
		}
		for name, call := range calls {
			foreign, unknown := call(ps.ID), call(never)
			codeOf(t, foreign, ab2bus.CodeNotFound)
			codeOf(t, unknown, ab2bus.CodeNotFound)
			if want := `session "` + ps.ID + `" does not exist`; msgOf(foreign) != want {
				t.Errorf("%s foreign: %q, want %q", name, msgOf(foreign), want)
			}
			if want := `session "` + never + `" does not exist`; msgOf(unknown) != want {
				t.Errorf("%s unknown: %q, want %q", name, msgOf(unknown), want)
			}
		}
		// Open with another identity's resume token keeps its own text.
		_, err := kim.Open(ctx, ab2bus.OpenSessionInput{ResumeToken: ps.ResumeToken})
		codeOf(t, err, ab2bus.CodeNotFound)
		if want := `resume token does not name a session of "kim"`; msgOf(err) != want {
			t.Errorf("Open foreign token: %q, want %q", msgOf(err), want)
		}
	})
}

// TestAgentNotDeliveredMemory: the server never delivers a memory channel to
// an agent (decision 20), whatever its subscriptions; a human is delivered it.
func TestAgentNotDeliveredMemory(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		mem := h.deliveryChannel("mem", ab2store.KindMemory, "")
		chat := h.deliveryChannel("chat", ab2store.KindOrdinary, "")
		kim := h.as("kim")
		hal := h.human("hal@corp.example")
		for _, id := range []string{"kim", "hal@corp.example"} {
			h.ensureIdentity(id)
			h.rawSubscribe(id, mem)
			h.rawSubscribe(id, chat)
		}
		h.append(mem, "pat", "", "a memory about rate limits")
		h.append(chat, "pat", "", "hello")
		ks, err := kim.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		hs, err := hal.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		wantPeek(t, peekOf(t, kim, ab2bus.PeekInput{Session: ks.ID}), 1, ab2bus.PeekChannel{Channel: "chat", Count: 1})
		wantPeek(t, peekOf(t, hal, ab2bus.PeekInput{Session: hs.ID}), 2,
			ab2bus.PeekChannel{Channel: "chat", Count: 1}, ab2bus.PeekChannel{Channel: "mem", Count: 1})
		if got := channelsOf(recv(t, kim, ab2bus.ReceiveInput{Session: ks.ID}).Messages); !slices.Equal(got, []string{"chat"}) {
			t.Fatalf("agent received %v, want [chat]", got)
		}
		got := channelsOf(recv(t, hal, ab2bus.ReceiveInput{Session: hs.ID}).Messages)
		slices.Sort(got)
		if !slices.Equal(got, []string{"chat", "mem"}) {
			t.Fatalf("human received %v, want [chat mem]", got)
		}
		err = kim.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "mem"})
		codeOf(t, err, ab2bus.CodeValidation)
		if !strings.Contains(msgOf(err), "is a memory channel") {
			t.Fatalf("Subscribe text = %q", msgOf(err))
		}
		res, err := kim.Search(ctx, ab2bus.SearchInput{Query: "rate", Mode: "text"})
		if err != nil || len(res.Hits) != 1 || res.Hits[0].Channel != "mem" {
			t.Fatalf("agent Search = %+v, %v", res, err)
		}
	})
}

// TestScopeGateDelivery: a scoped identity is delivered, counted and woken for
// the channels of its scope only; a Receive channel filter naming a channel
// outside it answers as for a name that never existed. An unscoped identity
// with the same subscriptions is unchanged.
func TestScopeGateDelivery(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		pat := h.localAs("pat")
		work := h.deliveryChannel("work", ab2store.KindOrdinary, "job1")
		other := h.deliveryChannel("other", ab2store.KindOrdinary, "")
		for _, id := range []string{"job1-coder", "pat"} {
			h.rawSubscribe(id, work)
			h.rawSubscribe(id, other)
		}
		tagger := h.scopedIdentity("job1-tagger", "job1")
		if err := tagger.SubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"env:*"}}); err != nil {
			t.Fatal(err)
		}
		ts, err := tagger.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		h.append(work, "root", "", "in scope", "env:prod")
		h.append(other, "root", "", "out of scope", "env:prod")
		cs, err := coder.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		ps, err := pat.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		wantPeek(t, peekOf(t, coder, ab2bus.PeekInput{Session: cs.ID}), 1, ab2bus.PeekChannel{Channel: "work", Count: 1})
		wantPeek(t, peekOf(t, pat, ab2bus.PeekInput{Session: ps.ID}), 2,
			ab2bus.PeekChannel{Channel: "other", Count: 1}, ab2bus.PeekChannel{Channel: "work", Count: 1})

		// The filter: other is as unknown as a name never created.
		_, neverErr := coder.Receive(ctx, ab2bus.ReceiveInput{Session: cs.ID, Channels: []string{"never"}})
		codeOf(t, neverErr, ab2bus.CodeNotFound)
		_, otherErr := coder.Receive(ctx, ab2bus.ReceiveInput{Session: cs.ID, Channels: []string{"other"}})
		codeOf(t, otherErr, ab2bus.CodeNotFound)
		if msgOf(otherErr) != `channel "other" does not exist` || msgOf(neverErr) != `channel "never" does not exist` {
			t.Fatalf("filter texts: %q, %q", msgOf(otherErr), msgOf(neverErr))
		}

		if got := channelsOf(recv(t, coder, ab2bus.ReceiveInput{Session: cs.ID}).Messages); !slices.Equal(got, []string{"work"}) {
			t.Fatalf("scoped receive = %v, want [work]", got)
		}
		got := channelsOf(recv(t, pat, ab2bus.ReceiveInput{Session: ps.ID}).Messages)
		slices.Sort(got)
		if !slices.Equal(got, []string{"other", "work"}) {
			t.Fatalf("unscoped receive = %v, want [other work]", got)
		}

		// Search without a channel finds the in-scope message only.
		res, err := coder.Search(ctx, ab2bus.SearchInput{Query: "scope", Mode: "text"})
		if err != nil || len(res.Hits) != 1 || res.Hits[0].Channel != "work" {
			t.Fatalf("scoped Search = %+v, %v", res, err)
		}

		// A tag subscription matches only in scope.
		if got := channelsOf(recv(t, tagger, ab2bus.ReceiveInput{Session: ts.ID, IncludeOwn: true}).Messages); !slices.Equal(got, []string{"work"}) {
			t.Fatalf("scoped tag receive = %v, want [work]", got)
		}

		// Watch wakes on work only: a message on other does not wake the coder.
		h.append(other, "root", "", "again out of scope")
		o := awaitWatch(t, startWatch(ctx, coder, ab2bus.WatchInput{Session: cs.ID, Timeout: 300 * time.Millisecond}), 3*time.Second)
		if o.err != nil || o.res.Woke {
			t.Fatalf("scoped watch woke on an out-of-scope channel: %+v, %v", o.res, o.err)
		}
		o = awaitWatch(t, startWatch(ctx, pat, ab2bus.WatchInput{Session: ps.ID, Timeout: 2 * time.Second}), 3*time.Second)
		if o.err != nil || !o.res.Woke || !slices.Contains(o.res.Channels, "other") {
			t.Fatalf("unscoped watch = %+v, %v", o.res, o.err)
		}
	})
}

// TestAgentMemoryDoesNotWake: a memory appended to a channel an agent is
// subscribed to, or reaches through a tag subscription, neither wakes its Watch
// nor counts in its Peek (decision 20, server side).
func TestAgentMemoryDoesNotWake(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		ctx := context.Background()
		mem := h.deliveryChannel("mem", ab2store.KindMemory, "")
		mem2 := h.deliveryChannel("mem2", ab2store.KindMemory, "")
		kim := h.as("kim")
		h.rawSubscribe("kim", mem)
		if err := kim.SubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"x"}}); err != nil {
			t.Fatal(err)
		}
		s, err := kim.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		h.append(mem, "pat", "", "direct memory")
		h.append(mem2, "pat", "", "tagged memory", "x")
		wantPeek(t, peekOf(t, kim, ab2bus.PeekInput{Session: s.ID}), 0)
		o := awaitWatch(t, startWatch(ctx, kim, ab2bus.WatchInput{Session: s.ID, Timeout: 300 * time.Millisecond}), 3*time.Second)
		if o.err != nil || o.res.Woke {
			t.Fatalf("a memory woke the agent: %+v, %v", o.res, o.err)
		}
	})
}
