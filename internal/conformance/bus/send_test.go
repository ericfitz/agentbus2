package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func sendInt64p(v int64) *int64 { return &v }

func sendCfgWith(f func(*core.Config)) *core.Config {
	c := core.Defaults()
	f(&c)
	return &c
}

func TestSendRoundTrip(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		sess := h.session("pat", "pat/impl")
		b := h.as("pat")
		res, err := b.Send(ctx, ab2bus.SendInput{
			Channel: "dev", Content: "hello", Subject: "  greet  ", Type: "note",
			Metadata: map[string]string{"k": "v"}, Tags: []string{"Zeta", "alpha", "ALPHA"}, Session: sess.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Seq != 1 || res.MemoryID != nil {
			t.Fatalf("result = %+v, want seq 1 and no memory id", res)
		}
		got, err := b.History(ctx, ab2bus.HistoryInput{Channel: "dev"})
		if err != nil || len(got) != 1 {
			t.Fatalf("History = %v, %v", got, err)
		}
		m := got[0]
		if m.Seq != 1 || m.Channel != "dev" || m.Sender != "pat" || m.Session != sess.ID || m.Display != "pat/impl" ||
			m.Subject != "greet" || m.Content != "hello" || m.Type != "note" || m.Metadata["k"] != "v" ||
			m.CreatedAt != fixedNowMs || !slices.Equal(m.Tags, []string{"alpha", "zeta"}) {
			t.Fatalf("message = %+v", m)
		}
	})
}

func TestSendNeedsWrite(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		kim := h.as("kim")
		h.channel("ro", ab2store.KindOrdinary, "pat", []ab2store.ACLEntry{{Identity: "kim", Permissions: []ab2store.Permission{ab2store.PermRead}}})
		_, err := kim.Send(ctx, ab2bus.SendInput{Channel: "ro", Content: "x"})
		codeOf(t, err, ab2bus.CodeForbidden)
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			ch, err := tx.ChannelByName(ctx, "ro")
			if err != nil {
				return err
			}
			return tx.SetACL(ctx, ch.ID, []ab2store.ACLEntry{{Identity: "kim", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}}})
		})
		res, err := kim.Send(ctx, ab2bus.SendInput{Channel: "ro", Content: "x"})
		if err != nil || res.Seq == 0 {
			t.Fatalf("Send after grant = %+v, %v", res, err)
		}
	})
}

func TestSendRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		h.channel("work", ab2store.KindTasks, "pat", nil)
		b := h.as("pat")
		other := h.session("kim", "kim")
		lapsed := h.session("pat", "pat/old")
		closed := h.session("pat", "pat/gone")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.CloseSession(ctx, closed.ID, h.core.NowMs())
		})
		// Lapse only "lapsed": advance, then open a fresh live session afterwards.
		h.clock.Advance(time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds+1) * time.Second)
		before := lapsed.LeaseExpiresAt

		tooMany := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "a11"}
		cases := []struct {
			name string
			in   ab2bus.SendInput
			want ab2bus.Code
		}{
			{"empty content", ab2bus.SendInput{Channel: "dev"}, ab2bus.CodeValidation},
			{"empty channel", ab2bus.SendInput{Content: "x"}, ab2bus.CodeValidation},
			{"unknown channel", ab2bus.SendInput{Channel: "nope", Content: "x"}, ab2bus.CodeNotFound},
			{"tasks channel", ab2bus.SendInput{Channel: "work", Content: "x"}, ab2bus.CodeValidation},
			{"11 tags", ab2bus.SendInput{Channel: "dev", Content: "x", Tags: tooMany}, ab2bus.CodeValidation},
			{"bad tag", ab2bus.SendInput{Channel: "dev", Content: "x", Tags: []string{"Bad Tag"}}, ab2bus.CodeValidation},
			{"multiline subject", ab2bus.SendInput{Channel: "dev", Content: "x", Subject: "a\nb"}, ab2bus.CodeValidation},
			{"reply_to missing", ab2bus.SendInput{Channel: "dev", Content: "x", ReplyTo: sendInt64p(99)}, ab2bus.CodeNotFound},
			{"session of another identity", ab2bus.SendInput{Channel: "dev", Content: "x", Session: other.ID}, ab2bus.CodeNotFound},
			{"unknown session", ab2bus.SendInput{Channel: "dev", Content: "x", Session: "deadbeef"}, ab2bus.CodeNotFound},
			{"lapsed session", ab2bus.SendInput{Channel: "dev", Content: "x", Session: lapsed.ID}, ab2bus.CodeSessionLapsed},
			{"closed session", ab2bus.SendInput{Channel: "dev", Content: "x", Session: closed.ID}, ab2bus.CodeSessionClosed},
		}
		for _, c := range cases {
			_, err := b.Send(ctx, c.in)
			if got := ab2bus.CodeOf(err); got != c.want {
				t.Errorf("%s: code = %q (%v), want %q", c.name, got, err, c.want)
			}
		}
		var after int64
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			s, err := tx.Session(ctx, lapsed.ID)
			after = s.LeaseExpiresAt
			return err
		})
		if after != before {
			t.Errorf("Send renewed the lease: %d -> %d", before, after)
		}
		var max int64
		h.tx(func(ctx context.Context, tx ab2store.Tx) (err error) { max, err = tx.MaxSeq(ctx); return })
		if max != 0 {
			t.Errorf("a refused send left %d messages behind", max)
		}
		// An unregistered caller.
		_, err := newBus(h, "ghost").Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "x"})
		codeOf(t, err, ab2bus.CodeNotRegistered)
	})
}

func TestSendValidationMessagesArePlain(t *testing.T) {
	// A rule's validation error reaches the caller as it is, not re-wrapped as
	// the JSON text of the first error inside the message of a second.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		for _, c := range []struct {
			name string
			in   ab2bus.SendInput
			want string
		}{
			{"bad tag", ab2bus.SendInput{Channel: "dev", Content: "x", Tags: []string{"Bad Tag"}}, `"bad tag"`},
			{"multiline subject", ab2bus.SendInput{Channel: "dev", Content: "x", Subject: "a\nb"}, "subject must be a single line"},
		} {
			_, err := b.Send(context.Background(), c.in)
			var be *ab2bus.Error
			if !errors.As(err, &be) || be.Code != ab2bus.CodeValidation {
				t.Fatalf("%s: err = %v, want a validation error", c.name, err)
			}
			if strings.HasPrefix(be.Message, "{") || strings.Contains(be.Message, `\"code\"`) || !strings.Contains(be.Message, c.want) {
				t.Errorf("%s: message = %q, want plain text containing %q", c.name, be.Message, c.want)
			}
		}
	})
}

func TestSendMemoryKind(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("mem", ab2store.KindMemory, "pat", nil)
		b := h.as("pat")
		res, err := b.Send(ctx, ab2bus.SendInput{Channel: "mem", Content: "remember"})
		if err != nil {
			t.Fatal(err)
		}
		if res.MemoryID == nil || *res.MemoryID != res.Seq {
			t.Fatalf("result = %+v, want memory id = seq", res)
		}
		got, err := b.History(ctx, ab2bus.HistoryInput{Channel: "mem"})
		if err != nil || len(got) != 1 || got[0].MemoryID == nil || got[0].Revision == nil || *got[0].Revision != 1 {
			t.Fatalf("History = %+v, %v; want revision 1", got, err)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			refs, err := tx.ExpiredMemories(ctx, h.core.NowMs()+1, 10)
			if err != nil || len(refs) != 1 || refs[0].MemoryID != res.Seq {
				t.Errorf("ExpiredMemories = %v, %v; want the memory's access row touched", refs, err)
			}
			return nil
		})
	})
}

func TestSendMemoryKicksEmbedding(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		h.channel("mem", ab2store.KindMemory, "pat", nil)
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		kicks := 0
		h.core.EmbedSoon = func() { kicks++ }
		b := h.as("pat")
		ctx := context.Background()
		if _, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "x"}); err != nil {
			t.Fatal(err)
		}
		if kicks != 0 {
			t.Fatalf("an ordinary send kicked the embedder %d times", kicks)
		}
		in := ab2bus.SendInput{Channel: "mem", Content: "x", IdempotencyKey: "k"}
		for range 2 {
			if _, err := b.Send(ctx, in); err != nil {
				t.Fatal(err)
			}
		}
		if kicks != 1 {
			t.Fatalf("kicks = %d, want 1 (a replay does not kick)", kicks)
		}
	})
}

func TestSendIdempotencyReplay(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		in := ab2bus.SendInput{Channel: "dev", Content: "once", IdempotencyKey: "k1", Tags: []string{"b", "a"}}
		r1, err := b.Send(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		in.Tags = []string{"a", "b", "A"} // another form of the same payload
		r2, err := b.Send(ctx, in)
		if err != nil || r2 != r1 {
			t.Fatalf("replay = %+v, %v; want %+v", r2, err, r1)
		}
		got, _ := b.History(ctx, ab2bus.HistoryInput{Channel: "dev"})
		if len(got) != 1 {
			t.Fatalf("%d messages after a replay, want 1", len(got))
		}
		// Keys are per identity.
		k := h.as("kim")
		r3, err := k.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "once", IdempotencyKey: "k1"})
		if err != nil || r3.Seq == r1.Seq {
			t.Fatalf("another identity's same key = %+v, %v; want a new message", r3, err)
		}
	})
}

func TestSendIdempotencyConflict(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		if _, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "one", IdempotencyKey: "k"}); err != nil {
			t.Fatal(err)
		}
		_, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "two", IdempotencyKey: "k"})
		codeOf(t, err, ab2bus.CodeConflict)
	})
}

func TestSendReplayFromRestartedSession(t *testing.T) {
	// Decision 67: a restarted session of the same identity (a new session id)
	// can retry a keyed send safely. The session is attribution of the first
	// send, not payload, so it is not part of the receipt fingerprint.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		first := h.session("pat", "pat/first")
		second := h.session("pat", "pat/second")
		b := h.as("pat")
		in := ab2bus.SendInput{Channel: "dev", Content: "once", IdempotencyKey: "k", Session: first.ID}
		r1, err := b.Send(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		in.Session = second.ID
		r2, err := b.Send(ctx, in)
		if err != nil || r2 != r1 {
			t.Fatalf("retry from a restarted session = %+v, %v; want the replay %+v", r2, err, r1)
		}
		in.Session = ""
		r3, err := b.Send(ctx, in)
		if err != nil || r3 != r1 {
			t.Fatalf("retry with no session = %+v, %v; want the replay %+v", r3, err, r1)
		}
		got, err := b.History(ctx, ab2bus.HistoryInput{Channel: "dev"})
		if err != nil || len(got) != 1 {
			t.Fatalf("History = %v, %v; want the one message", got, err)
		}
		if got[0].Session != first.ID || got[0].Display != "pat/first" {
			t.Errorf("message attributed to %q (%q), want the first send's session %q (pat/first)", got[0].Session, got[0].Display, first.ID)
		}
		// A different payload from the new session is still a conflict.
		in.Session, in.Content = second.ID, "twice"
		_, err = b.Send(ctx, in)
		codeOf(t, err, ab2bus.CodeConflict)
	})
}

func TestSendReplayEmptyMetadata(t *testing.T) {
	// nil and an empty map are the same payload: a retry that spells "no
	// metadata" the other way replays rather than conflicts.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		for _, c := range []struct {
			name         string
			first, retry map[string]string
		}{
			{"nil then empty", nil, map[string]string{}},
			{"empty then nil", map[string]string{}, nil},
		} {
			key := "k-" + c.name
			r1, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: c.name, IdempotencyKey: key, Metadata: c.first})
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			r2, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: c.name, IdempotencyKey: key, Metadata: c.retry})
			if err != nil || r2 != r1 {
				t.Errorf("%s: retry = %+v, %v; want the replay %+v", c.name, r2, err, r1)
			}
		}
		// Real metadata is still payload.
		_, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "nil then empty", IdempotencyKey: "k-nil then empty", Metadata: map[string]string{"k": "v"}})
		codeOf(t, err, ab2bus.CodeConflict)
	})
}

func TestSendReplayAfterRetention(t *testing.T) {
	// Review Focus 7: receipts are checked before any mutable lookup.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		in := ab2bus.SendInput{Channel: "dev", Content: "gone soon", IdempotencyKey: "k"}
		r1, err := b.Send(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteMessages(ctx, []int64{r1.Seq}) })
		r2, err := b.Send(ctx, in)
		if err != nil || r2 != r1 {
			t.Fatalf("replay after retention = %+v, %v; want the stored %+v", r2, err, r1)
		}
	})
}

func TestSendRateLimited(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{cfg: sendCfgWith(func(c *core.Config) { c.SendMessagesPerSecond = 2 })})
		ctx := context.Background()
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		send := func() error {
			_, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "x"})
			return err
		}
		for i := 0; i < 2; i++ {
			if err := send(); err != nil {
				t.Fatalf("send %d: %v", i, err)
			}
		}
		err := send()
		codeOf(t, err, ab2bus.CodeRateLimited)
		var be *ab2bus.Error
		if !errors.As(err, &be) || !be.Retryable {
			t.Errorf("rate_limited is not retryable: %v", err)
		}
		h.clock.Advance(time.Second)
		if err := send(); err != nil {
			t.Fatalf("send after refill: %v", err)
		}
		// Another identity has its own bucket (Decision 86).
		if _, err := h.as("kim").Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "x"}); err != nil {
			t.Fatalf("other identity: %v", err)
		}
	})
}

func TestSendChargesOncePerCommit(t *testing.T) {
	// Review Focus 3: the injected fault re-runs the transaction body on
	// Postgres (the retried run commits once: one message, one receipt, one
	// hint, one charge) and fails the send on SQLite (nothing committed,
	// nothing charged, nothing published).
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{
			cfg:   sendCfgWith(func(c *core.Config) { c.SendMessagesPerSecond = 2 }),
			fault: &pgconnError40001,
		})
		ctx := context.Background()
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		sub, cancel := h.notifier.Subscribe()
		defer cancel()
		sent := 0
		send := func() (key string, res ab2bus.SendResult, err error) {
			sent++
			key = fmt.Sprintf("charge-%d", sent)
			res, err = b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "x", IdempotencyKey: key})
			return
		}
		count := func() (msgs int) {
			h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				max, err := tx.MaxSeq(ctx)
				msgs = int(max)
				return err
			})
			return
		}
		receipt := func(key string) (r ab2store.Receipt, ok bool) {
			h.tx(func(ctx context.Context, tx ab2store.Tx) (err error) {
				r, ok, err = tx.Receipt(ctx, "pat", key, h.core.NowMs())
				return
			})
			return
		}
		// committed asserts what one successful send leaves: exactly one hint
		// {channel, seq}, a receipt whose stored result is that send's, and
		// wantMsgs messages in all.
		committed := func(name, key string, res ab2bus.SendResult, wantMsgs int) {
			t.Helper()
			if got := sendDrainHints(sub); len(got) != 1 || got[0] != (notify.Hint{ChannelID: ch.ID, Seq: res.Seq}) {
				t.Errorf("%s: hints = %v, want one {%d %d}", name, got, ch.ID, res.Seq)
			}
			r, ok := receipt(key)
			var stored ab2bus.SendResult
			if !ok || json.Unmarshal(r.Result, &stored) != nil || stored != res {
				t.Errorf("%s: receipt = %+v (found %v), want the stored result %+v", name, r, ok, res)
			}
			if n := count(); n != wantMsgs {
				t.Errorf("%s: %d messages, want %d", name, n, wantMsgs)
			}
		}
		// refused asserts a send that committed nothing.
		refused := func(name, key string, wantMsgs int) {
			t.Helper()
			if got := sendDrainHints(sub); len(got) != 0 {
				t.Errorf("%s: a failed send published %v", name, got)
			}
			if r, ok := receipt(key); ok {
				t.Errorf("%s: a failed send left the receipt %+v", name, r)
			}
			if n := count(); n != wantMsgs {
				t.Errorf("%s: %d messages, want %d", name, n, wantMsgs)
			}
		}

		want := 0
		if e.Name == "postgres" {
			key, res, err := send()
			if err != nil {
				t.Fatalf("first send: %v", err)
			}
			want++
			committed("faulted then retried send", key, res, want)
		} else {
			key, _, err := send()
			codeOf(t, err, ab2bus.CodeInternal)
			refused("faulted send", key, 0)
		}
		for want < 2 {
			key, res, err := send()
			if err != nil {
				t.Fatalf("send %d: %v (an earlier run was charged)", sent, err)
			}
			want++
			committed(fmt.Sprintf("send %d", sent), key, res, want)
		}
		key, _, err := send()
		codeOf(t, err, ab2bus.CodeRateLimited)
		refused("rate-limited send", key, want)
	})
}

// sendDrainHints returns the hints pending on sub without blocking.
func sendDrainHints(sub <-chan notify.Hint) []notify.Hint {
	var out []notify.Hint
	for {
		select {
		case h := <-sub:
			out = append(out, h)
		default:
			return out
		}
	}
}

func TestSendPublishesHint(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		sub, cancel := h.notifier.Subscribe()
		defer cancel()
		for i := 0; i < 3; i++ {
			res, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "x"})
			if err != nil {
				t.Fatal(err)
			}
			got := sendDrainHints(sub)
			if len(got) != 1 || got[0] != (notify.Hint{ChannelID: ch.ID, Seq: res.Seq}) {
				t.Fatalf("hints after send %d = %v, want one {%d %d}", i, got, ch.ID, res.Seq)
			}
		}
		// A refused send publishes nothing.
		_, _ = b.Send(ctx, ab2bus.SendInput{Channel: "dev"})
		if got := sendDrainHints(sub); len(got) != 0 {
			t.Fatalf("a refused send published %v", got)
		}
	})
}

func TestSendOversized(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{cfg: sendCfgWith(func(c *core.Config) { c.MaxMessageKiB = 1 })})
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		b := h.as("pat")
		_, err := b.Send(context.Background(), ab2bus.SendInput{Channel: "dev", Content: strings.Repeat("x", 1100)})
		codeOf(t, err, ab2bus.CodeValidation)
		if _, err := b.Send(context.Background(), ab2bus.SendInput{Channel: "dev", Content: "small"}); err != nil {
			t.Fatalf("small send: %v", err)
		}
	})
}

func TestSendConcurrentSeqOrder(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{cfg: sendCfgWith(func(c *core.Config) {
			c.SendMessagesPerSecond = 10000
			c.SendKiBPerSecond = 60000
		})})
		ctx := context.Background()
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		h.as("pat")
		const workers, each = 8, 25
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			errs []error
		)
		for w := 0; w < workers; w++ {
			b := newBus(h, "pat")
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < each; i++ {
					_, err := b.Send(ctx, ab2bus.SendInput{
						Channel: "dev", Content: "x", IdempotencyKey: fmt.Sprintf("w%d-%d", w, i),
					})
					if err != nil {
						mu.Lock()
						errs = append(errs, err)
						mu.Unlock()
					}
				}
			}()
		}
		wg.Wait()
		if len(errs) > 0 {
			t.Fatalf("%d sends failed, first: %v", len(errs), errs[0])
		}
		b := newBus(h, "pat")
		var all []ab2bus.Message
		var after int64
		for {
			page, err := b.History(ctx, ab2bus.HistoryInput{Channel: "dev", After: &after, Count: 50})
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			all = append(all, page...)
			after = page[len(page)-1].Seq
		}
		if len(all) != workers*each {
			t.Fatalf("%d messages, want %d", len(all), workers*each)
		}
		for i := 1; i < len(all); i++ {
			if all[i].Seq != all[i-1].Seq+1 {
				t.Fatalf("seq gap or disorder: %d after %d", all[i].Seq, all[i-1].Seq)
			}
		}
		var max int64
		h.tx(func(ctx context.Context, tx ab2store.Tx) (err error) { max, err = tx.MaxSeq(ctx); return })
		if first := all[0].Seq; first < 1 || max != all[len(all)-1].Seq || max-first+1 != int64(len(all)) {
			t.Fatalf("first seq %d, last %d, MaxSeq %d: not a contiguous run", all[0].Seq, all[len(all)-1].Seq, max)
		}
		_ = ch
	})
}
