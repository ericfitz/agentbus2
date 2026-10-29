package bus

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func inspectMaxSeq(h *harness) (n int64) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) (err error) { n, err = tx.MaxSeq(ctx); return })
	return
}

func inspectNotice(h *harness) (found bool) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) (err error) {
		_, found, err = tx.Notice(ctx, core.NoticeInspection)
		return
	})
	return
}

func inspectHasReceipt(h *harness, identity, key string) (ok bool) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) (err error) {
		_, ok, err = tx.Receipt(ctx, identity, key, h.core.NowMs())
		return
	})
	return
}

func TestInspectSendRequest(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		s := h.session("pat", "coder")
		b := h.as("pat")
		first, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "first"})
		if err != nil {
			t.Fatalf("first send: %v", err)
		}
		subj, err := rules.NormalizeSubject(" Hello ")
		if err != nil {
			t.Fatal(err)
		}
		tags, err := rules.NormalizeTags([]string{"B", "a"})
		if err != nil {
			t.Fatal(err)
		}
		in := ab2bus.SendInput{
			Channel: "dev", Subject: " Hello ", Content: "body", Type: "note", Tags: []string{"B", "a"},
			Metadata: map[string]string{"k": "v"}, ReplyTo: &first.Seq, Session: s.ID,
		}
		if _, err := b.Send(ctx, in); err != nil {
			t.Fatalf("send: %v", err)
		}
		calls := fi.calls()
		if len(calls) != 2 {
			t.Fatalf("%d inspector calls, want 2", len(calls))
		}
		got := calls[1]
		if len(got.RequestID) != 16 {
			t.Errorf("request id %q is not 16 characters", got.RequestID)
		}
		got.RequestID = ""
		want := core.InspectRequest{
			Version: 1, Operation: "send", Source: "api", Sender: "pat", SenderKind: "agent", Session: "coder",
			Channel: core.InspectChannel{Name: "dev", Kind: "ordinary"},
			Record: core.InspectRecord{
				Subject: subj, Content: "body", Type: "note", Tags: tags,
				Metadata: map[string]string{"k": "v"}, ReplyTo: &first.Seq,
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("request = %+v, want %+v", got, want)
		}
		if n := inspectMaxSeq(h); n != 2 {
			t.Errorf("MaxSeq = %d, want 2", n)
		}

		// A human sender says so.
		hb := h.human("eric@example.com")
		if _, err := hb.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "from a human"}); err != nil {
			t.Fatalf("human send: %v", err)
		}
		calls = fi.calls()
		if len(calls) != 3 || calls[2].SenderKind != "human" || calls[2].Sender != "eric@example.com" || calls[2].Session != "" {
			t.Errorf("human request = %+v", calls[len(calls)-1])
		}
	})
}

func TestInspectSendRefuse(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		fi := &fakeInspector{reply: func(context.Context, core.InspectRequest) ([]byte, error) {
			return refuseJSON("pii", "contains a key"), nil
		}}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		sub, cancel := h.notifier.Subscribe()
		defer cancel()
		before := inspectMaxSeq(h)
		_, err := h.as("pat").Send(context.Background(), ab2bus.SendInput{Channel: "dev", Content: "secret"})
		var be *ab2bus.Error
		if !errors.As(err, &be) {
			t.Fatalf("error = %v, want a bus error", err)
		}
		if be.Code != ab2bus.CodeInspectionRejected || be.Retryable || be.Message != "rejected by inspection (pii): contains a key" {
			t.Errorf("error = %+v", be)
		}
		if n := inspectMaxSeq(h); n != before {
			t.Errorf("MaxSeq = %d, want %d", n, before)
		}
		if got := sendDrainHints(sub); len(got) != 0 {
			t.Errorf("a refused send published %v", got)
		}
	})
}

func TestInspectSendChargesRefusal(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		var n atomic.Int32
		fi := &fakeInspector{reply: func(context.Context, core.InspectRequest) ([]byte, error) {
			if n.Add(1) == 1 {
				return refuseJSON("policy", "no"), nil
			}
			return []byte(allowJSON), nil
		}}
		h := newHarness(t, e, harnessOptions{
			cfg:       sendCfgWith(func(c *core.Config) { c.SendMessagesPerSecond = 1 }),
			inspector: fi,
		})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		_, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "one"})
		codeOf(t, err, ab2bus.CodeInspectionRejected)
		_, err = b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "two"})
		codeOf(t, err, ab2bus.CodeRateLimited)
		if c := len(fi.calls()); c != 1 {
			t.Errorf("%d inspector calls, want 1 (the pre-pass rate check runs before inspection)", c)
		}
	})
}

func TestInspectSendKeyed(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		var allow atomic.Bool
		fi := &fakeInspector{reply: func(context.Context, core.InspectRequest) ([]byte, error) {
			if allow.Load() {
				return []byte(allowJSON), nil
			}
			return refuseJSON("policy", "no"), nil
		}}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		in := ab2bus.SendInput{Channel: "dev", Content: "keyed", IdempotencyKey: "k1"}

		_, err := b.Send(ctx, in)
		codeOf(t, err, ab2bus.CodeInspectionRejected)
		_, err = b.Send(ctx, in)
		codeOf(t, err, ab2bus.CodeInspectionRejected)
		if c := len(fi.calls()); c != 2 {
			t.Fatalf("%d inspector calls after two refusals, want 2", c)
		}
		if inspectHasReceipt(h, "pat", "k1") {
			t.Fatal("a refused keyed send left a receipt")
		}

		allow.Store(true)
		res, err := b.Send(ctx, in)
		if err != nil {
			t.Fatalf("allowed send: %v", err)
		}
		if c := len(fi.calls()); c != 3 {
			t.Fatalf("%d inspector calls, want 3", c)
		}
		again, err := b.Send(ctx, in)
		if err != nil || again != res {
			t.Fatalf("replay = %+v, %v; want %+v", again, err, res)
		}
		if c := len(fi.calls()); c != 3 {
			t.Errorf("%d inspector calls after the replay, want 3", c)
		}
		other := in
		other.Content = "other"
		_, err = b.Send(ctx, other)
		codeOf(t, err, ab2bus.CodeConflict)
		if c := len(fi.calls()); c != 3 {
			t.Errorf("%d inspector calls after the conflict, want 3", c)
		}
		if n := inspectMaxSeq(h); n != 1 {
			t.Errorf("MaxSeq = %d, want 1", n)
		}
	})
}

// TestInspectSendKeyedOverlap: two keyed sends with the same key and content
// both inspect (blocked together on a barrier); one message and one receipt
// are stored, and both calls answer alike.
func TestInspectSendKeyedOverlap(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		var arrived atomic.Int32
		release := make(chan struct{})
		fi := &fakeInspector{reply: func(ctx context.Context, _ core.InspectRequest) ([]byte, error) {
			arrived.Add(1)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return []byte(allowJSON), nil
		}}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		in := ab2bus.SendInput{Channel: "dev", Content: "overlap", IdempotencyKey: "k1"}
		type result struct {
			res ab2bus.SendResult
			err error
		}
		out := make(chan result, 2)
		for i := 0; i < 2; i++ {
			go func() {
				r, err := b.Send(ctx, in)
				out <- result{r, err}
			}()
		}
		deadline := time.After(10 * time.Second)
		for arrived.Load() < 2 {
			select {
			case <-deadline:
				t.Fatalf("only %d of 2 sends reached the inspector", arrived.Load())
			case <-time.After(5 * time.Millisecond):
			}
		}
		close(release)
		var got [2]result
		for i := range got {
			select {
			case got[i] = <-out:
			case <-deadline:
				t.Fatal("sends did not finish")
			}
		}
		for i, r := range got {
			if r.err != nil {
				t.Fatalf("send %d: %v", i, r.err)
			}
		}
		if got[0].res != got[1].res {
			t.Errorf("results differ: %+v vs %+v", got[0].res, got[1].res)
		}
		if n := inspectMaxSeq(h); n != 1 {
			t.Errorf("MaxSeq = %d, want 1 stored message", n)
		}
		if !inspectHasReceipt(h, "pat", "k1") {
			t.Error("no receipt stored")
		}
	})
}

func TestInspectSendRefusalsBeforeInspection(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{
			cfg:       sendCfgWith(func(c *core.Config) { c.SendMessagesPerSecond = 1 }),
			inspector: fi,
		})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		h.channel("work", ab2store.KindTasks, "pat", nil)
		h.channel("readonly", ab2store.KindOrdinary, "kim", []ab2store.ACLEntry{
			{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead}},
		})
		h.channel("other", ab2store.KindOrdinary, "kim", nil)
		h.rtScope("job1")
		h.channel("outside", ab2store.KindOrdinary, "pat", nil)
		closed := h.session("pat", "closed")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.CloseSession(ctx, closed.ID, h.core.NowMs())
		})
		foreign := h.as("kim")
		otherMsg, err := foreign.Send(ctx, ab2bus.SendInput{Channel: "other", Content: "theirs"})
		if err != nil {
			t.Fatal(err)
		}
		base := len(fi.calls())
		scoped := h.scopedIdentity("job1-coder", "job1")
		pat := h.as("pat")

		cases := []struct {
			name string
			b    ab2bus.Bus
			in   ab2bus.SendInput
			want ab2bus.Code
		}{
			{"empty content", pat, ab2bus.SendInput{Channel: "dev"}, ab2bus.CodeValidation},
			{"tasks channel", pat, ab2bus.SendInput{Channel: "work", Content: "x"}, ab2bus.CodeValidation},
			{"no write row", pat, ab2bus.SendInput{Channel: "readonly", Content: "x"}, ab2bus.CodeForbidden},
			{"out of scope", scoped, ab2bus.SendInput{Channel: "outside", Content: "x"}, ab2bus.CodeNotFound},
			{"closed session", pat, ab2bus.SendInput{Channel: "dev", Content: "x", Session: closed.ID}, ab2bus.CodeSessionClosed},
			{"reply_to in another channel", pat, ab2bus.SendInput{Channel: "dev", Content: "x", ReplyTo: &otherMsg.Seq}, ab2bus.CodeValidation},
		}
		for _, c := range cases {
			_, err := c.b.Send(ctx, c.in)
			if got := ab2bus.CodeOf(err); got != c.want {
				t.Errorf("%s: code = %q (%v), want %q", c.name, got, err, c.want)
			}
		}
		if c := len(fi.calls()) - base; c != 0 {
			t.Errorf("%d inspector calls for refusals that precede inspection, want 0", c)
		}
		// The rate limit: the first send is inspected, the second is refused first.
		if _, err := pat.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "ok"}); err != nil {
			t.Fatalf("first send: %v", err)
		}
		_, err = pat.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "again"})
		codeOf(t, err, ab2bus.CodeRateLimited)
		if c := len(fi.calls()) - base; c != 1 {
			t.Errorf("%d inspector calls around the rate limit, want 1", c)
		}
	})
}

func TestInspectSendRetriedTxInspectsOnce(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi, fault: &pgconnError40001})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		// The first inspection also clears the notice state in a Store.Tx,
		// which would spend the fault; spend it there on purpose, then re-arm.
		fs := h.store.(*faultStore)
		fs.mu.Lock()
		fs.fired = true
		fs.mu.Unlock()
		if _, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "warm"}); err != nil {
			t.Fatalf("warm-up send: %v", err)
		}
		fs.mu.Lock()
		fs.fired = false
		fs.mu.Unlock()
		base, before := len(fi.calls()), inspectMaxSeq(h)

		_, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "faulted"})
		if e.Name == "postgres" {
			if err != nil {
				t.Fatalf("postgres retries the fault: %v", err)
			}
			if n := inspectMaxSeq(h); n != before+1 {
				t.Errorf("MaxSeq = %d, want %d", n, before+1)
			}
		} else {
			codeOf(t, err, ab2bus.CodeInternal)
			if n := inspectMaxSeq(h); n != before {
				t.Errorf("MaxSeq = %d, want %d (nothing committed)", n, before)
			}
		}
		if c := len(fi.calls()) - base; c != 1 {
			t.Errorf("%d inspector calls, want 1", c)
		}
	})
}

func TestInspectSlowInspectorHoldsNoLock(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		release := make(chan struct{})
		started := make(chan struct{}, 1)
		fi := &fakeInspector{reply: func(ctx context.Context, req core.InspectRequest) ([]byte, error) {
			if req.Sender == "pat" {
				started <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return []byte(allowJSON), nil
		}}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		pat, kim := h.as("pat"), h.as("kim")

		var wg sync.WaitGroup
		var patErr error
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, patErr = pat.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "slow"})
		}()
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatal("the slow inspection never started")
		}
		kctx, kcancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := kim.Send(kctx, ab2bus.SendInput{Channel: "dev", Content: "fast"})
		kcancel()
		close(release)
		wg.Wait()
		if err != nil {
			t.Errorf("kim's send while pat's inspection was blocked: %v", err)
		}
		if patErr != nil {
			t.Errorf("pat's send: %v", patErr)
		}
		if n := inspectMaxSeq(h); n != 2 {
			t.Errorf("MaxSeq = %d, want 2", n)
		}
	})
}

func TestInspectSendFailureModes(t *testing.T) {
	type mode struct {
		name    string
		timeout float64
		reply   func(ctx context.Context, req core.InspectRequest) ([]byte, error)
	}
	raw := func(s string) func(context.Context, core.InspectRequest) ([]byte, error) {
		return func(context.Context, core.InspectRequest) ([]byte, error) { return []byte(s), nil }
	}
	modes := []mode{
		{"transport error", 0, func(context.Context, core.InspectRequest) ([]byte, error) {
			return nil, &core.InspectError{Class: core.FailTransport, Err: errors.New("connection refused")}
		}},
		{"not json", 0, raw("not json")},
		{"unknown member", 0, raw(`{"verdict":"allow","extra":1}`)},
		{"redact", 0, raw(`{"verdict":"redact"}`)},
		{"trailing value", 0, raw(`{"verdict":"allow"} {}`)},
		{"timeout", 0.1, func(ctx context.Context, _ core.InspectRequest) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
	}
	for _, m := range modes {
		for _, onFail := range []string{core.InspectionClosed, core.InspectionOpen} {
			t.Run(m.name+"/"+onFail, func(t *testing.T) {
				dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
					fi := &fakeInspector{reply: m.reply}
					h := newHarness(t, e, harnessOptions{
						inspector: fi,
						cfg: sendCfgWith(func(c *core.Config) {
							c.InspectionOnFailure = onFail
							if m.timeout > 0 {
								c.InspectionTimeoutSeconds = m.timeout
							}
						}),
					})
					h.channel("dev", ab2store.KindOrdinary, "pat", nil)
					_, err := h.as("pat").Send(context.Background(), ab2bus.SendInput{Channel: "dev", Content: "x"})
					if !inspectNotice(h) {
						t.Error("the inspection notice is not set")
					}
					if onFail == core.InspectionOpen {
						if err != nil {
							t.Fatalf("fail-open send: %v", err)
						}
						if n := inspectMaxSeq(h); n != 1 {
							t.Errorf("MaxSeq = %d, want 1", n)
						}
						return
					}
					var be *ab2bus.Error
					if !errors.As(err, &be) || be.Code != ab2bus.CodeUnavailable || !be.Retryable {
						t.Fatalf("error = %v, want retryable unavailable", err)
					}
					if n := inspectMaxSeq(h); n != 0 {
						t.Errorf("MaxSeq = %d, want 0", n)
					}
				})
			})
		}
	}
}

func TestInspectSendChannelRecreated(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		for _, always := range []bool{false, true} {
			name := "once"
			if always {
				name = "every call"
			}
			t.Run(name, func(t *testing.T) {
				var h *harness
				var n atomic.Int32
				fi := &fakeInspector{reply: func(context.Context, core.InspectRequest) ([]byte, error) {
					if n.Add(1) == 1 || always {
						old := h.channelID("dev")
						h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteChannel(ctx, old) })
						h.channel("dev", ab2store.KindOrdinary, "pat", nil)
					}
					return []byte(allowJSON), nil
				}}
				h = newHarness(t, e, harnessOptions{inspector: fi})
				h.channel("dev", ab2store.KindOrdinary, "pat", nil)
				_, err := h.as("pat").Send(context.Background(), ab2bus.SendInput{Channel: "dev", Content: "x"})
				if c := len(fi.calls()); c != 2 {
					t.Errorf("%d inspector calls, want 2", c)
				}
				if always {
					var be *ab2bus.Error
					if !errors.As(err, &be) || be.Code != ab2bus.CodeConflict || !be.Retryable {
						t.Fatalf("error = %v, want retryable conflict", err)
					}
					if n := inspectMaxSeq(h); n != 0 {
						t.Errorf("MaxSeq = %d, want 0", n)
					}
					return
				}
				if err != nil {
					t.Fatalf("send: %v", err)
				}
				cur := h.channelID("dev")
				h.tx(func(ctx context.Context, tx ab2store.Tx) error {
					m, err := tx.Message(ctx, inspectMaxSeqTx(ctx, tx))
					if err != nil {
						return err
					}
					if m.ChannelID != cur {
						t.Errorf("message landed on channel %d, want the new %d", m.ChannelID, cur)
					}
					return nil
				})
			})
		}
	})
}

func inspectMaxSeqTx(ctx context.Context, tx ab2store.Tx) int64 {
	n, _ := tx.MaxSeq(ctx)
	return n
}

// channelID is the id of the channel named name.
func (h *harness) channelID(name string) (id int64) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		ch, err := tx.ChannelByName(ctx, name)
		id = ch.ID
		return err
	})
	return
}
