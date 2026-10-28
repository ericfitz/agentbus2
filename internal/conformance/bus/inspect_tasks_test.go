package bus

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/local"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// reset forgets the requests seen so far.
func (f *fakeInspector) reset() {
	f.mu.Lock()
	f.reqs = nil
	f.mu.Unlock()
}

func TestInspectTaskCreate(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		var refuse atomic.Bool
		fi := &fakeInspector{reply: func(context.Context, core.InspectRequest) ([]byte, error) {
			if refuse.Load() {
				return refuseJSON("policy", "no"), nil
			}
			return []byte(allowJSON), nil
		}}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		h.taskList("todo")
		pat := h.as("pat")
		in := ab2bus.TaskCreateInput{Channel: "todo", Subject: "s", Description: "d", Metadata: map[string]string{"k": "v"}}

		got, err := pat.Create(ctx, in)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		calls := fi.calls()
		if len(calls) != 1 {
			t.Fatalf("%d inspector calls, want 1", len(calls))
		}
		c := calls[0]
		c.RequestID = ""
		want := core.InspectRequest{
			Version: 1, Operation: "task_create", Source: "api", Sender: "pat", SenderKind: "agent",
			Channel: core.InspectChannel{Name: "todo", Kind: "tasks"},
			Record:  core.InspectRecord{Subject: "s", Description: "d", Metadata: map[string]string{"k": "v"}},
		}
		if !reflect.DeepEqual(c, want) {
			t.Errorf("request = %+v, want %+v", c, want)
		}
		if got.Subject != "s" {
			t.Errorf("created %+v", got)
		}

		refuse.Store(true)
		_, err = pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "second"})
		codeOf(t, err, ab2bus.CodeInspectionRejected)
		sums, err := pat.List(ctx, ab2bus.TaskListInput{Channel: "todo"})
		if err != nil || len(sums) != 1 {
			t.Fatalf("list after refusal = %+v, %v; want only the first task", sums, err)
		}
	})
}

func TestInspectTaskUpdateTrigger(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		h.taskList("todo", "pat", "kim")
		pat := h.as("pat")
		a, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "a", Description: "d", Metadata: map[string]string{"k": "v"}})
		if err != nil {
			t.Fatal(err)
		}
		b, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "b"})
		if err != nil {
			t.Fatal(err)
		}
		fi.reset()

		// A description patch is inspected, with the merged metadata.
		res, err := pat.Update(ctx, ab2bus.TaskPatch{ID: a.ID, Description: taskPtr("new"), Metadata: map[string]string{"x": "y"}})
		if err != nil {
			t.Fatalf("description patch: %v", err)
		}
		calls := fi.calls()
		if len(calls) != 1 {
			t.Fatalf("%d calls after a description patch, want 1", len(calls))
		}
		c := calls[0]
		c.RequestID = ""
		want := core.InspectRequest{
			Version: 1, Operation: "task_update", Source: "api", Sender: "pat", SenderKind: "agent",
			Channel: core.InspectChannel{Name: "todo", Kind: "tasks"},
			Record:  core.InspectRecord{Subject: "a", Description: "new", Metadata: map[string]string{"k": "v", "x": "y"}},
		}
		if !reflect.DeepEqual(c, want) {
			t.Errorf("request = %+v, want %+v", c, want)
		}
		a = res.Task
		fi.reset()

		// None of these reach the inspector.
		steps := []struct {
			name  string
			patch ab2bus.TaskPatch
		}{
			{"claim", ab2bus.TaskPatch{ID: a.ID, Owner: taskPtr("kim"), Status: taskPtr("in_progress")}},
			{"release", ab2bus.TaskPatch{ID: a.ID, Owner: taskPtr(""), Status: taskPtr("pending"), LeasedUntil: taskPtr(int64(0))}},
			{"status", ab2bus.TaskPatch{ID: a.ID, Status: taskPtr("completed")}},
			{"owner", ab2bus.TaskPatch{ID: b.ID, Owner: taskPtr("pat")}},
			{"move", ab2bus.TaskPatch{ID: b.ID, Before: a.ID}},
			{"add_blocked_by", ab2bus.TaskPatch{ID: b.ID, AddBlockedBy: []int64{a.ID}}},
			{"same description", ab2bus.TaskPatch{ID: a.ID, Description: taskPtr("new")}},
			{"same subject and metadata", ab2bus.TaskPatch{ID: a.ID, Subject: taskPtr("a"), Metadata: map[string]string{"k": "v"}}},
			{"delete", ab2bus.TaskPatch{ID: b.ID, Delete: true}},
		}
		for _, s := range steps {
			if _, err := pat.Update(ctx, s.patch); err != nil {
				t.Fatalf("%s: %v", s.name, err)
			}
			if n := len(fi.calls()); n != 0 {
				t.Fatalf("%s: %d inspector calls, want 0", s.name, n)
			}
		}
	})
}

func TestInspectTaskUpdateAbandoned(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		h.taskList("todo", "kim")
		pat, kim := h.as("pat"), h.as("kim")
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "first"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kim.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID}); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds+1) * time.Second)
		fi.reset()

		res, err := pat.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Subject: taskPtr("renamed")})
		if err != nil {
			t.Fatalf("subject patch on the abandoned task: %v", err)
		}
		if n := len(fi.calls()); n != 1 {
			t.Errorf("%d inspector calls, want 1", n)
		}
		if res.Task.Status != "pending" || res.Task.Owner != "" || res.Task.Subject != "renamed" {
			t.Errorf("result = %+v, want pending, no owner, renamed", res.Task)
		}
	})
}

func TestInspectTaskConcurrent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()

		// The first reply patches the description through a sibling core, then allows.
		var n atomic.Int32
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		sib := local.New(h.sibling(harnessOptions{}).core, "pat")
		h.taskList("todo")
		made, err := h.as("pat").Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "s", Description: "d"})
		if err != nil {
			t.Fatal(err)
		}
		fi.reset()
		fi.reply = func(ctx context.Context, _ core.InspectRequest) ([]byte, error) {
			if n.Add(1) == 1 {
				if _, err := sib.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Description: taskPtr("sibling")}); err != nil {
					return nil, err
				}
			}
			return []byte(allowJSON), nil
		}
		res, err := h.as("pat").Update(ctx, ab2bus.TaskPatch{ID: made.ID, Subject: taskPtr("t")})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		calls := fi.calls()
		if len(calls) != 2 || calls[0].Record.Description != "d" || calls[1].Record.Description != "sibling" {
			t.Fatalf("calls = %+v, want two with descriptions d and sibling", calls)
		}
		if res.Task.Subject != "t" || res.Task.Description != "sibling" {
			t.Errorf("result = %+v, want subject t, description sibling", res.Task)
		}

		// A reply that patches on every call never settles: a retryable conflict.
		fi2 := &fakeInspector{}
		h2 := newHarness(t, e, harnessOptions{inspector: fi2})
		sib2 := local.New(h2.sibling(harnessOptions{}).core, "pat")
		h2.taskList("todo")
		made2, err := h2.as("pat").Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "s"})
		if err != nil {
			t.Fatal(err)
		}
		fi2.reset()
		var k atomic.Int32
		fi2.reply = func(ctx context.Context, _ core.InspectRequest) ([]byte, error) {
			d := string(rune('a' + k.Add(1)))
			if _, err := sib2.Update(ctx, ab2bus.TaskPatch{ID: made2.ID, Description: &d}); err != nil {
				return nil, err
			}
			return []byte(allowJSON), nil
		}
		_, err = h2.as("pat").Update(ctx, ab2bus.TaskPatch{ID: made2.ID, Subject: taskPtr("t")})
		codeOf(t, err, ab2bus.CodeConflict)
		var be *ab2bus.Error
		if !errors.As(err, &be) || !be.Retryable {
			t.Errorf("conflict %v is not retryable", err)
		}
		if c := len(fi2.calls()); c != 2 {
			t.Errorf("%d inspector calls, want 2", c)
		}
	})
}

func TestInspectTaskRefusalsBeforeInspection(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		pat, kim := h.as("pat"), h.as("kim")
		h.taskList("todo")
		h.channel("plain", ab2store.KindOrdinary, "pat", nil)
		h.channel("ro", ab2store.KindTasks, "pat", []ab2store.ACLEntry{
			{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}},
			{Identity: "kim", Permissions: []ab2store.Permission{ab2store.PermRead}},
		})
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "ro", Subject: "x"})
		if err != nil {
			t.Fatal(err)
		}
		fi.reset()

		_, err = pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "plain", Subject: "x"})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = kim.Create(ctx, ab2bus.TaskCreateInput{Channel: "ro", Subject: "x"})
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = kim.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Description: taskPtr("x")})
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "x", Parent: 99999})
		codeOf(t, err, ab2bus.CodeValidation)
		if n := len(fi.calls()); n != 0 {
			t.Errorf("%d inspector calls, want 0", n)
		}
	})
}

func TestInspectTaskKeyed(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		var allow atomic.Bool
		fi := &fakeInspector{reply: func(context.Context, core.InspectRequest) ([]byte, error) {
			if allow.Load() {
				return []byte(allowJSON), nil
			}
			return refuseJSON("policy", "no"), nil
		}}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		h.taskList("todo")
		pat := h.as("pat")
		allow.Store(true)
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "s"})
		if err != nil {
			t.Fatal(err)
		}
		allow.Store(false)
		fi.reset()

		// Create.
		cin := ab2bus.TaskCreateInput{Channel: "todo", Subject: "keyed", IdempotencyKey: "kc"}
		for i := 1; i <= 2; i++ {
			_, err = pat.Create(ctx, cin)
			codeOf(t, err, ab2bus.CodeInspectionRejected)
			if c := len(fi.calls()); c != i {
				t.Fatalf("%d calls after %d refused creates, want %d (a retry is inspected again)", c, i, i)
			}
		}
		if inspectHasReceipt(h, "pat", "kc") {
			t.Fatal("a refused keyed create left a receipt")
		}
		allow.Store(true)
		res, err := pat.Create(ctx, cin)
		if err != nil {
			t.Fatalf("allowed create: %v", err)
		}
		if c := len(fi.calls()); c != 3 {
			t.Fatalf("%d calls, want 3", c)
		}
		again, err := pat.Create(ctx, cin)
		if err != nil || !reflect.DeepEqual(again, res) {
			t.Fatalf("create replay = %+v, %v; want %+v", again, err, res)
		}
		if c := len(fi.calls()); c != 3 {
			t.Errorf("%d calls after the create replay, want 3", c)
		}

		// Update.
		allow.Store(false)
		uin := ab2bus.TaskPatch{ID: made.ID, Description: taskPtr("keyed"), IdempotencyKey: "ku"}
		for i := 4; i <= 5; i++ {
			_, err = pat.Update(ctx, uin)
			codeOf(t, err, ab2bus.CodeInspectionRejected)
			if c := len(fi.calls()); c != i {
				t.Fatalf("%d calls after a refused update, want %d", c, i)
			}
		}
		if inspectHasReceipt(h, "pat", "ku") {
			t.Fatal("a refused keyed update left a receipt")
		}
		allow.Store(true)
		ures, err := pat.Update(ctx, uin)
		if err != nil || ures.Task.Description != "keyed" {
			t.Fatalf("allowed update = %+v, %v", ures, err)
		}
		if c := len(fi.calls()); c != 6 {
			t.Fatalf("%d calls, want 6", c)
		}
		uagain, err := pat.Update(ctx, uin)
		if err != nil || !reflect.DeepEqual(uagain, ures) {
			t.Fatalf("update replay = %+v, %v; want %+v", uagain, err, ures)
		}
		if c := len(fi.calls()); c != 6 {
			t.Errorf("%d calls after the update replay, want 6", c)
		}

		// A later unkeyed patch changes the task; replaying the first key still
		// returns the first result with no inspector call.
		if _, err := pat.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Description: taskPtr("other")}); err != nil {
			t.Fatalf("unkeyed update: %v", err)
		}
		if c := len(fi.calls()); c != 7 {
			t.Fatalf("%d calls after the unkeyed update, want 7", c)
		}
		uagain, err = pat.Update(ctx, uin)
		if err != nil || !reflect.DeepEqual(uagain, ures) {
			t.Fatalf("update replay after a change = %+v, %v; want %+v", uagain, err, ures)
		}
		if c := len(fi.calls()); c != 7 {
			t.Errorf("%d calls after the second update replay, want 7", c)
		}

		// The same key with a different payload is a conflict, never inspected.
		cin2 := cin
		cin2.Subject = "different"
		_, err = pat.Create(ctx, cin2)
		codeOf(t, err, ab2bus.CodeConflict)
		uin2 := uin
		uin2.Description = taskPtr("different")
		_, err = pat.Update(ctx, uin2)
		codeOf(t, err, ab2bus.CodeConflict)
		if c := len(fi.calls()); c != 7 {
			t.Errorf("%d calls after the mismatched keys, want 7", c)
		}
	})
}

func TestInspectTaskChargesRefusal(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		fi := &fakeInspector{reply: func(context.Context, core.InspectRequest) ([]byte, error) {
			return refuseJSON("policy", "no"), nil
		}}
		h := newHarness(t, e, harnessOptions{
			cfg:       sendCfgWith(func(c *core.Config) { c.SendMessagesPerSecond = 1 }),
			inspector: fi,
		})
		h.taskList("todo")
		pat := h.as("pat")
		_, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "one"})
		codeOf(t, err, ab2bus.CodeInspectionRejected)
		_, err = pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "two"})
		codeOf(t, err, ab2bus.CodeRateLimited)
		if c := len(fi.calls()); c != 1 {
			t.Errorf("%d inspector calls, want 1 (the refusal was charged once)", c)
		}
	})
}

// rawTask stores a task row through the raw Store with the given rank, so a
// test can build the equal sibling ranks only a corrupt write produces.
func rawTask(h *harness, ch ab2store.Channel, subject, rank string) int64 {
	h.t.Helper()
	var seq int64
	err := h.raw.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		seq, _, err = h.core.Append(ctx, tx, core.AppendInput{
			ChannelID: ch.ID, Channel: ch.Name, Kind: ab2store.KindTasks, Sender: "pat", SenderKind: ab2store.IdentityAgent,
			Subject: subject, Content: `{"subject":"` + subject + `","status":"pending","rank":"` + rank + `"}`, TaskRow: true,
		})
		return err
	})
	if err != nil {
		h.t.Fatalf("raw task: %v", err)
	}
	return seq
}

func TestInspectTaskRankCollision(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		fi := &fakeInspector{}
		h := newHarness(t, e, harnessOptions{inspector: fi})
		ch := h.taskList("todo")
		a := rawTask(h, ch, "a", "V")
		b := rawTask(h, ch, "b", "V") // equal to a's rank: corrupt
		pat := h.as("pat")

		// Create after a: the pre-pass keeps rank V, the write renumbers and places.
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "new", After: a})
		if err != nil {
			t.Fatalf("Create across a collision: %v", err)
		}
		if n := len(fi.calls()); n != 1 {
			t.Fatalf("%d inspector calls after create, want 1", n)
		}
		assertRanksOrdered(t, pat, "todo", []int64{a, made.ID, b})

		// Update: move c before b (now behind a corrupt pair again) with a content patch.
		h2 := newHarness(t, e, harnessOptions{inspector: fi})
		ch2 := h2.taskList("todo")
		a2 := rawTask(h2, ch2, "a", "V")
		b2 := rawTask(h2, ch2, "b", "V")
		pat2 := h2.as("pat")
		c, err := pat2.Create(ctx, ab2bus.TaskCreateInput{Channel: "todo", Subject: "c"})
		if err != nil {
			t.Fatal(err)
		}
		fi.reset()
		if _, err := pat2.Update(ctx, ab2bus.TaskPatch{ID: c.ID, Description: taskPtr("moved"), Before: b2}); err != nil {
			t.Fatalf("Update across a collision: %v", err)
		}
		if n := len(fi.calls()); n != 1 {
			t.Fatalf("%d inspector calls after update, want 1", n)
		}
		assertRanksOrdered(t, pat2, "todo", []int64{a2, c.ID, b2})
		got, err := pat2.GetTask(ctx, c.ID)
		if err != nil || got.Description != "moved" {
			t.Fatalf("task after update = %+v, %v", got, err)
		}
	})
}

// assertRanksOrdered checks that the tasks have strictly increasing ranks in
// the given order.
func assertRanksOrdered(t *testing.T, b ab2bus.Bus, list string, ids []int64) {
	t.Helper()
	prev := ""
	for i, id := range ids {
		got, err := b.GetTask(context.Background(), id)
		if err != nil {
			t.Fatalf("GetTask %d: %v", id, err)
		}
		if i > 0 && got.Rank <= prev {
			t.Errorf("task %d rank %q is not above %q (order %v)", id, got.Rank, prev, ids)
		}
		prev = got.Rank
	}
}
