package bus

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/embed"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// embeddedSeqs lists the seqs holding an embedding under model.
func (h *harness) embeddedSeqs(model string) []int64 {
	h.t.Helper()
	var out []int64
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		chs, err := tx.ListChannels(ctx)
		if err != nil {
			return err
		}
		var ids []int64
		for _, c := range chs {
			ids = append(ids, c.ID)
		}
		es, err := tx.Embeddings(ctx, model, ids, ab2store.SearchFilter{})
		for _, e := range es {
			out = append(out, e.Seq)
		}
		return err
	})
	slices.Sort(out)
	return out
}

func (h *harness) embedNotice() (ab2store.Notice, bool) {
	h.t.Helper()
	var n ab2store.Notice
	var ok bool
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		n, ok, err = tx.Notice(ctx, "embedding")
		return err
	})
	return n, ok
}

func TestEmbedBatchEmbedsLiveMemoriesOnly(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{embedder: true})
		ord := h.channel("ord", ab2store.KindOrdinary, "pat", nil)
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		tasks := h.channel("tasks", ab2store.KindTasks, "pat", nil)
		h.append(ord, "pat", "", "alpha ordinary")   // 1: not a memory
		old := h.append(mem, "pat", "", "alpha old") // 2: edited below
		gone := h.append(mem, "pat", "", "bravo")    // 3: tombstoned below
		h.append(tasks, "pat", "", "alpha task")     // 4: tasks kind
		keep := h.append(mem, "pat", "", "charlie")  // 5
		// Edit memory 2: revision 2 is live, revision 1 tombstoned.
		var rev2 int64
		err := h.raw.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			seq, err := tx.AppendMessage(ctx, ab2store.NewMessage{ChannelID: mem.ID, Sender: "pat", CreatedAt: h.core.NowMs(), Content: "alpha new"})
			if err != nil {
				return err
			}
			rev2 = seq
			if err := tx.SetMemory(ctx, seq, old, 2); err != nil {
				return err
			}
			_, err = h.core.TombstoneMemory(ctx, tx, old, old, h.core.NowMs())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			_, err := h.core.TombstoneMemory(ctx, tx, gone, gone, h.core.NowMs())
			return err
		})
		n, err := embed.EmbedBatch(context.Background(), h.core)
		if err != nil || n != 2 {
			t.Fatalf("EmbedBatch = %d, %v; want 2", n, err)
		}
		if got := h.embeddedSeqs("fake"); !reflect.DeepEqual(got, []int64{keep, rev2}) {
			t.Fatalf("embedded = %v, want [%d %d]", got, keep, rev2)
		}
		// State (Definition of Done D): a second run finds nothing to do.
		if n, err := embed.EmbedBatch(context.Background(), h.core); err != nil || n != 0 {
			t.Fatalf("second EmbedBatch = %d, %v; want 0", n, err)
		}
	})
}

func TestEmbedBatchRecordsRowFailure(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{embedder: true})
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		h.embedder.Reject = map[string]string{"bad": "input too long"}
		one := h.append(mem, "pat", "", "ok one")
		h.append(mem, "pat", "", "bad")
		two := h.append(mem, "pat", "", "ok two")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.SetNotice(ctx, ab2store.Notice{Kind: "embedding", Message: "stale complaint", SetAt: 1})
		})
		if _, err := h.embedder.Embed(context.Background(), []string{"ok one", "bad"}); !core.IsRowRejected(err) {
			t.Fatalf("whole-batch call = %v, want RowRejected", err)
		}
		n, err := embed.EmbedBatch(context.Background(), h.core)
		if err != nil || n != 3 {
			t.Fatalf("EmbedBatch = %d, %v; want 3 (two embedded, one failure)", n, err)
		}
		if got := h.embeddedSeqs("fake"); !reflect.DeepEqual(got, []int64{one, two}) {
			t.Fatalf("embedded = %v, want [%d %d]", got, one, two)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			count, last, err := tx.EmbedFailures(ctx, "fake")
			if err != nil || count != 1 || last.Error != "input too long" {
				t.Errorf("EmbedFailures = %d, %+v, %v", count, last, err)
			}
			return nil
		})
		if _, ok := h.embedNotice(); ok {
			t.Fatal("the embedding notice is still set after a finished pass")
		}
		if n, err := embed.EmbedBatch(context.Background(), h.core); err != nil || n != 0 {
			t.Fatalf("second EmbedBatch = %d, %v; a recorded failure is not retried", n, err)
		}
	})
}

func TestEmbedBatchRejectingEverythingIsConfiguration(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{embedder: true})
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		h.embedder.Reject = map[string]string{"bad one": "no", "bad two": "no"}
		h.append(mem, "pat", "", "bad one")
		h.append(mem, "pat", "", "bad two")
		_, err := embed.EmbedBatch(context.Background(), h.core)
		if err == nil || !strings.Contains(err.Error(), "rejected every input") {
			t.Fatalf("EmbedBatch err = %v, want the configuration error", err)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if count, _, _ := tx.EmbedFailures(ctx, "fake"); count != 0 {
				t.Errorf("%d failures recorded though the endpoint never accepted anything", count)
			}
			return nil
		})
		if _, ok := h.embedNotice(); !ok {
			t.Fatal("no notice for the configuration error")
		}
	})
}

func TestEmbedBatchNotesEndpointError(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{embedder: true})
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		seq := h.append(mem, "pat", "", "alpha")
		h.embedder.Fail = true
		if _, err := embed.EmbedBatch(context.Background(), h.core); err == nil {
			t.Fatal("EmbedBatch succeeded against a failing endpoint")
		}
		n, ok := h.embedNotice()
		if !ok || !strings.Contains(n.Message, "endpoint failure") || n.SetAt != h.core.NowMs() {
			t.Fatalf("notice = %+v, %v; want it set with the error", n, ok)
		}
		if got := h.embeddedSeqs("fake"); len(got) != 0 {
			t.Fatalf("embedded = %v after a failed pass", got)
		}
		h.embedder.Fail = false
		if n, err := embed.EmbedBatch(context.Background(), h.core); err != nil || n != 1 {
			t.Fatalf("recovery EmbedBatch = %d, %v", n, err)
		}
		if _, ok := h.embedNotice(); ok {
			t.Fatal("notice not cleared by the next success")
		}
		if got := h.embeddedSeqs("fake"); !reflect.DeepEqual(got, []int64{seq}) {
			t.Fatalf("embedded = %v", got)
		}
	})
}

func TestEmbedBatchStaleModel(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{embedder: true})
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		seq := h.append(mem, "pat", "", "alpha")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutEmbedding(ctx, ab2store.Embedding{Seq: seq, Model: "old", Vector: []byte{0, 0, 0, 0}})
		})
		if got := h.embeddedSeqs("old"); !reflect.DeepEqual(got, []int64{seq}) {
			t.Fatalf("setup: old embedding = %v", got)
		}
		if n, err := embed.EmbedBatch(context.Background(), h.core); err != nil || n != 1 {
			t.Fatalf("EmbedBatch = %d, %v", n, err)
		}
		if got := h.embeddedSeqs("old"); len(got) != 0 {
			t.Fatalf("stale-model embedding survived: %v", got)
		}
		if got := h.embeddedSeqs("fake"); !reflect.DeepEqual(got, []int64{seq}) {
			t.Fatalf("embedded = %v", got)
		}
	})
}

func TestEmbedBatchWithoutEmbedderDoesNothing(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		h.append(mem, "pat", "", "alpha")
		if n, err := embed.EmbedBatch(context.Background(), h.core); err != nil || n != 0 {
			t.Fatalf("EmbedBatch = %d, %v; want 0, nil", n, err)
		}
	})
}
