package bus

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// memoryACL gives owner everything, reader read only and writer read and
// write; nobody else holds a row.
func memoryACL(h *harness) []ab2store.ACLEntry {
	h.ensureIdentity("owner")
	h.ensureIdentity("reader")
	h.ensureIdentity("writer")
	return []ab2store.ACLEntry{
		{Identity: "owner", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}},
		{Identity: "reader", Permissions: []ab2store.Permission{ab2store.PermRead}},
		{Identity: "writer", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}},
	}
}

func memStrp(s string) *string { return &s }

// memExpiredIDs lists the live memories whose access is older than before.
func memExpiredIDs(t *testing.T, h *harness, before int64) []int64 {
	t.Helper()
	var ids []int64
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		refs, err := tx.ExpiredMemories(ctx, before, 100)
		for _, r := range refs {
			ids = append(ids, r.MemoryID)
		}
		return err
	})
	return ids
}

func TestMemoryGetNeedsReadAndTouches(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		seq := h.append(ch, "owner", "", "hello")
		h.as("nobody")

		_, err := newBus(h, "nobody").GetMemory(ctx, seq)
		codeOf(t, err, ab2bus.CodeForbidden)

		h.clock.Advance(time.Hour)
		now := h.core.NowMs()
		if got := memExpiredIDs(t, h, now); len(got) != 1 || got[0] != seq {
			t.Fatalf("before Get, expired = %v, want [%d]", got, seq)
		}
		// The refused Get touched nothing; the permitted one does.
		m, err := h.as("reader").GetMemory(ctx, seq)
		if err != nil {
			t.Fatalf("GetMemory: %v", err)
		}
		if m.Seq != seq || m.Content != "hello" || m.Channel != "mem" || m.Revision == nil || *m.Revision != 1 {
			t.Fatalf("GetMemory = %+v", m)
		}
		if got := memExpiredIDs(t, h, now); len(got) != 0 {
			t.Fatalf("after Get, expired = %v, want none", got)
		}
		_, err = h.as("reader").GetMemory(ctx, seq+100)
		codeOf(t, err, ab2bus.CodeNotFound)
	})
}

func TestMemoryEditNeedsWrite(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		seq := h.append(ch, "owner", "", "v1")
		in := ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v2")}

		_, err := h.as("reader").Edit(ctx, in)
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = h.as("nobody").Edit(ctx, in)
		codeOf(t, err, ab2bus.CodeForbidden)
		res, err := h.as("writer").Edit(ctx, in)
		if err != nil {
			t.Fatalf("Edit: %v", err)
		}
		if res.Revision != 2 {
			t.Fatalf("result = %+v", res)
		}
		// Delete needs write too.
		codeOf(t, h.as("reader").Delete(ctx, ab2bus.DeleteMemoryInput{ID: seq}), ab2bus.CodeForbidden)
		// An unregistered caller is refused before any lookup.
		_, err = newBus(h, "ghost").Edit(ctx, in)
		codeOf(t, err, ab2bus.CodeNotRegistered)
	})
}

func TestMemoryEditKeepsSubjectAndTags(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		seq := h.append(ch, "owner", "", "v1", "red", "blue")
		b := h.as("owner")

		r, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Subject: memStrp("  Title  "), Content: memStrp("v2")})
		if err != nil {
			t.Fatal(err)
		}
		m, err := b.GetMemory(ctx, seq)
		if err != nil {
			t.Fatal(err)
		}
		if m.Seq != r.Seq || m.Subject != "Title" || !reflect.DeepEqual(m.Tags, []string{"blue", "red"}) {
			t.Fatalf("after edit 1: %+v", m)
		}
		// Omitted subject and nil tags are carried.
		if _, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v3")}); err != nil {
			t.Fatal(err)
		}
		m, _ = b.GetMemory(ctx, seq)
		if m.Content != "v3" || m.Subject != "Title" || !reflect.DeepEqual(m.Tags, []string{"blue", "red"}) {
			t.Fatalf("after edit 2: %+v", m)
		}
		// [] clears tags; the subject is still kept.
		if _, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v4"), Tags: []string{}}); err != nil {
			t.Fatal(err)
		}
		m, _ = b.GetMemory(ctx, seq)
		if len(m.Tags) != 0 || m.Subject != "Title" {
			t.Fatalf("after edit 3: %+v", m)
		}
		// New tags are normalized; a pointer to "" clears the subject.
		if _, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v5"), Subject: memStrp(""), Tags: []string{"Green"}}); err != nil {
			t.Fatal(err)
		}
		m, _ = b.GetMemory(ctx, seq)
		if m.Subject != "" || !reflect.DeepEqual(m.Tags, []string{"green"}) {
			t.Fatalf("after edit 4: %+v", m)
		}
	})
}

func TestMemoryEditRevisionChain(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		seq := h.append(ch, "owner", "", "v1")
		b := h.as("owner")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutEmbedding(ctx, ab2store.Embedding{Seq: seq, Model: "fake", Vector: []byte{1, 2, 3, 4}})
		})
		embeds := func() int {
			var n int
			h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				es, err := tx.Embeddings(ctx, "fake", []int64{ch.ID}, ab2store.SearchFilter{})
				n = len(es)
				return err
			})
			return n
		}
		if embeds() != 1 {
			t.Fatal("fixture embedding missing")
		}

		res, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v2")})
		if err != nil {
			t.Fatal(err)
		}
		if res.MemoryID != seq || res.Revision != 2 || res.Replaced != seq || res.Seq <= seq {
			t.Fatalf("result = %+v", res)
		}
		if embeds() != 0 {
			t.Fatal("the old revision's embedding survived the edit")
		}
		revs, err := b.Revisions(ctx, seq)
		if err != nil {
			t.Fatal(err)
		}
		if len(revs) != 2 || revs[0].Seq != seq || revs[1].Seq != res.Seq ||
			*revs[0].Revision != 1 || *revs[1].Revision != 2 || *revs[1].MemoryID != seq ||
			revs[0].Content != "v1" || revs[1].Content != "v2" {
			t.Fatalf("revisions = %+v", revs)
		}
		// Edits chain by memory id: a second edit replaces the revision-2 row.
		res2, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v3")})
		if err != nil {
			t.Fatal(err)
		}
		if res2.Revision != 3 || res2.Replaced != res.Seq {
			t.Fatalf("second result = %+v", res2)
		}
		_, err = b.Revisions(ctx, seq+1000)
		codeOf(t, err, ab2bus.CodeNotFound)
		// Revisions needs read.
		_, err = h.as("nobody").Revisions(ctx, seq)
		codeOf(t, err, ab2bus.CodeForbidden)
	})
}

func TestMemoryEditRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		tasks := h.channel("work", ab2store.KindTasks, "owner", memoryACL(h))
		seq := h.append(ch, "owner", "", "v1")
		taskSeq := h.append(tasks, "owner", "", "a task")
		b := h.as("owner")

		_, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq + 1000, Content: memStrp("x")})
		codeOf(t, err, ab2bus.CodeNotFound)
		_, err = b.Edit(ctx, ab2bus.EditMemoryInput{ID: taskSeq, Content: memStrp("x")})
		codeOf(t, err, ab2bus.CodeValidation)
		codeOf(t, b.Delete(ctx, ab2bus.DeleteMemoryInput{ID: taskSeq}), ab2bus.CodeValidation)
		_, err = b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("")})
		codeOf(t, err, ab2bus.CodeValidation)
		eleven := strings.Split("a b c d e f g h i j k", " ")
		_, err = b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("x"), Tags: eleven})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("x"), Tags: []string{"bad tag!"}})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("x"), Subject: memStrp("two\nlines")})
		codeOf(t, err, ab2bus.CodeValidation)
		big := strings.Repeat("x", (h.cfg.MaxMessageKiB+1)*1024)
		_, err = b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp(big)})
		codeOf(t, err, ab2bus.CodeValidation)
		// Nothing above changed the memory.
		revs, err := b.Revisions(ctx, seq)
		if err != nil || len(revs) != 1 {
			t.Fatalf("revisions = %d, %v; want 1", len(revs), err)
		}
		if m, err := b.GetMemory(ctx, seq); err != nil || m.Content != "v1" {
			t.Fatalf("GetMemory = %+v, %v", m, err)
		}
	})
}

func TestMemoryEditRateLimited(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		cfg := core.Defaults()
		cfg.SendMessagesPerSecond = 1
		h := newHarness(t, e, harnessOptions{cfg: &cfg})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		seq := h.append(ch, "owner", "", "v1")
		b := h.as("owner")
		if _, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v2")}); err != nil {
			t.Fatal(err)
		}
		_, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v3")})
		codeOf(t, err, ab2bus.CodeRateLimited)
		// The refused edit left the memory at revision 2.
		revs, _ := b.Revisions(ctx, seq)
		if len(revs) != 2 {
			t.Fatalf("revisions = %d, want 2", len(revs))
		}
	})
}

func TestMemoryDelete(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		seq := h.append(ch, "owner", "", "v1")
		b := h.as("owner")
		in := ab2bus.DeleteMemoryInput{ID: seq, IdempotencyKey: "del-1"}

		if err := b.Delete(ctx, in); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		// Receipt replay: nil again, even though the memory is gone.
		if err := b.Delete(ctx, in); err != nil {
			t.Fatalf("replayed Delete: %v", err)
		}
		_, err := b.GetMemory(ctx, seq)
		codeOf(t, err, ab2bus.CodeNotFound)
		revs, err := b.Revisions(ctx, seq)
		if err != nil || len(revs) != 1 {
			t.Fatalf("Revisions = %d, %v; want the tombstoned row", len(revs), err)
		}
		// Unkeyed second delete: no live revision.
		codeOf(t, b.Delete(ctx, ab2bus.DeleteMemoryInput{ID: seq}), ab2bus.CodeNotFound)
		// The same key with another payload conflicts.
		codeOf(t, b.Delete(ctx, ab2bus.DeleteMemoryInput{ID: seq + 1, IdempotencyKey: "del-1"}), ab2bus.CodeConflict)
		// The access row went with the memory.
		if got := memExpiredIDs(t, h, h.core.NowMs()+1); len(got) != 0 {
			t.Fatalf("expired = %v, want none", got)
		}
	})
}

func TestMemoryEditIdempotent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		seq := h.append(ch, "owner", "", "v1")
		b := h.as("owner")
		in := ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v2"), IdempotencyKey: "k"}

		first, err := b.Edit(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		second, err := b.Edit(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Fatalf("replay = %+v, first = %+v", second, first)
		}
		revs, _ := b.Revisions(ctx, seq)
		if len(revs) != 2 {
			t.Fatalf("revisions = %d, want 2", len(revs))
		}
		in.Content = memStrp("other")
		_, err = b.Edit(ctx, in)
		codeOf(t, err, ab2bus.CodeConflict)
	})
}

// Post-build decision 10: an omitted field is kept, an explicitly empty one
// clears, content cannot be cleared, and IfRevision guards the live revision.
func TestMemoryEditMerge(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("mem", ab2store.KindMemory, "owner", memoryACL(h))
		seq := h.append(ch, "owner", "", "v1", "red")
		b := h.as("owner")
		meta := map[string]string{"k": "v"}
		if _, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Subject: memStrp("s"), Type: memStrp("note"), Metadata: &meta}); err != nil {
			t.Fatalf("setup edit (no content): %v", err)
		}
		get := func() ab2bus.Message {
			t.Helper()
			m, err := b.GetMemory(ctx, seq)
			if err != nil {
				t.Fatal(err)
			}
			return m
		}
		m := get()
		if m.Content != "v1" || m.Type != "note" || m.Subject != "s" || !reflect.DeepEqual(m.Metadata, meta) || !reflect.DeepEqual(m.Tags, []string{"red"}) {
			t.Fatalf("after setup: %+v", m)
		}

		// Content only: everything else is kept.
		if _, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v2")}); err != nil {
			t.Fatal(err)
		}
		m = get()
		if m.Content != "v2" || m.Type != "note" || m.Subject != "s" || !reflect.DeepEqual(m.Metadata, meta) || !reflect.DeepEqual(m.Tags, []string{"red"}) {
			t.Fatalf("content-only edit: %+v", m)
		}

		// Type "" clears type only.
		if _, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Type: memStrp("")}); err != nil {
			t.Fatal(err)
		}
		m = get()
		if m.Content != "v2" || m.Type != "" || m.Subject != "s" || !reflect.DeepEqual(m.Metadata, meta) {
			t.Fatalf("type clear: %+v", m)
		}

		// Empty metadata clears metadata only.
		empty := map[string]string{}
		if _, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Metadata: &empty}); err != nil {
			t.Fatal(err)
		}
		m = get()
		if m.Content != "v2" || len(m.Metadata) != 0 || m.Subject != "s" {
			t.Fatalf("metadata clear: %+v", m)
		}

		// Metadata only keeps the content.
		if _, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Metadata: &meta}); err != nil {
			t.Fatal(err)
		}
		m = get()
		if m.Content != "v2" || !reflect.DeepEqual(m.Metadata, meta) {
			t.Fatalf("metadata only: %+v", m)
		}

		// Empty content and an empty request are validation errors.
		_, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("")})
		codeOf(t, err, ab2bus.CodeValidation)
		if !strings.Contains(err.Error(), "delete the memory instead") {
			t.Fatalf("empty content message: %v", err)
		}
		_, err = b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq})
		codeOf(t, err, ab2bus.CodeValidation)

		// IfRevision: equal succeeds, stale conflicts and writes nothing.
		live := get()
		rev := *live.Revision
		if _, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v3"), IfRevision: rev}); err != nil {
			t.Fatalf("matching if_revision: %v", err)
		}
		before, _ := b.Revisions(ctx, seq)
		_, err = b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v4"), IfRevision: rev})
		codeOf(t, err, ab2bus.CodeConflict)
		want := fmt.Sprintf("memory %d is at revision %d, not %d", seq, rev+1, rev)
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("conflict message %q lacks %q", err.Error(), want)
		}
		after, _ := b.Revisions(ctx, seq)
		if len(after) != len(before) || get().Content != "v3" {
			t.Fatalf("stale edit wrote: %d -> %d revisions, content %q", len(before), len(after), get().Content)
		}

		// A keyed replay returns the first result even after the memory moved on.
		in := ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v5"), IdempotencyKey: "merge-k"}
		first, err := b.Edit(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.Edit(ctx, ab2bus.EditMemoryInput{ID: seq, Content: memStrp("v6")}); err != nil {
			t.Fatal(err)
		}
		second, err := b.Edit(ctx, in)
		if err != nil || second != first {
			t.Fatalf("replay = %+v, %v; first = %+v", second, err, first)
		}
	})
}
