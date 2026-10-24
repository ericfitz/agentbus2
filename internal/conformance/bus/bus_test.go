package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/local"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func TestScaffold(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		db := e.Open(t)
		var one int
		if err := db.QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("%s: SELECT 1 = %d, %v", e.Name, one, err)
		}
	})
}

var _ ab2bus.Bus = (*local.Bus)(nil)

func TestCodeOfHelper(t *testing.T) {
	codeOf(t, core.E(ab2bus.CodeNotFound, "x"), ab2bus.CodeNotFound)
	codeOf(t, errors.New("plain"), "")
}

func TestLocalBusImplementsEveryInterface(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		b := h.admin("root")
		must := func(name string, err error) {
			if err != nil {
				t.Errorf("%s: %v, want nil", name, err)
			}
		}
		_, err := b.ListChannels(ctx)
		must("Messaging.ListChannels", err)
		_, err = b.Identities(ctx)
		must("Sessions.Identities", err)
		_, err = b.Status(ctx)
		must("Admin.Status", err)
		_, err = b.GetMemory(ctx, 1)
		codeOf(t, err, ab2bus.CodeNotFound)
		_, err = b.GetTask(ctx, 1)
		codeOf(t, err, ab2bus.CodeNotFound)
	})
}

func TestHarnessFixtures(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{embedder: true})
		ctx := context.Background()

		// session: a live row that lapses with the clock.
		s := h.session("pat", "pat")
		if s.DisplayName != "pat" || s.ID == "" {
			t.Fatalf("session = %+v", s)
		}
		var stored ab2store.Session
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			var err error
			stored, err = tx.Session(ctx, s.ID)
			return err
		})
		if !h.core.IsLive(stored, h.core.NowMs()) {
			t.Fatal("a fresh h.session row is not live")
		}
		h.clock.Advance(time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds+1) * time.Second)
		if h.core.IsLive(stored, h.core.NowMs()) {
			t.Fatal("the session is still live after SessionLeaseSeconds + LeaseSkewSeconds + 1")
		}

		// append on a memory-kind channel leaves a live memory.
		ch := h.channel("mem", ab2store.KindMemory, "pat", nil)
		seq := h.append(ch, "pat", "", "alpha")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			m, err := tx.LiveMemory(ctx, seq)
			if err != nil || m.Seq != seq {
				t.Errorf("LiveMemory(%d) = %+v, %v", seq, m, err)
			}
			if m.CreatedAt != h.clock.Now().UnixMilli() {
				t.Errorf("CreatedAt = %d, want the harness clock %d", m.CreatedAt, h.clock.Now().UnixMilli())
			}
			return nil
		})

		// the fake embedder: a rejected text fails the whole call as a RowRejected.
		h.embedder.Reject = map[string]string{"bad": "too long"}
		_, err := h.embedder.Embed(ctx, []string{"ok", "bad"})
		var rr *core.RowRejected
		if !core.IsRowRejected(err) || !errors.As(err, &rr) || rr.Index != 1 || rr.Reason != "too long" {
			t.Fatalf("Embed with a rejected text = %v, want *core.RowRejected at index 1", err)
		}
		h.embedder.Reject = nil
		vecs, err := h.embedder.Embed(ctx, []string{"Alpha alpha zulu", "bravo"})
		if err != nil || len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][1] != 1 {
			t.Fatalf("Embed = %v, %v; want unit histogram vectors", vecs, err)
		}
		h.embedder.Fail = true
		if _, err := h.embedder.Embed(ctx, []string{"alpha"}); err == nil || core.IsRowRejected(err) {
			t.Fatalf("Fail: err = %v, want a plain endpoint error", err)
		}
	})
}

func TestHarnessSiblingSharesDatabase(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		sib := h.sibling(harnessOptions{})
		if sib.core == h.core || sib.core.Owner == h.core.Owner || sib.core.Limits == h.core.Limits || sib.clock == h.clock {
			t.Fatal("a sibling must have its own Core, owner, limiter and clock")
		}
		ch := h.channel("shared", ab2store.KindOrdinary, "pat", nil)
		seq := sib.append(ch, "kim", "", "from the sibling")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			m, err := tx.Message(ctx, seq)
			if err != nil || m.Content != "from the sibling" {
				t.Errorf("Message(%d) = %+v, %v; the first core does not see the sibling's write", seq, m, err)
			}
			return nil
		})
	})
}

func TestHarnessFaultStore(t *testing.T) {
	// Review Focus 3: the fault fires inside fn on the first transaction only.
	// SQLite does not classify 40001 as retryable (the error surfaces and
	// nothing committed); Postgres retries and commits once.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{fault: &pgconnError40001})
		ch := h.channel("c", ab2store.KindOrdinary, "pat", nil)
		runs := 0
		err := h.store.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			runs++
			_, _, err := h.core.Append(ctx, tx, core.AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: ab2store.IdentityAgent, Content: "x"})
			return err
		})
		var max int64
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			var err error
			max, err = tx.MaxSeq(ctx)
			return err
		})
		if e.Name == "postgres" {
			if err != nil || runs != 2 || max != 1 {
				t.Fatalf("postgres: err=%v runs=%d maxseq=%d; want a retry that commits once", err, runs, max)
			}
			return
		}
		if err == nil || runs != 1 || max != 0 {
			t.Fatalf("sqlite: err=%v runs=%d maxseq=%d; want the fault to surface with nothing committed", err, runs, max)
		}
	})
}
