package bus

import (
	"context"
	"slices"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/core/statuses"
	"github.com/ericfitz/agentmesh/internal/core/tasks"
	"github.com/ericfitz/agentmesh/internal/dbtest"
	ab2store "github.com/ericfitz/agentmesh/internal/store"
)

func (h *harness) canvasTaskACL() []ab2store.ACLEntry {
	for _, n := range []string{"alice", "bob", "carl"} {
		h.ensureIdentity(n)
	}
	rw := []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}
	return []ab2store.ACLEntry{
		{Identity: "alice", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}},
		{Identity: "bob", Permissions: rw},
		{Identity: "carl", Permissions: rw},
	}
}

// liveStats reads the live memory rows' count and highest revision, and the
// channel's read mark.
func (h *harness) liveStats(ch ab2store.Channel) (n int, maxRev int64, lastRead int64) {
	h.t.Helper()
	if err := h.raw.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		ms, err := tx.LiveMemories(ctx, ch.ID)
		if err != nil {
			return err
		}
		for _, m := range ms {
			if m.Revision != nil && *m.Revision > maxRev {
				maxRev = *m.Revision
			}
		}
		n = len(ms)
		c, err := tx.Channel(ctx, ch.ID)
		lastRead = c.LastReadAt
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return n, maxRev, lastRead
}

func TestTaskSnapshotReadsWithoutWriting(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("job1-tasks", ab2store.KindTasks, "alice", h.canvasTaskACL())
		alice := h.as("alice")
		mk := func(in ab2bus.TaskCreateInput) int64 {
			in.Channel = "job1-tasks"
			task, err := alice.Create(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			return task.ID
		}
		id1 := mk(ab2bus.TaskCreateInput{Subject: "Root"})
		id2 := mk(ab2bus.TaskCreateInput{Subject: "Child", Parent: id1})
		id3 := mk(ab2bus.TaskCreateInput{Subject: "Grandchild", Parent: id2})
		id4 := mk(ab2bus.TaskCreateInput{Subject: "Great", Parent: id3})
		id5 := mk(ab2bus.TaskCreateInput{Subject: "Sibling", BlockedBy: []int64{id1}})

		h.session("bob", "")
		h.session("carl", "")
		if _, err := h.as("bob").Claim(ctx, ab2bus.TaskClaimInput{ID: id2, LeasedUntil: h.taskLeaseIn(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.as("carl").Claim(ctx, ab2bus.TaskClaimInput{ID: id3, LeasedUntil: h.taskLeaseIn(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(2 * time.Minute)

		n0, rev0, _ := h.liveStats(ch)
		var got []tasks.SnapshotTask
		if err := h.raw.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
			var err error
			got, err = (tasks.Service{Core: h.core}).Snapshot(ctx, tx, ch, h.core.NowMs())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var order []int64
		var depths []int
		by := map[int64]tasks.SnapshotTask{}
		for _, s := range got {
			order = append(order, s.ID)
			depths = append(depths, s.Depth)
			by[s.ID] = s
		}
		if !slices.Equal(order, []int64{id1, id2, id3, id4, id5}) || !slices.Equal(depths, []int{0, 1, 2, 3, 0}) {
			t.Fatalf("order %v depths %v", order, depths)
		}
		if !slices.Equal(by[id5].OpenBlockers, []int64{id1}) {
			t.Fatalf("blockers %v", by[id5].OpenBlockers)
		}
		if s := by[id2]; !s.Abandoned || !s.LeaseExpired || s.Assignee != "bob" || s.Parent != id1 {
			t.Fatalf("bob's task %+v", s)
		}
		if s := by[id3]; !s.Abandoned || s.LeaseExpired || s.Assignee != "carl" {
			t.Fatalf("carl's task %+v", s)
		}
		if by[id1].Abandoned {
			t.Fatalf("root %+v", by[id1])
		}

		n1, rev1, lastRead := h.liveStats(ch)
		if n1 != n0 || rev1 != rev0 || lastRead != 0 {
			t.Fatalf("snapshot wrote: rows %d->%d revision %d->%d last read %d", n0, n1, rev0, rev1, lastRead)
		}
		// The probe works: a List reclaims, so the revision grows.
		if _, err := alice.List(ctx, ab2bus.TaskListInput{Channel: "job1-tasks"}); err != nil {
			t.Fatal(err)
		}
		if _, rev2, _ := h.liveStats(ch); rev2 <= rev1 {
			t.Fatalf("List did not reclaim: revision %d -> %d", rev1, rev2)
		}
	})
}

func TestTaskSnapshotParentGoneIsRoot(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("job1-tasks", ab2store.KindTasks, "alice", h.canvasTaskACL())
		root, err := h.as("alice").Create(ctx, ab2bus.TaskCreateInput{Channel: "job1-tasks", Subject: "Root"})
		if err != nil {
			t.Fatal(err)
		}
		h.append(ch, "alice", "", `{"subject":"orphan","status":"pending","parent":999,"rank":"n"}`)
		var got []tasks.SnapshotTask
		if err := h.raw.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
			var err error
			got, err = (tasks.Service{Core: h.core}).Snapshot(ctx, tx, ch, h.core.NowMs())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].ID != root.ID || got[1].Subject != "orphan" || got[1].Depth != 0 || got[1].Parent != 0 {
			t.Fatalf("snapshot %+v", got)
		}
	})
}

func TestTaskSnapshotKind(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ch := h.channel("plain", ab2store.KindOrdinary, "alice", nil)
		err := h.raw.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := (tasks.Service{Core: h.core}).Snapshot(ctx, tx, ch, h.core.NowMs())
			return err
		})
		codeOf(t, err, ab2bus.CodeValidation)
	})
}

func TestLiveStatusesDerivesOffline(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		bs := h.session("bob", "")
		if _, err := h.as("bob").SetAgentStatus(ctx, ab2bus.SetAgentStatusInput{Session: bs.ID, Tag: ab2bus.StatusNeedsHuman, Text: "which style?", Problem: ""}); err != nil {
			t.Fatal(err)
		}
		cs := h.session("carl", "")
		if _, err := h.as("carl").SetAgentStatus(ctx, ab2bus.SetAgentStatusInput{Session: cs.ID, Tag: ab2bus.StatusImplementing, Text: "building", Problem: "docker daemon down"}); err != nil {
			t.Fatal(err)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.CloseSession(ctx, cs.ID, h.core.NowMs())
		})
		live := func() map[string]ab2bus.AgentStatus {
			var m map[string]ab2bus.AgentStatus
			if err := h.raw.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
				var err error
				m, err = (statuses.Service{Core: h.core}).LiveStatuses(ctx, tx, []string{"bob", "carl", "nobody", "bob"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if m == nil {
				t.Fatal("nil map")
			}
			return m
		}
		m := live()
		if len(m) != 1 || m["bob"].Tag != ab2bus.StatusNeedsHuman || m["bob"].Text != "which style?" {
			t.Fatalf("live = %+v", m)
		}
		h.clock.Advance(10 * time.Minute)
		if m := live(); len(m) != 0 {
			t.Fatalf("after the lease: %+v", m)
		}
	})
}
