package bus

import (
	"context"
	"slices"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func p64(v int64) *int64 { return &v }

// sendTo sends content on channel as b, replying to replyTo (nil: none), and
// returns the seq.
func sendTo(t *testing.T, b ab2bus.Bus, channel, content string, replyTo *int64) int64 {
	t.Helper()
	res, err := b.Send(context.Background(), ab2bus.SendInput{Channel: channel, Content: content, ReplyTo: replyTo})
	if err != nil {
		t.Fatalf("Send %s %q: %v", channel, content, err)
	}
	return res.Seq
}

func replyOf(t *testing.T, b ab2bus.Bus, channel string, seq int64) *int64 {
	t.Helper()
	ms, err := b.History(context.Background(), ab2bus.HistoryInput{Channel: channel, After: p64(seq - 1), Before: p64(seq + 1)})
	if err != nil || len(ms) != 1 || ms[0].Seq != seq {
		t.Fatalf("History %s seq %d = %v, %v", channel, seq, ms, err)
	}
	return ms[0].ReplyTo
}

func TestReplyToRules(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		allow := []ab2store.ACLEntry{{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}}}
		h.channel("dev", ab2store.KindOrdinary, "pat", allow)
		h.channel("ops", ab2store.KindOrdinary, "pat", allow)
		h.channel("notes", ab2store.KindMemory, "pat", allow)
		pat := h.as("pat")
		a := sendTo(t, pat, "dev", "a", nil)
		b := sendTo(t, pat, "ops", "b", nil)
		mem := sendTo(t, pat, "notes", "m", nil)

		for name, parent := range map[string]int64{"ordinary other channel": b, "memory other channel": mem} {
			_, err := pat.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "x", ReplyTo: p64(parent)})
			codeOf(t, err, ab2bus.CodeValidation)
			if !strings.Contains(err.Error(), "another channel") {
				t.Errorf("%s: error %q lacks \"another channel\"", name, err)
			}
		}
		_, err := pat.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "x", ReplyTo: p64(9999)})
		codeOf(t, err, ab2bus.CodeNotFound)

		r := sendTo(t, pat, "dev", "r", p64(a))
		if got := replyOf(t, pat, "dev", r); got == nil || *got != a {
			t.Errorf("reply_to = %v, want %d", got, a)
		}
		// Memory channel: replying inside the channel is the allowed shape.
		mr := sendTo(t, pat, "notes", "m2", p64(mem))
		if got := replyOf(t, pat, "notes", mr); got == nil || *got != mem {
			t.Errorf("memory reply_to = %v, want %d", got, mem)
		}
	})
}

func TestReplyToRewriteOnBridgedChannel(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		allow := []ab2store.ACLEntry{{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}}}
		ch := h.channel("job1-chat", ab2store.KindOrdinary, "pat", allow)
		pat := h.as("pat")
		m0 := sendTo(t, pat, "job1-chat", "root", nil)
		r1 := sendTo(t, pat, "job1-chat", "r1", p64(m0))
		r2 := sendTo(t, pat, "job1-chat", "r2", p64(r1))
		if got := replyOf(t, pat, "job1-chat", r2); got == nil || *got != r1 {
			t.Fatalf("no bridge: reply_to = %v, want %d", got, r1)
		}
		var bridgeID int64
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			b, err := tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: ch.ID, ChannelName: "job1-chat", Scope: "job1", SlackName: "ab-job1-chat", CreatedAt: 5})
			if err != nil {
				return err
			}
			bridgeID = b.ID
			return nil
		})
		// A creating bridge does not rewrite.
		c0 := sendTo(t, pat, "job1-chat", "creating", p64(r2))
		if got := replyOf(t, pat, "job1-chat", c0); got == nil || *got != r2 {
			t.Errorf("creating bridge: reply_to = %v, want %d", got, r2)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.SetBridgeSlackChannel(ctx, bridgeID, "G1"); err != nil {
				return err
			}
			return tx.SetBridgeLive(ctx, bridgeID)
		})
		r3 := sendTo(t, pat, "job1-chat", "r3", p64(r2))
		if got := replyOf(t, pat, "job1-chat", r3); got == nil || *got != m0 {
			t.Errorf("live bridge, reply to r2: reply_to = %v, want root %d", got, m0)
		}
		r4 := sendTo(t, pat, "job1-chat", "r4", p64(m0))
		if got := replyOf(t, pat, "job1-chat", r4); got == nil || *got != m0 {
			t.Errorf("live bridge, reply to root: reply_to = %v, want %d", got, m0)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeArchived(ctx, bridgeID, 9) })
		r5 := sendTo(t, pat, "job1-chat", "r5", p64(r2))
		if got := replyOf(t, pat, "job1-chat", r5); got == nil || *got != r2 {
			t.Errorf("archived bridge: reply_to = %v, want %d", got, r2)
		}
	})
}

func TestHistoryThread(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		allow := []ab2store.ACLEntry{{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}}}
		dev := h.channel("dev", ab2store.KindOrdinary, "pat", allow)
		pat := h.as("pat")
		tr := sendTo(t, pat, "dev", "root", nil)
		r1 := sendTo(t, pat, "dev", "r1", p64(tr))
		other := sendTo(t, pat, "dev", "other", nil)
		r2 := sendTo(t, pat, "dev", "r2", p64(tr))
		for i := range 150 {
			h.append(dev, "pat", "", "later"+string(rune('a'+i%26))) // direct: Send is rate limited
		}
		want := []int64{tr, r1, r2}
		got, err := pat.History(ctx, ab2bus.HistoryInput{Channel: "dev", Thread: &tr})
		if err != nil || !slices.Equal(historySeqs(got), want) {
			t.Errorf("Thread = %v, %v; want %v", historySeqs(got), err, want)
		}
		got, err = pat.History(ctx, ab2bus.HistoryInput{Channel: "dev", Thread: &other})
		if err != nil || !slices.Equal(historySeqs(got), []int64{other}) {
			t.Errorf("Thread other = %v, %v", historySeqs(got), err)
		}
		got, err = pat.History(ctx, ab2bus.HistoryInput{Channel: "dev", Thread: &tr, After: p64(tr)})
		if err != nil || !slices.Equal(historySeqs(got), []int64{r1, r2}) {
			t.Errorf("Thread After = %v, %v", historySeqs(got), err)
		}
		got, err = pat.History(ctx, ab2bus.HistoryInput{Channel: "dev", Thread: &tr, Before: p64(r2)})
		if err != nil || !slices.Equal(historySeqs(got), []int64{tr, r1}) {
			t.Errorf("Thread Before = %v, %v", historySeqs(got), err)
		}
		for _, bad := range []int64{0, -3} {
			_, err = pat.History(ctx, ab2bus.HistoryInput{Channel: "dev", Thread: p64(bad)})
			codeOf(t, err, ab2bus.CodeValidation)
		}
	})
}
