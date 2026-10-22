package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/messaging"
	"github.com/ericfitz/agentbus2/internal/core/sessions"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/store"
)

// mtFixture is an env with identity pat, a memory channel "mem" holding one
// memory, a tasks channel "work" and an ordinary channel "chat", all created
// through the core.
type mtFixture struct {
	e        *env
	c        *gen.ClientWithResponses
	memoryID int64
}

func newMTFixture(t *testing.T) *mtFixture {
	t.Helper()
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	ctx := context.Background()
	ch := channels.Service{Core: e.core}
	for _, in := range []ab2bus.CreateChannelInput{
		{Name: "mem", Kind: ab2bus.KindMemory},
		{Name: "work", Kind: ab2bus.KindTasks},
		{Name: "chat"},
	} {
		if _, err := ch.Create(ctx, "pat", in); err != nil {
			t.Fatalf("create %s: %v", in.Name, err)
		}
	}
	res, err := messaging.Service{Core: e.core}.Send(ctx, "pat", ab2bus.SendInput{
		Channel: "mem", Subject: "first", Content: "v1", Tags: []string{"a", "b"},
	})
	if err != nil || res.MemoryID == nil {
		t.Fatalf("send memory: %+v, %v", res, err)
	}
	return &mtFixture{e: e, c: e.client("pat"), memoryID: *res.MemoryID}
}

// latest is the newest stored revision of the fixture's memory, read through
// the Store.
func (f *mtFixture) latest(t *testing.T) store.Message {
	t.Helper()
	var m store.Message
	err := f.e.store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		revs, err := tx.MemoryRevisions(ctx, f.memoryID)
		if err != nil {
			return err
		}
		m = revs[len(revs)-1]
		return nil
	})
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	return m
}

func wantProblem(t *testing.T, status int, p *gen.Problem, wantStatus int, wantCode gen.ProblemCode) {
	t.Helper()
	if status != wantStatus || p == nil || p.Code != wantCode {
		t.Fatalf("status %d problem %+v, want %d %s", status, p, wantStatus, wantCode)
	}
}

func sp(s string) *string { return &s }

func TestEditMemoryKeepVersusClear(t *testing.T) {
	f := newMTFixture(t)
	ctx := context.Background()
	edit := func(body string) *gen.EditMemoryResponse {
		t.Helper()
		r, err := f.c.EditMemoryWithBodyWithResponse(ctx, f.memoryID, &gen.EditMemoryParams{}, "application/json",
			bytes.NewBufferString(body))
		if err != nil || r.JSON201 == nil {
			t.Fatalf("edit %s: %+v, %v", body, r, err)
		}
		return r
	}

	// No tags member keeps the tags; no subject member keeps the subject.
	if r := edit(`{"content":"v2"}`); r.JSON201.Revision != 2 {
		t.Fatalf("revision = %d, want 2", r.JSON201.Revision)
	}
	if m := f.latest(t); m.Subject != "first" || len(m.Tags) != 2 || m.Content != "v2" {
		t.Fatalf("after no-tags edit: %+v", m)
	}

	// "tags": [] clears.
	edit(`{"content":"v3","tags":[]}`)
	if m := f.latest(t); len(m.Tags) != 0 || m.Subject != "first" {
		t.Fatalf("after empty-tags edit: %+v", m)
	}

	// "subject": "" clears the subject.
	edit(`{"content":"v4","subject":""}`)
	if m := f.latest(t); m.Subject != "" || m.Content != "v4" {
		t.Fatalf("after empty-subject edit: %+v", m)
	}
}

func TestMemoryRoutesWire(t *testing.T) {
	f := newMTFixture(t)
	ctx := context.Background()

	g, err := f.c.GetMemoryWithResponse(ctx, f.memoryID)
	if err != nil || g.JSON200 == nil || g.JSON200.Content != "v1" {
		t.Fatalf("get: %+v, %v", g, err)
	}
	v2 := "v2"
	e, err := f.c.EditMemoryWithResponse(ctx, f.memoryID, &gen.EditMemoryParams{}, gen.EditMemoryRequest{Content: &v2})
	if err != nil || e.JSON201 == nil || e.JSON201.Revision != 2 || e.JSON201.MemoryId != f.memoryID {
		t.Fatalf("edit: %+v, %v", e, err)
	}
	d, err := f.c.DeleteMemoryWithResponse(ctx, f.memoryID, &gen.DeleteMemoryParams{})
	if err != nil || d.StatusCode() != http.StatusNoContent {
		t.Fatalf("delete: %+v, %v", d, err)
	}
	g, err = f.c.GetMemoryWithResponse(ctx, f.memoryID)
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, g.StatusCode(), g.ApplicationproblemJSONDefault, http.StatusNotFound, gen.NotFound)
	rv, err := f.c.ListMemoryRevisionsWithResponse(ctx, f.memoryID)
	if err != nil || rv.JSON200 == nil || len(*rv.JSON200) == 0 {
		t.Fatalf("revisions after delete: %+v, %v", rv, err)
	}
}

func TestTaskRoutesWire(t *testing.T) {
	f := newMTFixture(t)
	ctx := context.Background()

	// An ordinary channel is not a task list.
	r, err := f.c.CreateTaskWithResponse(ctx, "chat", &gen.CreateTaskParams{}, gen.TaskCreateRequest{Subject: "x"})
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, r.StatusCode(), r.ApplicationproblemJSONDefault, http.StatusBadRequest, gen.Validation)

	cr, err := f.c.CreateTaskWithResponse(ctx, "work", &gen.CreateTaskParams{}, gen.TaskCreateRequest{Subject: "one"})
	if err != nil || cr.JSON201 == nil {
		t.Fatalf("create: %+v, %v", cr, err)
	}
	id := cr.JSON201.Id

	bad := "paused"
	l, err := f.c.ListTasksWithResponse(ctx, "work", &gen.ListTasksParams{Status: &bad})
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, l.StatusCode(), l.ApplicationproblemJSONDefault, http.StatusBadRequest, gen.Validation)
	l, err = f.c.ListTasksWithResponse(ctx, "work", &gen.ListTasksParams{})
	if err != nil || l.JSON200 == nil || len(*l.JSON200) != 1 {
		t.Fatalf("list: %+v, %v", l, err)
	}

	// Claim with the same key twice returns the same result (Decision 66).
	k := "claim-1"
	c1, err := f.c.ClaimTaskWithResponse(ctx, id, &gen.ClaimTaskParams{IdempotencyKey: &k}, gen.ClaimTaskRequest{})
	if err != nil || c1.JSON200 == nil || c1.JSON200.Task.Status != "in_progress" {
		t.Fatalf("claim: %+v, %v", c1, err)
	}
	c2, err := f.c.ClaimTaskWithResponse(ctx, id, &gen.ClaimTaskParams{IdempotencyKey: &k}, gen.ClaimTaskRequest{})
	if err != nil || c2.JSON200 == nil || c2.JSON200.Task.Revision != c1.JSON200.Task.Revision ||
		c2.JSON200.Replaced != c1.JSON200.Replaced {
		t.Fatalf("claim replay: %+v vs %+v, %v", c2, c1, err)
	}

	bogus := "bogus"
	u, err := f.c.UpdateTaskWithResponse(ctx, id, &gen.UpdateTaskParams{}, gen.TaskPatch{Status: &bogus})
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, u.StatusCode(), u.ApplicationproblemJSONDefault, http.StatusBadRequest, gen.Validation)

	done := "completed"
	u, err = f.c.UpdateTaskWithResponse(ctx, id, &gen.UpdateTaskParams{}, gen.TaskPatch{Status: &done})
	if err != nil || u.JSON200 == nil || u.JSON200.Task.Status != "completed" {
		t.Fatalf("complete: %+v, %v", u, err)
	}

	// Release a second claimed task.
	cr2, err := f.c.CreateTaskWithResponse(ctx, "work", &gen.CreateTaskParams{}, gen.TaskCreateRequest{Subject: "two"})
	if err != nil || cr2.JSON201 == nil {
		t.Fatalf("create 2: %+v, %v", cr2, err)
	}
	if c, err := f.c.ClaimTaskWithResponse(ctx, cr2.JSON201.Id, &gen.ClaimTaskParams{}, gen.ClaimTaskRequest{}); err != nil || c.JSON200 == nil {
		t.Fatalf("claim 2: %+v, %v", c, err)
	}
	rel, err := f.c.ReleaseTaskWithResponse(ctx, cr2.JSON201.Id, &gen.ReleaseTaskParams{})
	if err != nil || rel.JSON200 == nil || rel.JSON200.Task.Status != "pending" || rel.JSON200.Task.Owner != nil {
		t.Fatalf("release: %+v, %v", rel, err)
	}

	gt, err := f.c.GetTaskWithResponse(ctx, id)
	if err != nil || gt.JSON200 == nil || gt.JSON200.Subject != "one" {
		t.Fatalf("get: %+v, %v", gt, err)
	}
}

func TestTaskPatchMembersRoundTrip(t *testing.T) {
	f := newMTFixture(t)
	ctx := context.Background()
	mk := func(s string) int64 {
		r, err := f.c.CreateTaskWithResponse(ctx, "work", &gen.CreateTaskParams{}, gen.TaskCreateRequest{Subject: s})
		if err != nil || r.JSON201 == nil {
			t.Fatalf("create %s: %+v, %v", s, r, err)
		}
		return r.JSON201.Id
	}
	a, b, parent := mk("a"), mk("b"), mk("parent")
	patch := func(p gen.TaskPatch) gen.Task {
		t.Helper()
		r, err := f.c.UpdateTaskWithResponse(ctx, a, &gen.UpdateTaskParams{}, p)
		if err != nil || r.JSON200 == nil {
			t.Fatalf("patch %+v: %+v, %v", p, r, err)
		}
		return r.JSON200.Task
	}

	if tk := patch(gen.TaskPatch{Subject: sp("renamed"), Description: sp("details")}); tk.Subject != "renamed" ||
		tk.Description == nil || *tk.Description != "details" {
		t.Fatalf("subject/description: %+v", tk)
	}
	if tk := patch(gen.TaskPatch{Metadata: &map[string]string{"k": "v"}}); tk.Metadata == nil || (*tk.Metadata)["k"] != "v" {
		t.Fatalf("metadata: %+v", tk)
	}
	if tk := patch(gen.TaskPatch{AddBlockedBy: &[]int64{b}}); tk.BlockedBy == nil || len(*tk.BlockedBy) != 1 || !tk.Blocked {
		t.Fatalf("add_blocked_by: %+v", tk)
	}
	if tk := patch(gen.TaskPatch{RemoveBlockedBy: &[]int64{b}}); tk.Blocked {
		t.Fatalf("remove_blocked_by: %+v", tk)
	}
	before := patch(gen.TaskPatch{Before: &b})
	if after := patch(gen.TaskPatch{After: &b}); before.Rank == "" || after.Rank == before.Rank {
		t.Fatalf("before rank %q, after rank %q: want distinct non-empty ranks", before.Rank, after.Rank)
	}
	if tk := patch(gen.TaskPatch{Parent: &parent}); tk.Parent == nil || *tk.Parent != parent {
		t.Fatalf("parent: %+v", tk)
	}
	lease := f.e.core.NowMs() + 60_000
	if tk := patch(gen.TaskPatch{Owner: sp("pat"), Status: sp("in_progress"), LeasedUntil: &lease}); tk.Owner == nil ||
		*tk.Owner != "pat" || tk.LeasedUntil == nil || *tk.LeasedUntil != lease || tk.Status != "in_progress" {
		t.Fatalf("owner/status/lease: %+v", tk)
	}

	// Force only matters against the owner guard: give b to another identity
	// (with a live session, or the task is reclaimed as abandoned) and in
	// progress, then Delete alone is refused and Delete with Force deletes.
	f.e.identity("sam", false)
	if _, err := (sessions.Service{Core: f.e.core}).Open(ctx, "sam", ab2bus.OpenSessionInput{}); err != nil {
		t.Fatalf("open sam's session: %v", err)
	}
	bPatch, err := f.c.UpdateTaskWithResponse(ctx, b, &gen.UpdateTaskParams{},
		gen.TaskPatch{Owner: sp("sam"), Status: sp("in_progress")})
	if err != nil || bPatch.JSON200 == nil || bPatch.JSON200.Task.Owner == nil || *bPatch.JSON200.Task.Owner != "sam" {
		t.Fatalf("give b to sam: %+v, %v", bPatch, err)
	}
	tr := true
	r, err := f.c.UpdateTaskWithResponse(ctx, b, &gen.UpdateTaskParams{}, gen.TaskPatch{Delete: &tr})
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, r.StatusCode(), r.ApplicationproblemJSONDefault, http.StatusConflict, gen.Conflict)
	r, err = f.c.UpdateTaskWithResponse(ctx, b, &gen.UpdateTaskParams{}, gen.TaskPatch{Delete: &tr, Force: &tr})
	if err != nil || r.JSON200 == nil || r.JSON200.Deleted == nil || !*r.JSON200.Deleted {
		t.Fatalf("delete/force: %+v, %v", r, err)
	}
}

// TestHandlersRefuseAMissingBody pins the handlers' own guard behind the
// validator, which rejects a missing required body first: the error is the
// validation code with the same wording as the messages handlers.
func TestHandlersRefuseAMissingBody(t *testing.T) {
	a := &api{}
	ctx := context.Background()
	const want = "the request body is required"
	for name, call := range map[string]func() error{
		"EditMemory": func() error { _, err := a.EditMemory(ctx, gen.EditMemoryRequestObject{Id: 1}); return err },
		"CreateTask": func() error { _, err := a.CreateTask(ctx, gen.CreateTaskRequestObject{Channel: "work"}); return err },
		"UpdateTask": func() error { _, err := a.UpdateTask(ctx, gen.UpdateTaskRequestObject{Id: 1}); return err },
	} {
		var be *ab2bus.Error
		if err := call(); !errors.As(err, &be) || be.Code != ab2bus.CodeValidation || be.Retryable || be.Message != want {
			t.Errorf("%s with no body: %v, want validation %q", name, err, want)
		}
	}
}

func TestClaimLeasedUntilWire(t *testing.T) {
	f := newMTFixture(t)
	ctx := context.Background()
	cr, err := f.c.CreateTaskWithResponse(ctx, "work", &gen.CreateTaskParams{}, gen.TaskCreateRequest{Subject: "x"})
	if err != nil || cr.JSON201 == nil {
		t.Fatalf("create: %+v, %v", cr, err)
	}
	lease := f.e.core.NowMs() + 120_000
	c, err := f.c.ClaimTaskWithResponse(ctx, cr.JSON201.Id, &gen.ClaimTaskParams{}, gen.ClaimTaskRequest{LeasedUntil: &lease})
	if err != nil || c.JSON200 == nil || c.JSON200.Task.LeasedUntil == nil || *c.JSON200.Task.LeasedUntil != lease {
		t.Fatalf("claim with lease: %+v, %v", c, err)
	}
}
