package bus

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/dbtest"
	"github.com/ericfitz/agentmesh/internal/slackapi"
	"github.com/ericfitz/agentmesh/internal/slackapi/slackfake"
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

		// Every human must read: dropping the first or the last human hides the list.
		partial := func(drop int) []ab2store.ACLEntry {
			acl := []ab2store.ACLEntry{slEntry("job1-coder", R, W, A), slEntry(ab2store.Admins, A)}
			for i, n := range canvasHumans {
				if i != drop {
					acl = append(acl, slEntry(n, R))
				}
			}
			return acl
		}
		f.slKindChannel("job1-nolast", "job1", ab2store.KindTasks, partial(len(canvasHumans)-1))
		f.slKindChannel("job1-nofirst", "job1", ab2store.KindTasks, partial(0))
		f.slKindChannel("job1-nomid", "job1", ab2store.KindMemory, partial(4))

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

		// The reclaim leaves its mark, and the task is no longer abandoned.
		jobNow := h.clock.Now().UnixMilli()
		if n := h.runJob("abandoned tasks"); n != 1 { // task 3 only: task 2 is completed
			t.Fatalf("abandoned tasks job reclaimed %d, want 1", n)
		}
		if c := tasksOf()[id3]; c.ReclaimedFrom != "job1-b" || !c.ReclaimedAt.Equal(time.UnixMilli(jobNow).UTC()) || c.Stalled || c.Assignee != "" {
			t.Errorf("task 3 after the reclaim %+v (job at %d)", c, jobNow)
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

// TestCanvasSnapshotUnknownKindUnreachable pins the invariant behind the
// snapshot's error branch for an unknown kind: the Store refuses to hold a
// channel of any kind but ordinary, memory and tasks, so the branch is
// defensive and cannot be reached through it.
func TestCanvasSnapshotUnknownKindUnreachable(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newInFixOpts(t, e, harnessOptions{}, nil)
		err := f.h.raw.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			seq, err := tx.MaxSeq(ctx)
			if err != nil {
				return err
			}
			_, err = tx.CreateChannel(ctx, ab2store.ChannelCreate{
				Name: "job1-odd", Kind: ab2store.Kind("bogus"), Creator: "job1-coder", Scope: "job1", CreatedSeq: seq, CreatedAt: f.h.core.NowMs(),
			})
			return err
		})
		if !errors.Is(err, ab2store.ErrInvalid) {
			t.Fatalf("CreateChannel with an unknown kind: %v", err)
		}
	})
}

// canvasFix is a bridged job1 with a visible task list (one task whose
// subject is a sentinel) and a visible memory channel (one memory whose
// content is a sentinel); the publisher must publish both and log neither.
func newCanvasFix(t *testing.T, e dbtest.Engine, enabled bool) *inFix {
	t.Helper()
	f := newInFixOpts(t, e, harnessOptions{}, func(c *slackbridge.Config) { c.CanvasEnabled = enabled })
	f.canvasSetup()
	return f
}

// canvasSetup adds job1-b, a visible tasks channel with one task and a visible
// memory channel with one memory; it answers the tasks channel.
func (f *inFix) canvasSetup() ab2store.Channel {
	f.h.t.Helper()
	R, W, A := ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin
	f.h.scopedIdentity("job1-b", "job1")
	tasks := f.slKindChannel("job1-tasks", "job1", ab2store.KindTasks, canvasTasksACL())
	f.slKindChannel("job1-notes", "job1", ab2store.KindMemory, []ab2store.ACLEntry{slEntry("job1-coder", R, W, A), slEntry(ab2store.Everyone, R), slEntry(ab2store.Admins, A)})
	f.addCanvasTask("SECRET-SUBJECT first")
	if _, err := f.h.localAs("job1-coder").Send(context.Background(), ab2bus.SendInput{Channel: "job1-notes", Subject: "note", Content: "SECRET-CONTENT"}); err != nil {
		f.h.t.Fatalf("memory: %v", err)
	}
	return tasks
}

func (f *inFix) addCanvasTask(subject string) {
	f.h.t.Helper()
	if _, err := f.h.localAs("job1-coder").Create(context.Background(), ab2bus.TaskCreateInput{Channel: "job1-tasks", Subject: subject}); err != nil {
		f.h.t.Fatalf("task: %v", err)
	}
}

func (f *inFix) pub() (time.Time, error) {
	f.h.t.Helper()
	return f.svc.PublishCanvas(context.Background(), f.bridge(), "job1")
}

func (f *inFix) mustPub() {
	f.h.t.Helper()
	if at, err := f.pub(); err != nil || !at.IsZero() {
		f.h.t.Fatalf("PublishCanvas = %v, %v", at, err)
	}
}

// canvasOf is the one canvas of the bridged Slack channel (read without
// counting a call).
func canvasOf(f *inFix) slackfake.FakeCanvas {
	f.h.t.Helper()
	var found []slackfake.FakeCanvas
	for i := 1; i < 40; i++ {
		if c, ok := f.fake.Canvas(fmt.Sprintf("F%06d", i)); ok && c.ChannelID == f.sl {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		f.h.t.Fatalf("the Slack channel has %d canvases", len(found))
	}
	return found[0]
}

func (f *inFix) canvasCalls() map[string]int {
	out := map[string]int{}
	for _, m := range []string{"CreateChannelCanvas", "ChannelCanvases", "CanvasContains", "ReplaceCanvas", "SetCanvasChannelAccess"} {
		out[m] = f.fake.Calls(m)
	}
	return out
}

func inWindow(t *testing.T, at time.Time, d time.Duration) {
	t.Helper()
	want := time.Now().Add(d)
	if at.Before(want.Add(-time.Second)) || at.After(want.Add(time.Second)) {
		t.Errorf("retryAt = %v, want about %v", at, want)
	}
}

func apiCode(err error) string {
	var ae *slackapi.APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestCanvasPublishCreatesAndReplaces(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.mustPub()
		if f.fake.Calls("CreateChannelCanvas") != 1 || f.fake.Calls("SetCanvasChannelAccess") != 1 {
			t.Fatalf("calls %v", f.canvasCalls())
		}
		c := canvasOf(f)
		if c.Label != "Status" || c.Access[f.sl] != "read" ||
			!strings.Contains(c.Markdown, "# job1 · tasks and memory") || !strings.Contains(c.Markdown, "changes go through agentmesh") ||
			!strings.Contains(c.Markdown, "SECRET-SUBJECT first") || !strings.Contains(c.Markdown, "SECRET-CONTENT") {
			t.Fatalf("canvas %+v", c)
		}
		before := f.canvasCalls()
		f.h.clock.Advance(time.Hour)
		f.mustPub()
		if got := f.canvasCalls(); !maps.Equal(before, got) {
			t.Errorf("an unchanged rebuild called Slack: %v -> %v", before, got)
		}
		f.addCanvasTask("SECRET-SUBJECT second")
		f.mustPub()
		if f.fake.Calls("ReplaceCanvas") != 1 || !strings.Contains(canvasOf(f).Markdown, "SECRET-SUBJECT second") {
			t.Errorf("calls %v, canvas %q", f.canvasCalls(), canvasOf(f).Markdown)
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasAccessFailureRetried(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.fake.FailNext("SetCanvasChannelAccess", &slackapi.APIError{Method: "canvases.access.set", Code: "internal_error"})
		at, err := f.pub()
		if apiCode(err) != "internal_error" {
			t.Fatalf("first pub: %v", err)
		}
		inWindow(t, at, 30*time.Second)
		if f.fake.Calls("CreateChannelCanvas") != 1 || len(canvasOf(f).Access) != 0 {
			t.Fatalf("calls %v, access %v", f.canvasCalls(), canvasOf(f).Access)
		}
		before := f.canvasCalls()
		f.mustPub()
		after := f.canvasCalls()
		if after["SetCanvasChannelAccess"] != before["SetCanvasChannelAccess"]+1 {
			t.Errorf("access calls %v -> %v", before, after)
		}
		for _, m := range []string{"CreateChannelCanvas", "ChannelCanvases", "CanvasContains", "ReplaceCanvas"} {
			if after[m] != before[m] {
				t.Errorf("%s called again: %v -> %v", m, before, after)
			}
		}
		if canvasOf(f).Access[f.sl] != "read" {
			t.Errorf("access %v", canvasOf(f).Access)
		}
		f.mustPub()
		if got := f.canvasCalls(); !maps.Equal(after, got) {
			t.Errorf("third pub called Slack: %v -> %v", after, got)
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasAdoptsByMarker(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		id := f.fake.AddCanvas(f.sl, "Old title", "# x\n_Updated ... changes go through agentmesh_\n")
		f.mustPub()
		c, _ := f.fake.Canvas(id)
		if f.fake.Calls("CreateChannelCanvas") != 0 || f.fake.Calls("CanvasContains") == 0 || f.fake.Calls("ReplaceCanvas") != 1 ||
			c.Access[f.sl] != "read" || !strings.Contains(c.Markdown, "# job1 · tasks and memory") {
			t.Errorf("calls %v, canvas %+v", f.canvasCalls(), c)
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasFreeTierSecondCreateAdopts(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.fake.AddCanvas(f.sl, "Status", "# x\n_changes go through agentmesh_\n")
		f.fake.EmptyTabsNext(1)
		f.mustPub()
		if f.fake.Calls("CreateChannelCanvas") != 1 || f.fake.Calls("ChannelCanvases") != 2 || f.fake.Calls("ReplaceCanvas") != 1 {
			t.Errorf("calls %v", f.canvasCalls())
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasRestartAdopts(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.mustPub()
		svc := f.newService(f.h, func(c *slackbridge.Config) { c.CanvasEnabled = true })
		if at, err := svc.PublishCanvas(context.Background(), f.bridge(), "job1"); err != nil || !at.IsZero() {
			t.Fatalf("PublishCanvas = %v, %v", at, err)
		}
		if f.fake.Calls("CreateChannelCanvas") != 1 || f.fake.Calls("SetCanvasChannelAccess") != 2 || canvasOf(f).Access[f.sl] != "read" {
			t.Errorf("calls %v", f.canvasCalls())
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasLookupFailureRetries(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.fake.FailNext("ChannelCanvases", &slackapi.APIError{Method: "conversations.info", Code: "internal_error"})
		at, err := f.pub()
		if apiCode(err) != "internal_error" || f.fake.Calls("CreateChannelCanvas") != 0 {
			t.Fatalf("pub: %v, calls %v", err, f.canvasCalls())
		}
		inWindow(t, at, 30*time.Second)
		f.mustPub()
		if f.fake.Calls("CreateChannelCanvas") != 1 {
			t.Errorf("calls %v", f.canvasCalls())
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasForeignTabDisables(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.fake.AddCanvas(f.sl, "Notes", "# a human's canvas")
		for range 3 {
			f.mustPub()
		}
		if f.fake.Calls("ReplaceCanvas") != 0 || f.fake.Calls("CreateChannelCanvas") != 0 || f.fake.Calls("ChannelCanvases") != 1 {
			t.Errorf("calls %v", f.canvasCalls())
		}
		if n := strings.Count(f.logs.String(), "belongs to someone else"); n != 1 || !strings.Contains(f.logs.String(), f.sl) {
			t.Errorf("warnings %d:\n%s", n, f.logs.String())
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasRecreatesAfterNotFound(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.mustPub()
		f.fake.DeleteCanvasByHand(canvasOf(f).ID)
		f.addCanvasTask("SECRET-SUBJECT second")
		at, err := f.pub()
		if apiCode(err) != "canvas_not_found" {
			t.Fatalf("pub: %v", err)
		}
		inWindow(t, at, 30*time.Second)
		f.mustPub()
		if f.fake.Calls("CreateChannelCanvas") != 2 || !strings.Contains(canvasOf(f).Markdown, "SECRET-SUBJECT second") {
			t.Errorf("calls %v", f.canvasCalls())
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasRateLimitWaits(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.mustPub()
		f.addCanvasTask("SECRET-SUBJECT second")
		f.fake.RateLimit("ReplaceCanvas", 1, 3*time.Second)
		at, err := f.pub()
		var rl *slackapi.RateLimitedError
		if !errors.As(err, &rl) {
			t.Fatalf("pub: %v", err)
		}
		inWindow(t, at, 3*time.Second)
		f.mustPub()
		if !strings.Contains(canvasOf(f).Markdown, "SECRET-SUBJECT second") {
			t.Errorf("canvas %q", canvasOf(f).Markdown)
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasMissingScopeWarnsOnce(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.fake.FailNext("CreateChannelCanvas", &slackapi.APIError{Method: "conversations.canvases.create", Code: "missing_scope"})
		for range 3 {
			f.mustPub()
		}
		log := f.logs.String()
		if strings.Count(log, "canvases:read") != 1 || !strings.Contains(log, "canvases:write") || f.fake.Calls("CreateChannelCanvas") != 1 {
			t.Errorf("calls %v, log:\n%s", f.canvasCalls(), log)
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasPlanUnavailableWarnsOnce(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.fake.FailNext("CreateChannelCanvas", &slackapi.APIError{Method: "conversations.canvases.create", Code: "team_tier_cannot_create_channel_canvases"})
		for range 3 {
			f.mustPub()
		}
		if n := strings.Count(f.logs.String(), "canvas unavailable on this Slack plan"); n != 1 || f.fake.Calls("CreateChannelCanvas") != 1 {
			t.Errorf("warnings %d, calls %v", n, f.canvasCalls())
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasArchivedInSlack(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.mustPub()
		f.addCanvasTask("SECRET-SUBJECT second")
		f.fake.FailNext("ReplaceCanvas", &slackapi.APIError{Method: "canvases.edit", Code: "is_archived"})
		if at, err := f.pub(); err != nil || !at.IsZero() {
			t.Fatalf("pub = %v, %v", at, err)
		}
		if f.bridge().State != ab2store.BridgeArchived {
			t.Fatalf("bridge state %v", f.bridge().State)
		}
		before := f.canvasCalls()
		f.mustPub()
		if got := f.canvasCalls(); !maps.Equal(before, got) {
			t.Errorf("an archived bridge called Slack: %v -> %v", before, got)
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasChannelGoneOnCreateArchives(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.fake.FailNext("ChannelCanvases", &slackapi.APIError{Method: "conversations.info", Code: "channel_not_found"})
		if at, err := f.pub(); err != nil || !at.IsZero() {
			t.Fatalf("pub = %v, %v", at, err)
		}
		if f.bridge().State != ab2store.BridgeArchived {
			t.Errorf("bridge state %v", f.bridge().State)
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasUnknownCodeDisables(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.mustPub()
		f.addCanvasTask("SECRET-SUBJECT second")
		f.fake.FailNext("ReplaceCanvas", &slackapi.APIError{Method: "canvases.edit", Code: "invalid_arguments"})
		f.mustPub()
		before := f.canvasCalls()
		f.addCanvasTask("SECRET-SUBJECT third")
		f.mustPub()
		if got := f.canvasCalls(); !maps.Equal(before, got) {
			t.Errorf("a disabled bridge called Slack: %v -> %v", before, got)
		}
		if n := strings.Count(f.logs.String(), "invalid_arguments"); n != 1 {
			t.Errorf("warnings %d:\n%s", n, f.logs.String())
		}
		f.noSecretsLogged(t)
	})
}

// TestCanvasMarkerInMemoryIsAdopted: a canvas of someone else that quotes the
// marker is adopted (the marker is the only test: spec 6.3).
func TestCanvasMarkerInMemoryIsAdopted(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.fake.AddCanvas(f.sl, "Notes", "# notes\nsomeone wrote: changes go through agentmesh\n")
		f.mustPub()
		if f.fake.Calls("CreateChannelCanvas") != 0 || f.fake.Calls("ReplaceCanvas") != 1 {
			t.Errorf("calls %v", f.canvasCalls())
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasDisabledMakesNoCall(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, false)
		f.mustPub()
		for m, n := range f.canvasCalls() {
			if n != 0 {
				t.Errorf("%s called %d times", m, n)
			}
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasNotInChannelArchives(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.mustPub()
		f.addCanvasTask("SECRET-SUBJECT second")
		nic := func(m string) error { return &slackapi.APIError{Method: m, Code: "not_in_channel"} }
		f.fake.FailNext("ReplaceCanvas", nic("canvases.edit"))
		f.fake.FailNext("Post", nic("chat.postMessage"))
		f.fake.FailNext("Archive", nic("conversations.archive"))
		if at, err := f.pub(); err != nil || !at.IsZero() {
			t.Fatalf("pub = %v, %v", at, err)
		}
		if f.bridge().State != ab2store.BridgeArchived {
			t.Fatalf("bridge state %v", f.bridge().State)
		}
		if n := strings.Count(f.logs.String(), "not in the Slack channel; marking the bridge archived"); n == 0 {
			t.Errorf("no not-in-channel warning:\n%s", f.logs.String())
		}
		before := f.canvasCalls()
		f.mustPub()
		if got := f.canvasCalls(); !maps.Equal(before, got) {
			t.Errorf("an archived bridge called Slack: %v -> %v", before, got)
		}
		f.noSecretsLogged(t)
	})
}

// A create that says a canvas exists is followed by one more lookup, never by
// a second create.
func TestCanvasSecondLookupEmptyNeverCreatesTwice(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.fake.AddCanvas(f.sl, "Status", "# x\n_changes go through agentmesh_\n")
		f.fake.EmptyTabsNext(2)
		at, err := f.pub()
		if err == nil || at.IsZero() {
			t.Fatalf("pub = %v, %v", at, err)
		}
		inWindow(t, at, 30*time.Second)
		if f.fake.Calls("CreateChannelCanvas") != 1 || f.fake.Calls("ChannelCanvases") != 2 {
			t.Errorf("calls %v", f.canvasCalls())
		}
		f.mustPub() // the next window adopts
		if f.fake.Calls("CreateChannelCanvas") != 1 || f.fake.Calls("ReplaceCanvas") != 1 {
			t.Errorf("calls %v", f.canvasCalls())
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasCredentialCodeWarnsOnce(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.mustPub()
		f.addCanvasTask("SECRET-SUBJECT second")
		for i := 0; i < 2; i++ {
			f.fake.FailNext("ReplaceCanvas", &slackapi.APIError{Method: "canvases.edit", Code: "invalid_auth"})
			at, err := f.pub()
			if apiCode(err) != "invalid_auth" {
				t.Fatalf("pass %d: %v", i, err)
			}
			inWindow(t, at, 30*time.Second)
		}
		if n := strings.Count(f.logs.String(), "credential error"); n != 1 {
			t.Errorf("warnings %d:\n%s", n, f.logs.String())
		}
		f.mustPub()
		if !strings.Contains(canvasOf(f).Markdown, "SECRET-SUBJECT second") {
			t.Errorf("canvas not replaced")
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasChannelNotFoundOnEditRecreates(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.mustPub()
		f.addCanvasTask("SECRET-SUBJECT second")
		f.fake.FailNext("ReplaceCanvas", &slackapi.APIError{Method: "canvases.edit", Code: "channel_not_found"})
		at, err := f.pub()
		if apiCode(err) != "channel_not_found" || at.IsZero() {
			t.Fatalf("pub = %v, %v", at, err)
		}
		inWindow(t, at, 30*time.Second)
		if f.bridge().State != ab2store.BridgeLive {
			t.Fatalf("bridge state %v", f.bridge().State)
		}
		before := f.fake.Calls("ChannelCanvases")
		f.mustPub()
		if f.fake.Calls("ChannelCanvases") != before+1 || !strings.Contains(canvasOf(f).Markdown, "SECRET-SUBJECT second") {
			t.Errorf("calls %v", f.canvasCalls())
		}
		f.noSecretsLogged(t)
	})
}

func TestCanvasTransportErrorRetries(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		f := newCanvasFix(t, e, true)
		f.fake.FailNext("CreateChannelCanvas", errors.New("connection reset"))
		at, err := f.pub()
		if err == nil || at.IsZero() {
			t.Fatalf("pub = %v, %v", at, err)
		}
		inWindow(t, at, 30*time.Second)
		f.mustPub()
		if f.fake.Calls("CreateChannelCanvas") != 2 {
			t.Errorf("calls %v", f.canvasCalls())
		}
		f.noSecretsLogged(t)
	})
}

// canvasRunFix is a bridged job1 with the canvas on, a 20 ms window and the
// canvas setup; hourly true leaves only the path under test to mark (the
// session-start pass still runs once).
func canvasRunFix(t *testing.T, e dbtest.Engine, hourly bool, coalesce time.Duration) (*inFix, ab2store.Channel) {
	t.Helper()
	f := newOutFix(t, e, func(c *slackbridge.Config) {
		c.CanvasEnabled = true
		c.CanvasCoalesce = coalesce
		if hourly {
			c.LifecycleInterval = time.Hour
		}
	})
	return f, f.canvasSetup()
}

func (f *inFix) awaitCanvas(t *testing.T) {
	t.Helper()
	waitFor(t, 10*time.Second, "the canvas", func() bool {
		return f.fake.Calls("CreateChannelCanvas") == 1 && f.fake.Calls("SetCanvasChannelAccess") >= 1
	})
}

func canvasMarkdownHas(f *inFix, sub string) func() bool {
	return func() bool {
		for i := 1; i < 40; i++ {
			if c, ok := f.fake.Canvas(fmt.Sprintf("F%06d", i)); ok && c.ChannelID == f.sl {
				return strings.Contains(c.Markdown, sub)
			}
		}
		return false
	}
}

func canvasCfg(c *slackbridge.Config) {
	c.CanvasEnabled = true
	c.CanvasCoalesce = 20 * time.Millisecond
}

func TestSlackBridgeCanvasRunner(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		t.Run("publishes-after-start", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, false, 20*time.Millisecond)
			runBridge(t, f.svc)
			waitFor(t, 10*time.Second, "the canvas", func() bool { return f.fake.Calls("CreateChannelCanvas") == 1 })
			waitFor(t, 10*time.Second, "the label", func() bool { return f.fake.Calls("SetCanvasChannelAccess") >= 1 })
			if c := canvasOf(f); c.Label != "Status" {
				t.Errorf("label %q", c.Label)
			}
		})

		t.Run("hint-rebuilds-within-window", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, true, 300*time.Millisecond)
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			f.addCanvasTask("SECRET-SUBJECT second")
			time.Sleep(10 * time.Millisecond)
			f.addCanvasTask("SECRET-SUBJECT third")
			waitFor(t, 10*time.Second, "the replace", func() bool { return f.fake.Calls("ReplaceCanvas") == 1 })
			time.Sleep(700 * time.Millisecond)
			md := canvasOf(f).Markdown
			if f.fake.Calls("ReplaceCanvas") != 1 || !strings.Contains(md, "second") || !strings.Contains(md, "third") {
				t.Errorf("replace calls %d, markdown %q", f.fake.Calls("ReplaceCanvas"), md)
			}
		})

		t.Run("unknown-channel-hint", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, true, 20*time.Millisecond)
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			f.slKindChannel("job1-fresh", "job1", ab2store.KindTasks, canvasTasksACL())
			if _, err := f.h.localAs("job1-coder").Create(context.Background(), ab2bus.TaskCreateInput{Channel: "job1-fresh", Subject: "in the fresh list"}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the new channel", canvasMarkdownHas(f, "job1-fresh"))
		})

		t.Run("lifecycle-catches-lease-expiry", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, false, 20*time.Millisecond)
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			ctx := context.Background()
			coder := f.h.localAs("job1-coder")
			f.h.session("job1-coder", "")
			task, err := coder.Create(ctx, ab2bus.TaskCreateInput{Channel: "job1-tasks", Subject: "LEASE-TASK-X"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := coder.Claim(ctx, ab2bus.TaskClaimInput{ID: task.ID, LeasedUntil: f.h.taskLeaseIn(time.Second)}); err != nil {
				t.Fatal(err)
			}
			// The claim's own hints publish the task unstalled first; only
			// then does the clock pass the lease, with no hint.
			waitFor(t, 10*time.Second, "the claimed task", canvasMarkdownHas(f, "LEASE-TASK-X"))
			time.Sleep(300 * time.Millisecond)
			if canvasMarkdownHas(f, "stalled: lease expired")() {
				t.Fatal("stalled before the lease expired")
			}
			f.h.clock.Advance(2 * time.Second)
			waitFor(t, 10*time.Second, "the stalled mark", canvasMarkdownHas(f, "stalled: lease expired"))
		})

		t.Run("maintenance-reclaim-shows-stalled", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, false, 20*time.Millisecond)
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			ctx := context.Background()
			coder := f.h.localAs("job1-coder")
			f.h.session("job1-coder", "")
			task, err := coder.Create(ctx, ab2bus.TaskCreateInput{Channel: "job1-tasks", Subject: "RECLAIM-TASK-X"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := coder.Claim(ctx, ab2bus.TaskClaimInput{ID: task.ID, LeasedUntil: f.h.taskLeaseIn(time.Second)}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the claimed task", canvasMarkdownHas(f, "RECLAIM-TASK-X"))
			f.h.clock.Advance(2 * time.Second)
			// The production sequence: maintenance reclaims before any
			// canvas pass sees the expired lease.
			if n := f.h.runJob("abandoned tasks"); n != 1 {
				t.Fatalf("abandoned tasks job reclaimed %d, want 1", n)
			}
			waitFor(t, 10*time.Second, "the reclaimed mark", canvasMarkdownHas(f, "stalled: reclaimed from job1-coder"))
			md := canvasOf(f).Markdown
			if !strings.Contains(md, "### \u26a0\ufe0e Stalled") {
				t.Errorf("no Stalled subsection: %q", md)
			}
			found := false
			for _, line := range strings.Split(md, "\n") {
				if strings.HasPrefix(line, "- \u26a0\ufe0e #") && strings.Contains(line, "reclaimed from job1-coder") && strings.Contains(line, " \u00b7 job1-tasks") {
					found = true
				}
			}
			if !found {
				t.Errorf("no reclaimed Stalled line: %q", md)
			}
			if strings.Contains(md, "owner offline") {
				t.Errorf("owner offline shown: %q", md)
			}
		})

		t.Run("reconnect-marks-all", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, true, 20*time.Millisecond)
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			before := canvasOf(f).Markdown
			var notes ab2store.Channel
			f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				var err error
				notes, err = tx.ChannelByName(ctx, "job1-notes")
				return err
			})
			f.h.append(notes, "job1-coder", "", "SECRET-CONTENT unhinted")
			time.Sleep(200 * time.Millisecond)
			if canvasOf(f).Markdown != before {
				t.Fatal("the canvas changed with no hint and no mark")
			}
			f.fake.Disconnect()
			waitFor(t, 10*time.Second, "the rebuild", func() bool { return canvasOf(f).Markdown != before })
		})

		t.Run("hung-call-does-not-stall-bridging", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, true, 20*time.Millisecond)
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			f.fake.DelayCanvas(2 * time.Second)
			t.Cleanup(func() { f.fake.DelayCanvas(0) })
			f.addCanvasTask("SECRET-SUBJECT slow")
			waitFor(t, 5*time.Second, "the hung replace", func() bool { return f.fake.Calls("ReplaceCanvas") == 1 })
			f.send("job1-coder", ab2bus.SendInput{Content: "still flowing"})
			waitFor(t, time.Second, "the bridged message", func() bool { return len(f.bridged()) == 1 })
			f.fake.Disconnect()
			f.fake.Say(f.sl, "U1", "from slack during the delay", "")
			waitFor(t, time.Second, "the inbound message", func() bool { return len(f.human()) == 1 })
			if n := f.fake.MaxConcurrentSockets(); n != 1 {
				t.Errorf("MaxConcurrentSockets = %d", n)
			}
			f.fake.DelayCanvas(0)
		})

		t.Run("hint-during-hung-call-is-published", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, true, 20*time.Millisecond)
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			f.fake.DelayCanvas(1500 * time.Millisecond)
			t.Cleanup(func() { f.fake.DelayCanvas(0) })
			f.addCanvasTask("SECRET-SUBJECT hung first")
			waitFor(t, 5*time.Second, "the hung replace", func() bool { return f.fake.Calls("ReplaceCanvas") == 1 })
			f.fake.DelayCanvas(0)
			f.addCanvasTask("SECRET-SUBJECT during the hang")
			waitFor(t, 10*time.Second, "the task added during the hang", canvasMarkdownHas(f, "during the hang"))
		})

		t.Run("credential-code-warned-once", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, true, 20*time.Millisecond)
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			for i := 0; i < 3; i++ {
				f.fake.FailNext("ReplaceCanvas", &slackapi.APIError{Method: "canvases.edit", Code: "invalid_auth"})
			}
			f.addCanvasTask("SECRET-SUBJECT after auth failures")
			waitFor(t, 10*time.Second, "the replace after three windows", canvasMarkdownHas(f, "after auth failures"))
			if n := f.fake.Calls("ReplaceCanvas"); n < 4 {
				t.Fatalf("ReplaceCanvas = %d, want at least 4", n)
			}
			log := f.logs.String()
			if n := strings.Count(log, "refused the canvas call with a credential error"); n != 1 {
				t.Errorf("credential warning logged %d times:\n%s", n, log)
			}
			if n := strings.Count(log, "canvas publish failed"); n != 0 {
				t.Errorf("the loop re-logged the error %d times:\n%s", n, log)
			}
		})

		t.Run("only-lock-holder-publishes", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, false, 20*time.Millisecond)
			h2 := f.h.sibling(harnessOptions{})
			svc2 := f.newService(h2, canvasCfg)
			runBridge(t, f.svc)
			runBridge(t, svc2)
			f.awaitCanvas(t)
			time.Sleep(time.Second)
			// A second publisher would find the first's canvas by its marker
			// and create nothing, so count the lookups: one find (no tab
			// yet) by the holder, none by the other replica.
			if got := f.canvasCalls(); got["CreateChannelCanvas"] != 1 || got["ChannelCanvases"] != 1 || got["CanvasContains"] != 0 {
				t.Errorf("calls %v: more than the lock holder published", got)
			}
			canvasOf(f)
		})

		t.Run("relock-republishes", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, false, 20*time.Millisecond)
			stop1 := runBridge(t, f.svc)
			f.awaitCanvas(t)
			stop1()
			h2 := f.h.sibling(harnessOptions{})
			svc2 := f.newService(h2, canvasCfg)
			f.addCanvasTask("SECRET-SUBJECT while two holds")
			stop2 := runBridge(t, svc2)
			waitFor(t, 10*time.Second, "the second holder's replace", func() bool { return f.fake.Calls("ReplaceCanvas") == 1 })
			stop2()
			runBridge(t, f.svc)
			waitFor(t, 10*time.Second, "the republish", func() bool { return f.fake.Calls("ReplaceCanvas") == 2 })
			if n := f.fake.Calls("CreateChannelCanvas"); n != 1 {
				t.Errorf("CreateChannelCanvas = %d", n)
			}
			canvasOf(f)
		})

		t.Run("archived-bridge-skipped", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, false, 20*time.Millisecond)
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			f.fake.ArchiveByHand(f.sl)
			f.send("job1-coder", ab2bus.SendInput{Content: "into the void"})
			waitFor(t, 10*time.Second, "the archive", func() bool { return f.bridge().State == ab2store.BridgeArchived })
			time.Sleep(200 * time.Millisecond)
			before := f.canvasCalls()
			f.addCanvasTask("SECRET-SUBJECT after archive")
			time.Sleep(500 * time.Millisecond)
			if got := f.canvasCalls(); !maps.Equal(before, got) {
				t.Errorf("canvas calls after the archive: %v -> %v", before, got)
			}
		})

		t.Run("creating-bridge-skipped", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, true, 20*time.Millisecond)
			other := f.slChannel("job1-more", "job1", nil)
			f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: other.ID, ChannelName: other.Name, Scope: "job1", SlackName: "ab-job1-more", CreatedAt: 9})
				return err
			})
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			f.addCanvasTask("SECRET-SUBJECT with a creating bridge")
			waitFor(t, 10*time.Second, "the replace", func() bool { return f.fake.Calls("ReplaceCanvas") == 1 })
			time.Sleep(300 * time.Millisecond)
			if n := f.fake.Calls("CreateChannelCanvas"); n != 1 {
				t.Errorf("CreateChannelCanvas = %d", n)
			}
			canvasOf(f)
			if strings.Contains(f.logs.String(), "slack bridge: canvas") {
				t.Errorf("a canvas warning was logged:\n%s", f.logs.String())
			}
		})

		t.Run("removed-between-snapshot-and-publish", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, true, 20*time.Millisecond)
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			time.Sleep(200 * time.Millisecond)
			before := f.canvasCalls()
			if err := f.svc.Remove(context.Background(), f.adm, ab2bus.RemoveBridgeInput{Channel: "job1-chat"}); err != nil {
				t.Fatal(err)
			}
			mark := len(f.logs.String())
			f.addCanvasTask("SECRET-SUBJECT after remove")
			time.Sleep(500 * time.Millisecond)
			if got := f.canvasCalls(); !maps.Equal(before, got) {
				t.Errorf("canvas calls after the removal: %v -> %v", before, got)
			}
			if strings.Contains(f.logs.String()[mark:], "slack bridge: canvas") {
				t.Errorf("a canvas line was logged after the removal:\n%s", f.logs.String()[mark:])
			}
		})

		t.Run("retired-scope-skipped", func(t *testing.T) {
			f, _ := canvasRunFix(t, e, true, 20*time.Millisecond)
			runBridge(t, f.svc)
			f.awaitCanvas(t)
			time.Sleep(200 * time.Millisecond)
			before := f.canvasCalls()
			f.h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				return tx.MarkScopeRetired(ctx, "job1", f.h.core.NowMs())
			})
			f.addCanvasTask("SECRET-SUBJECT after retire")
			time.Sleep(500 * time.Millisecond)
			if got := f.canvasCalls(); !maps.Equal(before, got) {
				t.Errorf("canvas calls after the retirement: %v -> %v", before, got)
			}
		})

		t.Run("disabled-setting", func(t *testing.T) {
			f := newOutFix(t, e, nil)
			f.canvasSetup()
			runBridge(t, f.svc)
			f.send("job1-coder", ab2bus.SendInput{Content: "hello"})
			waitFor(t, 10*time.Second, "the bridged message", func() bool { return len(f.bridged()) == 1 })
			time.Sleep(time.Second)
			for m, n := range f.canvasCalls() {
				if n != 0 {
					t.Errorf("%s called %d times", m, n)
				}
			}
		})
	})
}
