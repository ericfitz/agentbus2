package bus

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/core"
	"github.com/ericfitz/agentmesh/internal/core/tasks"
	"github.com/ericfitz/agentmesh/internal/dbtest"
	ab2store "github.com/ericfitz/agentmesh/internal/store"
)

func taskPtr[T any](v T) *T { return &v }

// revisions returns every stored revision row of a task, by revision.
func (h *harness) taskRevisions(id int64) []ab2store.Message {
	h.t.Helper()
	var ms []ab2store.Message
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		ms, err = tx.MemoryRevisions(ctx, id)
		return err
	})
	return ms
}

// taskList creates a tasks-kind channel owned by pat and returns it, with a
// live session row for every named identity so that their claims are not
// abandoned the moment they are made.
func (h *harness) taskList(name string, live ...string) ab2store.Channel {
	h.t.Helper()
	ch := h.channel(name, ab2store.KindTasks, "pat", nil)
	for _, id := range live {
		h.session(id, id)
	}
	return ch
}

func (h *harness) taskLeaseIn(d time.Duration) int64 { return h.clock.Now().Add(d).UnixMilli() }

// mustTask returns a check of a Create, Update or Get result that fails the
// test on an error: mustTask(t)(b.Create(ctx, in)).
func mustTask(t *testing.T) func(ab2bus.Task, error) ab2bus.Task {
	return func(task ab2bus.Task, err error) ab2bus.Task {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
}

func TestTaskCreateNeedsWriteAndKind(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat, kim := h.as("pat"), h.as("kim")
		h.channel("plain", ab2store.KindOrdinary, "pat", nil)
		_, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "plain", Subject: "x"})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "missing", Subject: "x"})
		codeOf(t, err, ab2bus.CodeNotFound)

		h.channel("work", ab2store.KindTasks, "pat", []ab2store.ACLEntry{
			{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}},
			{Identity: "kim", Permissions: []ab2store.Permission{ab2store.PermRead}},
		})
		_, err = kim.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "x"})
		codeOf(t, err, ab2bus.CodeForbidden)
		got, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"})
		if err != nil {
			t.Fatal(err)
		}
		if got.ID == 0 || got.Rank != "V" || got.Status != "pending" || got.Channel != "work" || got.Revision != 1 || got.UpdatedBy != "pat" {
			t.Fatalf("created = %+v", got)
		}
		_, err = pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: ""})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "x", Parent: 999})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = h.as("ghost").Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "x"})
		codeOf(t, err, ab2bus.CodeForbidden)
	})
}

func TestTaskGetAndListNeedRead(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat, kim := h.as("pat"), h.as("kim")
		h.channel("work", ab2store.KindTasks, "pat", []ab2store.ACLEntry{
			{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}},
		})
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = kim.GetTask(ctx, made.ID)
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = kim.List(ctx, ab2bus.TaskListInput{Channel: "work"})
		codeOf(t, err, ab2bus.CodeForbidden)
		got, err := pat.GetTask(ctx, made.ID)
		if err != nil || got.Subject != "first" {
			t.Fatalf("Get = %+v, %v", got, err)
		}
		sums, err := pat.List(ctx, ab2bus.TaskListInput{Channel: "work"})
		if err != nil || len(sums) != 1 || sums[0].ID != made.ID {
			t.Fatalf("List = %+v, %v", sums, err)
		}
		_, err = pat.GetTask(ctx, 999)
		codeOf(t, err, ab2bus.CodeNotFound)
		h.channel("plain", ab2store.KindOrdinary, "pat", nil)
		plain := h.append(h.channel("mem", ab2store.KindMemory, "pat", nil), "pat", "", "a memory, not a task")
		_, err = pat.GetTask(ctx, plain)
		codeOf(t, err, ab2bus.CodeNotFound)
		_, err = pat.List(ctx, ab2bus.TaskListInput{Channel: "plain"})
		codeOf(t, err, ab2bus.CodeValidation)
	})
}

func TestTaskClaimUpdateRelease(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.taskList("work", "pat", "kim")
		pat, kim := h.as("pat"), h.as("kim")
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"})
		if err != nil {
			t.Fatal(err)
		}
		lease := h.taskLeaseIn(time.Minute)
		res, err := pat.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID, LeasedUntil: lease})
		if err != nil {
			t.Fatal(err)
		}
		if res.Task.Owner != "pat" || res.Task.Status != "in_progress" || res.Task.LeasedUntil != lease || res.Task.Revision != 2 || res.Replaced == 0 {
			t.Fatalf("claim = %+v", res)
		}
		// There is no owner guard: another writer updates and takes over the task,
		// and the update writes an ordinary revision.
		upd, err := kim.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Description: taskPtr("mine now")})
		if err != nil || upd.Task.Description != "mine now" || upd.Task.UpdatedBy != "kim" {
			t.Fatalf("non-owner update = %+v, %v", upd, err)
		}
		if _, err := kim.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID}); err != nil {
			t.Fatalf("non-owner claim: %v", err)
		}
		for _, r := range h.taskRevisions(made.ID) {
			if r.Type == "forced" {
				t.Fatalf("a forced revision was written: %+v", r)
			}
		}
		if _, err := pat.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID, LeasedUntil: lease}); err != nil {
			t.Fatal(err)
		}
		// Release clears owner and lease.
		rel, err := pat.Release(ctx, ab2bus.TaskReleaseInput{ID: made.ID})
		if err != nil || rel.Task.Status != "pending" || rel.Task.Owner != "" || rel.Task.LeasedUntil != 0 {
			t.Fatalf("release = %+v, %v", rel, err)
		}
		// An unknown status is validation and writes no revision.
		before := len(h.taskRevisions(made.ID))
		_, err = pat.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Status: taskPtr("paused")})
		codeOf(t, err, ab2bus.CodeValidation)
		if after := len(h.taskRevisions(made.ID)); after != before {
			t.Fatalf("revisions %d -> %d after a refused patch", before, after)
		}
		// A no-op writes nothing either.
		noop, err := pat.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Subject: taskPtr("first")})
		if err != nil || noop.Replaced != 0 || len(h.taskRevisions(made.ID)) != before {
			t.Fatalf("no-op = %+v, %v, revisions %d", noop, err, len(h.taskRevisions(made.ID)))
		}
		// Only one live revision remains.
		live := 0
		for _, r := range h.taskRevisions(made.ID) {
			if !r.Tombstone {
				live++
			}
		}
		if live != 1 {
			t.Fatalf("%d live revisions, want 1", live)
		}
	})
}

func TestTaskUpdateNeedsWrite(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.ensureIdentity("kim")
		h.channel("work", ab2store.KindTasks, "pat", []ab2store.ACLEntry{
			{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}},
			{Identity: "kim", Permissions: []ab2store.Permission{ab2store.PermRead}},
		})
		pat, kim := h.as("pat"), h.as("kim")
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = kim.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Description: taskPtr("x")})
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = kim.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID})
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = kim.Release(ctx, ab2bus.TaskReleaseInput{ID: made.ID})
		codeOf(t, err, ab2bus.CodeForbidden)
		_, err = pat.Update(ctx, ab2bus.TaskPatch{ID: 999, Description: taskPtr("x")})
		codeOf(t, err, ab2bus.CodeNotFound)
	})
}

func TestTaskListRejectsUnknownStatus(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.taskList("work", "pat")
		pat := h.as("pat")
		a := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "a"}))
		b := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "b"}))
		c := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "c", Parent: a.ID}))
		if _, err := pat.Claim(ctx, ab2bus.TaskClaimInput{ID: b.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := pat.Update(ctx, ab2bus.TaskPatch{ID: c.ID, Status: taskPtr("completed")}); err != nil {
			t.Fatal(err)
		}
		_, err := pat.List(ctx, ab2bus.TaskListInput{Channel: "work", Status: "paused"})
		codeOf(t, err, ab2bus.CodeValidation)
		all, err := pat.List(ctx, ab2bus.TaskListInput{Channel: "work"})
		if err != nil || len(all) != 3 {
			t.Fatalf("all = %+v, %v", all, err)
		}
		// Depth-first tree order: a, its child c, then b.
		if all[0].ID != a.ID || all[1].ID != c.ID || all[1].Depth != 1 || all[2].ID != b.ID {
			t.Fatalf("order = %+v", all)
		}
		for status, want := range map[string]int64{"pending": a.ID, "in_progress": b.ID, "completed": c.ID} {
			got, err := pat.List(ctx, ab2bus.TaskListInput{Channel: "work", Status: status})
			if err != nil || len(got) != 1 || got[0].ID != want {
				t.Fatalf("status %s = %+v, %v; want only %d", status, got, err, want)
			}
		}
		byOwner, err := pat.List(ctx, ab2bus.TaskListInput{Channel: "work", Owner: "pat"})
		if err != nil || len(byOwner) != 2 {
			t.Fatalf("owner pat = %+v, %v; want the claimed and the completed task", byOwner, err)
		}
	})
}

func TestTaskOwnerRegistryCheck(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.taskList("work")
		pat := h.as("pat")
		h.ensureIdentity("kim") // registered, never opened a session
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"})
		if err != nil {
			t.Fatal(err)
		}
		for _, owner := range []string{"ghost", "everyone", "admins"} {
			_, err := pat.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Owner: taskPtr(owner)})
			codeOf(t, err, ab2bus.CodeNotFound)
		}
		got, err := pat.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Owner: taskPtr("kim")})
		if err != nil || got.Task.Owner != "kim" {
			t.Fatalf("assign to a registered identity = %+v, %v", got, err)
		}
	})
}

func TestTaskAbandonedByLease(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.taskList("work", "pat") // pat holds a live session, so only the lease can abandon
		pat := h.as("pat")
		made := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"}))
		if _, err := pat.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID, LeasedUntil: h.taskLeaseIn(10 * time.Second)}); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(9 * time.Second)
		got, err := pat.GetTask(ctx, made.ID)
		if err != nil || got.Status != "in_progress" {
			t.Fatalf("before expiry: %+v, %v", got, err)
		}
		h.clock.Advance(2 * time.Second)
		got, err = pat.GetTask(ctx, made.ID)
		if err != nil || got.Status != "pending" || got.Owner != "" || got.LeasedUntil != 0 {
			t.Fatalf("after expiry: %+v, %v", got, err)
		}
		revs := h.taskRevisions(made.ID)
		var reclaimed *ab2store.Message
		for i := range revs {
			if revs[i].Type == "reclaimed" {
				reclaimed = &revs[i]
			}
		}
		if reclaimed == nil || reclaimed.Tombstone || reclaimed.Sender != "" || *reclaimed.Revision != 3 {
			t.Fatalf("revisions = %+v, want a live reclaimed revision 3 sent by nobody", revs)
		}
	})
}

func TestTaskAbandonedByDeadOwner(t *testing.T) {
	// Decision (Eric, 2026-10-21): Milestone 3 item 13: owner liveness is a live session row.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("work", ab2store.KindTasks, "pat", nil)
		pat, kim := h.as("pat"), h.as("kim")
		h.session("kim", "kim")
		made := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"}))
		if _, err := kim.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID}); err != nil {
			t.Fatal(err)
		}
		span := time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds) * time.Second
		h.clock.Advance(span - time.Second)
		sums, err := pat.List(ctx, ab2bus.TaskListInput{Channel: "work"})
		if err != nil || sums[0].Status != "in_progress" {
			t.Fatalf("one second inside the lease and skew: %+v, %v", sums, err)
		}
		h.clock.Advance(time.Second) // exactly lease + skew: IsLive is inclusive
		sums, _ = pat.List(ctx, ab2bus.TaskListInput{Channel: "work"})
		if sums[0].Status != "in_progress" {
			t.Fatalf("at the skew boundary: %+v", sums)
		}
		h.clock.Advance(time.Second)
		sums, err = pat.List(ctx, ab2bus.TaskListInput{Channel: "work"})
		if err != nil || sums[0].Status != "pending" || sums[0].Owner != "" {
			t.Fatalf("after the lease and skew: %+v, %v", sums, err)
		}
		got, err := pat.GetTask(ctx, made.ID)
		if err != nil || got.Status != "pending" {
			t.Fatalf("Get = %+v, %v", got, err)
		}
		if n := countTaskRevs(h.taskRevisions(made.ID), "reclaimed"); n != 1 {
			t.Fatalf("%d reclaimed revisions, want 1", n)
		}

		// A live row that is closed reclaims at once.
		second := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "second"}))
		live := h.session("kim", "kim")
		if _, err := kim.Claim(ctx, ab2bus.TaskClaimInput{ID: second.ID}); err != nil {
			t.Fatal(err)
		}
		got, _ = pat.GetTask(ctx, second.ID)
		if got.Status != "in_progress" {
			t.Fatalf("with a live session: %+v", got)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.CloseSession(ctx, live.ID, h.core.NowMs()) })
		got, err = pat.GetTask(ctx, second.ID)
		if err != nil || got.Status != "pending" {
			t.Fatalf("after CloseSession: %+v, %v", got, err)
		}
	})
}

func countTaskRevs(ms []ab2store.Message, typ string) int {
	n := 0
	for _, m := range ms {
		if m.Type == typ {
			n++
		}
	}
	return n
}

func TestTaskRefusedUpdateRollsBackReclaim(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("work", ab2store.KindTasks, "pat", nil)
		pat, kim := h.as("pat"), h.as("kim")
		made := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"}))
		if _, err := pat.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID}); err != nil { // pat has no session: abandoned at once
			t.Fatal(err)
		}
		before := len(h.taskRevisions(made.ID))
		_, err := kim.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Status: taskPtr("paused")})
		codeOf(t, err, ab2bus.CodeValidation)
		if after := len(h.taskRevisions(made.ID)); after != before {
			t.Fatalf("revisions %d -> %d: the refused patch kept the reclaim", before, after)
		}
		// An accepted claim on the abandoned task is a reclaim followed by the claim.
		got, err := kim.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID})
		if err != nil || got.Task.Owner != "kim" {
			t.Fatalf("claim of an abandoned task = %+v, %v", got, err)
		}
		if n := countTaskRevs(h.taskRevisions(made.ID), "reclaimed"); n != 1 {
			t.Fatalf("%d reclaimed revisions, want 1", n)
		}
	})
}

func TestTaskReclaimChannelIdempotent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.taskList("work")
		pat := h.as("pat")
		a := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "a"}))
		b := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "b"}))
		for _, id := range []int64{a.ID, b.ID} {
			if _, err := pat.Claim(ctx, ab2bus.TaskClaimInput{ID: id}); err != nil {
				t.Fatal(err)
			}
		}
		svc := tasks.Service{Core: h.core}
		n, err := svc.ReclaimChannel(ctx, ch.ID, h.core.NowMs())
		if err != nil || n != 2 {
			t.Fatalf("first ReclaimChannel = %d, %v; want 2", n, err)
		}
		n, err = svc.ReclaimChannel(ctx, ch.ID, h.core.NowMs())
		if err != nil || n != 0 {
			t.Fatalf("second ReclaimChannel = %d, %v; want 0", n, err)
		}
		plain := h.channel("plain", ab2store.KindOrdinary, "pat", nil)
		if _, err := svc.ReclaimChannel(ctx, plain.ID, h.core.NowMs()); err == nil {
			t.Fatal("ReclaimChannel on an ordinary channel must be refused")
		}
	})
}

func TestTaskDelete(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.taskList("work", "pat", "kim")
		pat, kim := h.as("pat"), h.as("kim")
		parent := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "parent"}))
		child := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "child", Parent: parent.ID}))
		_, err := pat.Update(ctx, ab2bus.TaskPatch{ID: parent.ID, Delete: true})
		codeOf(t, err, ab2bus.CodeConflict)
		res, err := pat.Update(ctx, ab2bus.TaskPatch{ID: child.ID, Delete: true})
		if err != nil || !res.Deleted {
			t.Fatalf("delete = %+v, %v", res, err)
		}
		_, err = pat.GetTask(ctx, child.ID)
		codeOf(t, err, ab2bus.CodeNotFound)

		// A non-owner deletes another identity's in_progress task: no owner guard,
		// and the last revision is an ordinary tombstone.
		if _, err := pat.Claim(ctx, ab2bus.TaskClaimInput{ID: parent.ID}); err != nil {
			t.Fatal(err)
		}
		res, err = kim.Update(ctx, ab2bus.TaskPatch{ID: parent.ID, Delete: true})
		if err != nil || !res.Deleted {
			t.Fatalf("non-owner delete = %+v, %v", res, err)
		}
		revs := h.taskRevisions(parent.ID)
		if last := revs[len(revs)-1]; last.Type == "forced" || !last.Tombstone {
			t.Fatalf("last revision = %+v, want an ordinary tombstone", last)
		}
		for _, r := range revs {
			if !r.Tombstone {
				t.Fatalf("a live revision remains: %+v", r)
			}
		}
	})
}

func TestTaskIdempotentCreate(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.taskList("work")
		pat := h.as("pat")
		in := ab2bus.TaskCreateInput{Channel: "work", Subject: "once", IdempotencyKey: "k1"}
		first, err := pat.Create(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		again, err := pat.Create(ctx, in)
		if err != nil || again.ID != first.ID {
			t.Fatalf("replay = %+v, %v; want id %d", again, err, first.ID)
		}
		var live int
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			ms, err := tx.LiveMemories(ctx, ch.ID)
			live = len(ms)
			return err
		})
		if live != 1 {
			t.Fatalf("%d live task rows, want 1", live)
		}
		in.Subject = "other"
		_, err = pat.Create(ctx, in)
		codeOf(t, err, ab2bus.CodeConflict)
		// The key is per identity.
		other := in
		other.Subject = "once"
		second, err := h.as("kim").Create(ctx, other)
		if err != nil || second.ID == first.ID {
			t.Fatalf("kim with the same key = %+v, %v; want a new task", second, err)
		}
		// Replay of an update survives the task's deletion.
		up := ab2bus.TaskPatch{ID: first.ID, Description: taskPtr("d"), IdempotencyKey: "u1"}
		r1, err := pat.Update(ctx, up)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pat.Update(ctx, ab2bus.TaskPatch{ID: first.ID, Delete: true}); err != nil {
			t.Fatal(err)
		}
		r2, err := pat.Update(ctx, up)
		if err != nil || r2.Task.Revision != r1.Task.Revision {
			t.Fatalf("replay after delete = %+v, %v", r2, err)
		}
	})
}

func TestTaskRateLimited(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		cfg := core.Defaults()
		cfg.SendMessagesPerSecond = 2
		h := newHarness(t, e, harnessOptions{cfg: &cfg})
		ctx := context.Background()
		h.taskList("work")
		pat := h.as("pat")
		for i := 0; i < 2; i++ {
			if _, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "t"}); err != nil {
				t.Fatalf("create %d: %v", i, err)
			}
		}
		_, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "t"})
		codeOf(t, err, ab2bus.CodeRateLimited)
		if be, ok := err.(*ab2bus.Error); !ok || !be.Retryable {
			t.Fatalf("err = %#v, want retryable", err)
		}
		h.clock.Advance(time.Second)
		if _, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "t"}); err != nil {
			t.Fatalf("after a second: %v", err)
		}
		// A refused create charged nothing and wrote nothing.
		sums, err := pat.List(ctx, ab2bus.TaskListInput{Channel: "work"})
		if err != nil || len(sums) != 3 {
			t.Fatalf("List = %d rows, %v; want 3", len(sums), err)
		}
	})
}

func TestTaskChargesOncePerCommit(t *testing.T) {
	// Review Focus 3: a retried transaction body charges the limiter once.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		cfg := core.Defaults()
		cfg.SendMessagesPerSecond = 2
		h := newHarness(t, e, harnessOptions{cfg: &cfg, fault: &pgconnError40001})
		ctx := context.Background()
		h.taskList("work")
		pat := h.as("pat")
		create := func() error {
			_, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "t"})
			return err
		}
		want := 2
		if e.Name == "sqlite" {
			// The fault surfaces unretried; nothing committed or charged.
			codeOf(t, create(), ab2bus.CodeInternal)
		} else if err := create(); err != nil {
			t.Fatalf("postgres retries the fault: %v", err)
		}
		if err := create(); err != nil {
			t.Fatalf("second: %v", err)
		}
		if e.Name == "sqlite" {
			if err := create(); err != nil {
				t.Fatalf("third on sqlite: %v", err)
			}
		}
		codeOf(t, create(), ab2bus.CodeRateLimited)
		sums, err := pat.List(ctx, ab2bus.TaskListInput{Channel: "work"})
		if err != nil || len(sums) != want {
			t.Fatalf("List = %d rows, %v; want %d", len(sums), err, want)
		}
	})
}

func TestTaskWritePublishesHint(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.taskList("work")
		hints, cancel := h.notifier.Subscribe()
		defer cancel()
		made, err := h.as("pat").Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "t"})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case hint := <-hints:
			if hint.ChannelID != ch.ID || hint.Seq != made.ID {
				t.Fatalf("hint = %+v, want channel %d seq %d", hint, ch.ID, made.ID)
			}
		case <-time.After(time.Second):
			t.Fatal("no hint after a committed create")
		}
	})
}

// TestAnyWriterChangesAnyTask: write on a task list is the only gate (Eric
// 2026-10-25, spec 3.14); there is no owner guard and no force.
func TestAnyWriterChangesAnyTask(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.taskList("work", "pat", "kim")
		pat, kim := h.as("pat"), h.as("kim")

		first, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kim.Claim(ctx, ab2bus.TaskClaimInput{ID: first.ID}); err != nil {
			t.Fatal(err)
		}
		res, err := pat.Update(ctx, ab2bus.TaskPatch{ID: first.ID, Status: taskPtr("completed")})
		if err != nil || res.Task.Status != "completed" || res.Task.UpdatedBy != "pat" {
			t.Fatalf("non-owner complete = %+v, %v", res, err)
		}

		second, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "second"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kim.Claim(ctx, ab2bus.TaskClaimInput{ID: second.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := pat.Update(ctx, ab2bus.TaskPatch{ID: second.ID, Description: taskPtr("edited by pat")}); err != nil {
			t.Fatalf("non-owner edit of an in_progress task: %v", err)
		}
		if _, err := pat.Release(ctx, ab2bus.TaskReleaseInput{ID: second.ID}); err != nil {
			t.Fatalf("non-owner release: %v", err)
		}
		if _, err := kim.Claim(ctx, ab2bus.TaskClaimInput{ID: second.ID}); err != nil {
			t.Fatal(err)
		}
		del, err := pat.Update(ctx, ab2bus.TaskPatch{ID: second.ID, Delete: true})
		if err != nil || !del.Deleted {
			t.Fatalf("non-owner delete = %+v, %v", del, err)
		}
		for _, id := range []int64{first.ID, second.ID} {
			for _, r := range h.taskRevisions(id) {
				if r.Type == "forced" {
					t.Fatalf("task %d: a forced revision was written: %+v", id, r)
				}
			}
		}
	})
}

// liveTaskContent returns the content of a task's live (highest, non-tombstone)
// revision.
func (h *harness) liveTaskContent(id int64) string {
	h.t.Helper()
	var best *ab2store.Message
	revs := h.taskRevisions(id)
	for i := range revs {
		if revs[i].Tombstone || revs[i].Revision == nil {
			continue
		}
		if best == nil || *revs[i].Revision > *best.Revision {
			best = &revs[i]
		}
	}
	if best == nil {
		h.t.Fatalf("task %d has no live revision", id)
	}
	return best.Content
}

// reclaimedTask creates a task in list "work", has pat claim it with a 1 s
// lease and runs the abandoned-task job after the lease lapses.
func (h *harness) reclaimedTask(subject string) int64 {
	h.t.Helper()
	ctx := context.Background()
	pat := h.as("pat")
	made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: subject})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := pat.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID, LeasedUntil: h.taskLeaseIn(time.Second)}); err != nil {
		h.t.Fatal(err)
	}
	h.clock.Advance(2 * time.Second)
	if n := h.runJob("abandoned tasks"); n != 1 {
		h.t.Fatalf("abandoned tasks reclaimed %d, want 1", n)
	}
	return made.ID
}

func TestTaskReclaimWritesMark(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		h.taskList("work", "pat", "alice", "sam")
		ctx := context.Background()
		id := h.reclaimedTask("first")
		now := h.core.NowMs()
		alice := h.as("alice")
		listed := func() ab2bus.TaskSummary {
			ts, err := alice.List(ctx, ab2bus.TaskListInput{Channel: "work"})
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range ts {
				if s.ID == id {
					return s
				}
			}
			t.Fatalf("task %d is not listed", id)
			return ab2bus.TaskSummary{}
		}
		got1, err := alice.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got1.ReclaimedFrom != "pat" || got1.ReclaimedAt != now {
			t.Fatalf("GetTask mark = %q, %d; want pat, %d", got1.ReclaimedFrom, got1.ReclaimedAt, now)
		}
		if s := listed(); s.ReclaimedFrom != "pat" || s.ReclaimedAt != now {
			t.Fatalf("List mark = %q, %d; want pat, %d", s.ReclaimedFrom, s.ReclaimedAt, now)
		}
		got := h.liveTaskContent(id)
		for _, want := range []string{`"reclaimed_from":"pat"`, fmt.Sprintf(`"reclaimed_at":%d`, now)} {
			if !strings.Contains(got, want) {
				t.Fatalf("live content %s lacks %s", got, want)
			}
		}
		revs := len(h.taskRevisions(id))
		h.clock.Advance(time.Second)
		if n := h.runJob("abandoned tasks"); n != 0 {
			t.Fatalf("second run reclaimed %d, want 0", n)
		}
		if after := h.liveTaskContent(id); after != got || len(h.taskRevisions(id)) != revs {
			t.Fatalf("second run changed the task: %s", after)
		}
		if _, err := h.as("sam").Claim(ctx, ab2bus.TaskClaimInput{ID: id}); err != nil {
			t.Fatal(err)
		}
		got2, err := alice.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got2.ReclaimedFrom != "" || got2.ReclaimedAt != 0 {
			t.Fatalf("GetTask after claim still marks %q, %d", got2.ReclaimedFrom, got2.ReclaimedAt)
		}
		if s := listed(); s.ReclaimedFrom != "" || s.ReclaimedAt != 0 {
			t.Fatalf("List after claim still marks %q, %d", s.ReclaimedFrom, s.ReclaimedAt)
		}
	})
}

func TestTaskReclaimedMarkPersists(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.taskList("work", "pat", "alice", "sam")
		alice, sam := h.as("alice"), h.as("sam")
		has := func(id int64) bool { return strings.Contains(h.liveTaskContent(id), `"reclaimed_from":"pat"`) }
		root, err := alice.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "root"})
		if err != nil {
			t.Fatal(err)
		}

		id := h.reclaimedTask("kept")
		if _, err := alice.Update(ctx, ab2bus.TaskPatch{ID: id, Subject: taskPtr("renamed")}); err != nil {
			t.Fatal(err)
		}
		if !has(id) {
			t.Fatal("a rename cleared the mark")
		}
		if _, err := alice.Update(ctx, ab2bus.TaskPatch{ID: id, Parent: taskPtr(root.ID)}); err != nil {
			t.Fatal(err)
		}
		if !has(id) {
			t.Fatal("a move cleared the mark")
		}
		if _, err := alice.Release(ctx, ab2bus.TaskReleaseInput{ID: id}); err != nil {
			t.Fatal(err)
		}
		if !has(id) {
			t.Fatal("a release cleared the mark")
		}
		if _, err := sam.Claim(ctx, ab2bus.TaskClaimInput{ID: id}); err != nil {
			t.Fatal(err)
		}
		if got := h.liveTaskContent(id); strings.Contains(got, "reclaimed_from") || strings.Contains(got, "reclaimed_at") {
			t.Fatalf("a claim left the mark: %s", got)
		}
		var snap []tasks.SnapshotTask
		if err := h.raw.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
			var err error
			snap, err = (tasks.Service{Core: h.core}).Snapshot(ctx, tx, ch, h.core.NowMs())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, s := range snap {
			if s.ID != id {
				continue
			}
			found = true
			if s.ReclaimedFrom != "" || s.ReclaimedAt != 0 {
				t.Fatalf("snapshot still marks %+v", s)
			}
		}
		if !found {
			t.Fatalf("task %d is not in the snapshot %+v", id, snap)
		}

		done := h.reclaimedTask("completed")
		if _, err := alice.Update(ctx, ab2bus.TaskPatch{ID: done, Status: taskPtr("completed")}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h.liveTaskContent(done), "reclaimed_from") {
			t.Fatal("completing left the mark")
		}

		assigned := h.reclaimedTask("assigned")
		if _, err := alice.Update(ctx, ab2bus.TaskPatch{ID: assigned, Owner: taskPtr("sam")}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h.liveTaskContent(assigned), "reclaimed_from") {
			t.Fatal("an assignment left the mark")
		}
		sums, err := alice.List(ctx, ab2bus.TaskListInput{Channel: "work"})
		if err != nil {
			t.Fatal(err)
		}
		found = false
		for _, s := range sums {
			if s.ID != assigned {
				continue
			}
			found = true
			if s.Owner != "sam" || s.ReclaimedFrom != "" || s.ReclaimedAt != 0 {
				t.Fatalf("list row %+v", s)
			}
		}
		if !found {
			t.Fatalf("task %d is not listed: %+v", assigned, sums)
		}
	})
}

func TestTaskReleaseSetsNoMark(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.taskList("work", "pat")
		pat := h.as("pat")
		made := mustTask(t)(pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "x"}))
		if _, err := pat.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := pat.Release(ctx, ab2bus.TaskReleaseInput{ID: made.ID}); err != nil {
			t.Fatal(err)
		}
		if got := h.liveTaskContent(made.ID); strings.Contains(got, "reclaimed") {
			t.Fatalf("release left a mark: %s", got)
		}
		if n := countTaskRevs(h.taskRevisions(made.ID), "reclaimed"); n != 0 {
			t.Fatalf("%d reclaimed revisions after a release", n)
		}
	})
}
