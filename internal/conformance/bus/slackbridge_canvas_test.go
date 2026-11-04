package bus

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/dbtest"
	"github.com/ericfitz/agentmesh/internal/slackbridge"
	ab2store "github.com/ericfitz/agentmesh/internal/store"
)

var canvasHumans = []string{slAlice, slGina, slXav, slNone, siKim, siRon, siCat, siDee, siBotU, siUla}

func (f *inFix) canvasSnap(scope string) (slackbridge.CanvasSnapshot, error) {
	f.h.t.Helper()
	return f.svc.CanvasSnapshot(context.Background(), f.bridge(), scope)
}

func (f *inFix) mustSnap() slackbridge.CanvasSnapshot {
	f.h.t.Helper()
	s, err := f.canvasSnap("job1")
	if err != nil {
		f.h.t.Fatalf("CanvasSnapshot: %v", err)
	}
	return s
}

func canvasListNames(s slackbridge.CanvasSnapshot) (tl, mc []string) {
	for _, l := range s.TaskLists {
		tl = append(tl, l.Channel)
	}
	for _, m := range s.MemoryChannels {
		mc = append(mc, m.Channel)
	}
	return tl, mc
}

func TestCanvasSnapshotVisibility(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newInFixOpts(t, e, harnessOptions{}, nil)
		R, W, A := ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin
		readAll := []ab2store.ACLEntry{slEntry("job1-coder", R, W, A), slEntry(ab2store.Admins, A)}
		for _, n := range canvasHumans {
			readAll = append(readAll, slEntry(n, R))
		}
		f.slKindChannel("job1-tasks", "job1", ab2store.KindTasks, readAll)
		f.slKindChannel("job1-notes", "job1", ab2store.KindMemory, []ab2store.ACLEntry{slEntry("job1-coder", R, W, A), slEntry(ab2store.Everyone, R), slEntry(ab2store.Admins, A)})
		f.slKindChannel("job1-private", "job1", ab2store.KindTasks, []ab2store.ACLEntry{slEntry("job1-coder", R, W, A), slEntry(slAlice, R), slEntry(ab2store.Admins, A)})
		f.slKindChannel("job1-other", "job1", ab2store.KindMemory, []ab2store.ACLEntry{slEntry("job1-coder", R, W, A), slEntry(ab2store.Admins, A)})

		s := f.mustSnap()
		tl, mc := canvasListNames(s)
		if s.ScopeName != "job1" || !slices.Equal(tl, []string{"job1-tasks"}) || !slices.Equal(mc, []string{"job1-notes"}) {
			t.Fatalf("snapshot scope %q tasks %v memories %v", s.ScopeName, tl, mc)
		}
		if !s.At.Equal(f.h.core.Now().UTC()) {
			t.Errorf("At = %v", s.At)
		}
		if _, err := f.canvasSnap("nope"); !hasCode(err, ab2bus.CodeNotFound) {
			t.Errorf("unknown scope: %v", err)
		}

		t.Run("ordinary-ignored", func(t *testing.T) {
			for _, n := range append(tl, mc...) {
				if n == "job1-chat" {
					t.Errorf("ordinary channel listed")
				}
			}
		})
		t.Run("zero-humans", func(t *testing.T) {
			x := f
			solo := x.slChannel("job1-solo", "job1", []ab2store.ACLEntry{slEntry("job1-coder", R, W, A), slEntry(ab2store.Admins, A)})
			if _, err := x.create("job1-solo", "job1-solo"); err != nil {
				t.Fatalf("Create: %v", err)
			}
			b, ok := x.bridgeRow(solo)
			if !ok {
				t.Fatal("no bridge row")
			}
			got, err := x.svc.CanvasSnapshot(context.Background(), b, "job1")
			if err != nil {
				t.Fatal(err)
			}
			if len(got.TaskLists) != 0 || len(got.MemoryChannels) != 0 {
				t.Errorf("zero-human bridge shows %+v", got)
			}
		})
	})
}

func hasCode(err error, c ab2bus.Code) bool {
	var be *ab2bus.Error
	return errors.As(err, &be) && be.Code == c
}

func canvasTasksACL() []ab2store.ACLEntry {
	R, W, A := ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin
	acl := []ab2store.ACLEntry{slEntry("job1-coder", R, W, A), slEntry("job1-b", R, W), slEntry(ab2store.Admins, A)}
	for _, n := range canvasHumans {
		acl = append(acl, slEntry(n, R))
	}
	return acl
}

func TestCanvasSnapshotTasksAndStatuses(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newInFixOpts(t, e, harnessOptions{}, nil)
		ctx := context.Background()
		h := f.h
		coder := h.localAs("job1-coder")
		b := h.scopedIdentity("job1-b", "job1")
		ch := f.slKindChannel("job1-tasks", "job1", ab2store.KindTasks, canvasTasksACL())
		mk := func(in ab2bus.TaskCreateInput) int64 {
			in.Channel = "job1-tasks"
			task, err := coder.Create(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			return task.ID
		}
		id1 := mk(ab2bus.TaskCreateInput{Subject: "Root"})
		id2 := mk(ab2bus.TaskCreateInput{Subject: "child of 1", Parent: id1})
		id3 := mk(ab2bus.TaskCreateInput{Subject: "child of 2", Parent: id2})
		id4 := mk(ab2bus.TaskCreateInput{Subject: "root blocked by 1", BlockedBy: []int64{id1}})

		s1 := h.session("job1-coder", "")
		if _, err := coder.Claim(ctx, ab2bus.TaskClaimInput{ID: id2}); err != nil {
			t.Fatal(err)
		}
		if _, err := coder.SetAgentStatus(ctx, ab2bus.SetAgentStatusInput{Session: s1.ID, Tag: ab2bus.StatusNeedsHuman, Text: "which style?"}); err != nil {
			t.Fatal(err)
		}
		sb := h.session("job1-b", "")
		leaseAt := h.taskLeaseIn(time.Minute)
		if _, err := b.Claim(ctx, ab2bus.TaskClaimInput{ID: id3, LeasedUntil: leaseAt}); err != nil {
			t.Fatal(err)
		}
		if _, err := b.SetAgentStatus(ctx, ab2bus.SetAgentStatusInput{Session: sb.ID, Tag: ab2bus.StatusImplementing, Text: "working", Problem: "docker daemon down"}); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(2 * time.Minute)
		h.session("job1-coder", "")

		tasksOf := func() map[int64]slackbridge.CanvasTask {
			s := f.mustSnap()
			if len(s.TaskLists) != 1 || s.TaskLists[0].Channel != "job1-tasks" {
				t.Fatalf("lists %+v", s.TaskLists)
			}
			m := map[int64]slackbridge.CanvasTask{}
			for _, ct := range s.TaskLists[0].Tasks {
				m[ct.ID] = ct
			}
			return m
		}
		_, rev0, _ := h.liveStats(ch)
		by := tasksOf()
		if st := by[id2].OwnerStatus; st == nil || *st != (slackbridge.CanvasOwnerStatus{Tag: "needs-human", Text: "which style?"}) {
			t.Errorf("task 2 owner status %+v", st)
		}
		if c := by[id3]; !c.Stalled || !c.LeaseExpiredAt.Equal(time.UnixMilli(leaseAt).UTC()) || c.OwnerStatus != nil || c.Depth != 2 || c.Assignee != "job1-b" {
			t.Errorf("task 3 %+v", c)
		}
		if !slices.Equal(by[id4].OpenBlockers, []int64{id1}) {
			t.Errorf("task 4 blockers %v", by[id4].OpenBlockers)
		}
		if by[id1].OwnerStatus != nil || by[id1].Assignee != "" {
			t.Errorf("task 1 %+v", by[id1])
		}

		done := "completed"
		if _, err := coder.Update(ctx, ab2taskPatch(id2, &done)); err != nil {
			t.Fatal(err)
		}
		if st := tasksOf()[id2].OwnerStatus; st != nil {
			t.Errorf("completed task shows status %+v", st)
		}

		// Read-only: no read mark, and a snapshot leaves no revision.
		_, rev1, lastRead := h.liveStats(ch)
		h.clock.Advance(time.Minute)
		tasksOf()
		_, rev2, lastRead2 := h.liveStats(ch)
		if lastRead != 0 || lastRead2 != 0 || rev2 != rev1 || rev1 < rev0 {
			t.Errorf("snapshot wrote: revision %d -> %d -> %d, last read %d/%d", rev0, rev1, rev2, lastRead, lastRead2)
		}
	})
}

func ab2taskPatch(id int64, status *string) ab2bus.TaskPatch {
	return ab2bus.TaskPatch{ID: id, Status: status}
}

func TestCanvasSnapshotMemories(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newInFixOpts(t, e, harnessOptions{}, nil)
		ctx := context.Background()
		h := f.h
		R, W, A := ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin
		f.slKindChannel("job1-notes", "job1", ab2store.KindMemory, []ab2store.ACLEntry{slEntry("job1-coder", R, W, A), slEntry(ab2store.Everyone, R), slEntry(ab2store.Admins, A)})
		coder := h.localAs("job1-coder")
		send := func(in ab2bus.SendInput) int64 {
			in.Channel = "job1-notes"
			r, err := coder.Send(ctx, in)
			if err != nil || r.MemoryID == nil {
				t.Fatalf("Send: %v %+v", err, r)
			}
			return *r.MemoryID
		}
		first := send(ab2bus.SendInput{Subject: "Postgres needs TCP keepalive", Type: "fact", Content: "c1", Tags: []string{"area:db"}})
		second := send(ab2bus.SendInput{Content: "c2"})
		third := send(ab2bus.SendInput{Content: "c3"})
		h.clock.Advance(time.Minute)
		editAt := h.clock.Now().UTC()
		if _, err := coder.Edit(ctx, ab2bus.EditMemoryInput{ID: first, Content: memStrp("c1 edited")}); err != nil {
			t.Fatal(err)
		}
		if err := coder.Delete(ctx, ab2bus.DeleteMemoryInput{ID: third}); err != nil {
			t.Fatal(err)
		}
		s := f.mustSnap()
		if len(s.MemoryChannels) != 1 || s.MemoryChannels[0].Channel != "job1-notes" {
			t.Fatalf("memory channels %+v", s.MemoryChannels)
		}
		ms := s.MemoryChannels[0].Memories
		if len(ms) != 2 || ms[0].ID != first || ms[1].ID != second {
			t.Fatalf("memories %+v", ms)
		}
		m := ms[0]
		if !m.EditedAt.Equal(editAt) || m.Sender != "job1-coder" || !slices.Equal(m.Tags, []string{"area:db"}) ||
			m.Subject != "Postgres needs TCP keepalive" || m.Type != "fact" || m.Content != "c1 edited" {
			t.Errorf("edited memory %+v", m)
		}
	})
}
