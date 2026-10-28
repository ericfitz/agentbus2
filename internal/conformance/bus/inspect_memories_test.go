package bus

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/local"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// inspectMemory stores a memory with subject "s", content "c", tags [a] and
// metadata {k: v} through the raw Store (no inspection) and returns its id.
func inspectMemory(h *harness, ch ab2store.Channel) int64 {
	h.t.Helper()
	var seq int64
	err := h.raw.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		seq, _, err = h.core.Append(ctx, tx, core.AppendInput{
			ChannelID: ch.ID, Channel: ch.Name, Kind: ch.Kind, Sender: "owner", SenderKind: ab2store.IdentityAgent,
			Subject: "s", Content: "c", Tags: []string{"a"}, Metadata: map[string]string{"k": "v"},
		})
		return err
	})
	if err != nil {
		h.t.Fatalf("memory append: %v", err)
	}
	return seq
}

func TestInspectEditSeesMergedRecord(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		id := inspectMemory(h, ch)

		res, err := h.as("owner").Edit(context.Background(), ab2bus.EditMemoryInput{ID: id, Content: memStrp("new")})
		if err != nil {
			t.Fatalf("Edit: %v", err)
		}
		if res.Revision != 2 {
			t.Errorf("result = %+v, want revision 2", res)
		}
		calls := fi.calls()
		if len(calls) != 1 {
			t.Fatalf("%d inspector calls, want 1", len(calls))
		}
		got := calls[0]
		got.RequestID = ""
		want := core.InspectRequest{
			Version: 1, Operation: "edit_memory", Source: "api", Sender: "owner", SenderKind: "agent",
			Channel: core.InspectChannel{Name: "mem", Kind: "memory"},
			Record: core.InspectRecord{
				Subject: "s", Content: "new", Tags: []string{"a"}, Metadata: map[string]string{"k": "v"}, BaseRevision: 1,
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("request = %+v, want %+v", got, want)
		}
	})
}

func TestInspectEditRefuse(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		fi := &fakeInspector{reply: func(context.Context, core.InspectRequest) ([]byte, error) {
			return refuseJSON("policy", "no"), nil
		}}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		id := inspectMemory(h, ch)
		b := h.as("owner")

		_, err := b.Edit(context.Background(), ab2bus.EditMemoryInput{ID: id, Content: memStrp("new"), IdempotencyKey: "k1"})
		codeOf(t, err, ab2bus.CodeInspectionRejected)
		m, err := b.GetMemory(context.Background(), id)
		if err != nil || m.Content != "c" || m.Revision == nil || *m.Revision != 1 {
			t.Fatalf("memory after refusal = %+v, %v; want revision 1, content c", m, err)
		}
		if inspectHasReceipt(h, "owner", "k1") {
			t.Fatal("a refused keyed edit left a receipt")
		}
	})
}

func TestInspectEditConcurrent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()

		// The first reply edits the memory through a sibling core, then allows.
		var n atomic.Int32
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		sib := local.New(h.sibling(harnessOptions{}).core, "owner")
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		id := inspectMemory(h, ch)
		fi.reply = func(ctx context.Context, _ core.InspectRequest) ([]byte, error) {
			if n.Add(1) == 1 {
				if _, err := sib.Edit(ctx, ab2bus.EditMemoryInput{ID: id, Content: memStrp("sibling")}); err != nil {
					return nil, err
				}
			}
			return []byte(allowJSON), nil
		}
		res, err := h.as("owner").Edit(ctx, ab2bus.EditMemoryInput{ID: id, Subject: memStrp("t")})
		if err != nil {
			t.Fatalf("Edit: %v", err)
		}
		if res.Revision != 3 {
			t.Errorf("revision = %d, want 3", res.Revision)
		}
		calls := fi.calls()
		if len(calls) != 2 || calls[0].Record.BaseRevision != 1 || calls[1].Record.BaseRevision != 2 {
			t.Fatalf("calls = %+v, want two with base revisions 1 and 2", calls)
		}
		if calls[1].Record.Content != "sibling" {
			t.Errorf("second call content = %q, want the sibling's", calls[1].Record.Content)
		}

		// A reply that edits on every call never settles: a retryable conflict.
		fi2 := &fakeInspector{}
		h2 := newHarness(t, e, harnessOptions{inspector: fi2})
		sib2 := local.New(h2.sibling(harnessOptions{}).core, "owner")
		ch2 := h2.channel("mem", ab2store.KindMemory, "owner", memoryACL(h2))
		id2 := inspectMemory(h2, ch2)
		fi2.reply = func(ctx context.Context, _ core.InspectRequest) ([]byte, error) {
			if _, err := sib2.Edit(ctx, ab2bus.EditMemoryInput{ID: id2, Content: memStrp("again")}); err != nil {
				return nil, err
			}
			return []byte(allowJSON), nil
		}
		_, err = h2.as("owner").Edit(ctx, ab2bus.EditMemoryInput{ID: id2, Subject: memStrp("t")})
		codeOf(t, err, ab2bus.CodeConflict)
		var be *ab2bus.Error
		if !errors.As(err, &be) || !be.Retryable {
			t.Errorf("conflict %v is not retryable", err)
		}
		if c := len(fi2.calls()); c != 2 {
			t.Errorf("%d inspector calls, want 2", c)
		}
		revs, err := h2.as("owner").Revisions(ctx, id2)
		if err != nil {
			t.Fatal(err)
		}
		if live := revs[len(revs)-1]; live.Revision == nil || *live.Revision != 3 {
			t.Errorf("live revision = %+v, want 3 (the two sibling edits)", live.Revision)
		}
	})
}

func TestInspectEditRefusalsBeforeInspection(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		tasks := h.channel("work", ab2store.KindTasks, "owner", memoryACL(h))
		id := inspectMemory(h, ch)
		taskSeq := h.append(tasks, "owner", "", "a task")

		_, err := h.as("owner").Edit(ctx, ab2bus.EditMemoryInput{ID: id, Content: memStrp("x"), IfRevision: 5})
		codeOf(t, err, ab2bus.CodeConflict)
		_, err = h.as("owner").Edit(ctx, ab2bus.EditMemoryInput{ID: id, Content: memStrp("")})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = h.as("reader").Edit(ctx, ab2bus.EditMemoryInput{ID: id, Content: memStrp("x")})
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = h.as("owner").Edit(ctx, ab2bus.EditMemoryInput{ID: taskSeq, Content: memStrp("x")})
		codeOf(t, err, ab2bus.CodeValidation)
		if c := len(fi.calls()); c != 0 {
			t.Errorf("%d inspector calls, want 0", c)
		}
	})
}

func TestInspectDeleteNotInspected(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		id := inspectMemory(h, ch)
		if err := h.as("owner").Delete(context.Background(), ab2bus.DeleteMemoryInput{ID: id}); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if c := len(fi.calls()); c != 0 {
			t.Errorf("%d inspector calls, want 0", c)
		}
	})
}

func TestInspectEditKeyed(t *testing.T) {
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
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		id := inspectMemory(h, ch)
		b := h.as("owner")
		in := ab2bus.EditMemoryInput{ID: id, Content: memStrp("keyed"), IdempotencyKey: "k1"}

		_, err := b.Edit(ctx, in)
		codeOf(t, err, ab2bus.CodeInspectionRejected)
		_, err = b.Edit(ctx, in)
		codeOf(t, err, ab2bus.CodeInspectionRejected)
		if c := len(fi.calls()); c != 2 {
			t.Fatalf("%d inspector calls after two refusals, want 2 (a retry is inspected again)", c)
		}
		if inspectHasReceipt(h, "owner", "k1") {
			t.Fatal("a refused keyed edit left a receipt")
		}

		allow.Store(true)
		res, err := b.Edit(ctx, in)
		if err != nil || res.Revision != 2 {
			t.Fatalf("allowed edit = %+v, %v", res, err)
		}
		if c := len(fi.calls()); c != 3 {
			t.Fatalf("%d inspector calls, want 3", c)
		}
		again, err := b.Edit(ctx, in)
		if err != nil || again != res {
			t.Fatalf("replay = %+v, %v; want %+v", again, err, res)
		}
		if c := len(fi.calls()); c != 3 {
			t.Errorf("%d inspector calls after the replay, want 3", c)
		}
	})
}

func TestInspectEditChargesRefusal(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		fi := &fakeInspector{reply: func(context.Context, core.InspectRequest) ([]byte, error) {
			return refuseJSON("policy", "no"), nil
		}}
		h := newHarness(t, e, harnessOptions{
			cfg:       sendCfgWith(func(c *core.Config) { c.SendMessagesPerSecond = 1 }),
			inspector: fi,
		})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		id := inspectMemory(h, ch)
		b := h.as("owner")
		_, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: id, Content: memStrp("one")})
		codeOf(t, err, ab2bus.CodeInspectionRejected)
		_, err = b.Edit(ctx, ab2bus.EditMemoryInput{ID: id, Content: memStrp("two")})
		codeOf(t, err, ab2bus.CodeRateLimited)
		if c := len(fi.calls()); c != 1 {
			t.Errorf("%d inspector calls, want 1 (the refusal was charged once)", c)
		}
	})
}

func TestInspectEditMergesOtherFields(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		id := inspectMemory(h, ch)
		b := h.as("owner")
		meta := map[string]string{"x": "y"}
		edits := []ab2bus.EditMemoryInput{
			{ID: id, Subject: memStrp("s2")},
			{ID: id, Tags: []string{"b"}},
			{ID: id, Metadata: &meta},
		}
		want := []core.InspectRecord{
			{Subject: "s2", Content: "c", Tags: []string{"a"}, Metadata: map[string]string{"k": "v"}, BaseRevision: 1},
			{Subject: "s2", Content: "c", Tags: []string{"b"}, Metadata: map[string]string{"k": "v"}, BaseRevision: 2},
			{Subject: "s2", Content: "c", Tags: []string{"b"}, Metadata: meta, BaseRevision: 3},
		}
		for i, in := range edits {
			if _, err := b.Edit(ctx, in); err != nil {
				t.Fatalf("edit %d: %v", i, err)
			}
		}
		calls := fi.calls()
		if len(calls) != 3 {
			t.Fatalf("%d inspector calls, want 3", len(calls))
		}
		for i, c := range calls {
			if !reflect.DeepEqual(c.Record, want[i]) {
				t.Errorf("call %d record = %+v, want %+v", i, c.Record, want[i])
			}
		}
	})
}
