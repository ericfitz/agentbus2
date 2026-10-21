package bus

import (
	"context"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/tasks"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
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
		// Another identity's update is refused by the owner guard.
		_, err = kim.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Description: taskPtr("mine now")})
		codeOf(t, err, ab2bus.CodeConflict)
		_, err = kim.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID})
		codeOf(t, err, ab2bus.CodeConflict)
		// Force overrides and is recorded.
		forced, err := kim.Update(ctx, ab2bus.TaskPatch{ID: made.ID, Description: taskPtr("by force"), Force: true})
		if err != nil || forced.Task.Description != "by force" || forced.Task.UpdatedBy != "kim" {
			t.Fatalf("forced = %+v, %v", forced, err)
		}
		revs := h.taskRevisions(made.ID)
		if last := revs[len(revs)-1]; last.Type != "forced" || last.Tombstone {
			t.Fatalf("last revision = %+v, want a live forced row", last)
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
		a, _ := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "a"})
		b, _ := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "b"})
		c, _ := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "c", Parent: a.ID})
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
		made, _ := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"})
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
	// pending Eric: Milestone 3 item 13: owner liveness is a live session row.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("work", ab2store.KindTasks, "pat", nil)
		pat, kim := h.as("pat"), h.as("kim")
		h.session("kim", "kim")
		made, _ := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"})
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
		second, _ := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "second"})
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
		made, _ := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"})
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
		a, _ := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "a"})
		b, _ := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "b"})
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
		parent, _ := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "parent"})
		child, _ := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "child", Parent: parent.ID})
		_, err := pat.Update(ctx, ab2bus.TaskPatch{ID: parent.ID, Delete: true})
		codeOf(t, err, ab2bus.CodeConflict)
		res, err := pat.Update(ctx, ab2bus.TaskPatch{ID: child.ID, Delete: true})
		if err != nil || !res.Deleted {
			t.Fatalf("delete = %+v, %v", res, err)
		}
		_, err = pat.GetTask(ctx, child.ID)
		codeOf(t, err, ab2bus.CodeNotFound)

		// Forced delete of another identity's in_progress task leaves a tombstoned forced row.
		if _, err := pat.Claim(ctx, ab2bus.TaskClaimInput{ID: parent.ID}); err != nil {
			t.Fatal(err)
		}
		_, err = kim.Update(ctx, ab2bus.TaskPatch{ID: parent.ID, Delete: true})
		codeOf(t, err, ab2bus.CodeConflict)
		res, err = kim.Update(ctx, ab2bus.TaskPatch{ID: parent.ID, Delete: true, Force: true})
		if err != nil || !res.Deleted {
			t.Fatalf("forced delete = %+v, %v", res, err)
		}
		revs := h.taskRevisions(parent.ID)
		last := revs[len(revs)-1]
		if last.Type != "forced" || last.Sender != "kim" || !last.Tombstone {
			t.Fatalf("last revision = %+v, want a tombstoned forced row naming kim", last)
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
