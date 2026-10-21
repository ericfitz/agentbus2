package server

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

func optInt64(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}

func optInts(s []int64) *[]int64 {
	if s == nil {
		return nil
	}
	return &s
}

func taskToGen(t ab2bus.Task) gen.Task {
	out := gen.Task{
		Id: t.ID, Channel: t.Channel, Revision: t.Revision, Subject: t.Subject,
		Description: optStr(t.Description), Status: t.Status, Owner: optStr(t.Owner),
		Parent: optInt64(t.Parent), Rank: t.Rank, BlockedBy: optInts(t.BlockedBy),
		LeasedUntil: optInt64(t.LeasedUntil), Blocked: t.Blocked,
		OpenBlockers: optInts(t.OpenBlockers), UpdatedAt: t.UpdatedAt, UpdatedBy: t.UpdatedBy,
	}
	if t.Metadata != nil {
		md := t.Metadata
		out.Metadata = &md
	}
	return out
}

func taskResultToGen(r ab2bus.TaskUpdateResult) gen.TaskUpdateResult {
	out := gen.TaskUpdateResult{Task: taskToGen(r.Task), Replaced: r.Replaced}
	if r.Deleted {
		d := true
		out.Deleted = &d
	}
	return out
}

func (a *api) CreateTask(ctx context.Context, r gen.CreateTaskRequestObject) (gen.CreateTaskResponseObject, error) {
	if r.Body == nil {
		return nil, ab2bus.Errorf(ab2bus.CodeValidation, false, "the request body is required")
	}
	b := r.Body
	in := ab2bus.TaskCreateInput{
		Channel: r.Channel, Subject: b.Subject, Description: derefOr(b.Description),
		Parent: derefOr(b.Parent), Before: derefOr(b.Before), After: derefOr(b.After),
		BlockedBy: derefOr(b.BlockedBy), Metadata: derefOr(b.Metadata),
		IdempotencyKey: idemKey(r.Params.IdempotencyKey),
	}
	t, err := a.tasks.Create(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err
	}
	return gen.CreateTask201JSONResponse(taskToGen(t)), nil
}

func (a *api) ListTasks(ctx context.Context, r gen.ListTasksRequestObject) (gen.ListTasksResponseObject, error) {
	ts, err := a.tasks.List(ctx, IdentityFrom(ctx), ab2bus.TaskListInput{
		Channel: r.Channel, Status: derefOr(r.Params.Status), Owner: derefOr(r.Params.Owner),
	})
	if err != nil {
		return nil, err
	}
	out := make(gen.ListTasks200JSONResponse, len(ts))
	for i, s := range ts {
		g := gen.TaskSummary{
			Id: s.ID, Subject: s.Subject, Status: s.Status, Owner: optStr(s.Owner),
			Parent: optInt64(s.Parent), Depth: s.Depth, OpenBlockers: optInts(s.OpenBlockers),
			LeasedUntil: optInt64(s.LeasedUntil),
		}
		if s.HasDetails {
			h := true
			g.HasDetails = &h
		}
		out[i] = g
	}
	return out, nil
}

func (a *api) GetTask(ctx context.Context, r gen.GetTaskRequestObject) (gen.GetTaskResponseObject, error) {
	t, err := a.tasks.Get(ctx, IdentityFrom(ctx), r.Id)
	if err != nil {
		return nil, err
	}
	return gen.GetTask200JSONResponse(taskToGen(t)), nil
}

func (a *api) UpdateTask(ctx context.Context, r gen.UpdateTaskRequestObject) (gen.UpdateTaskResponseObject, error) {
	if r.Body == nil {
		return nil, ab2bus.Errorf(ab2bus.CodeValidation, false, "the request body is required")
	}
	b := r.Body
	p := ab2bus.TaskPatch{
		ID: r.Id, Status: b.Status, Owner: b.Owner, Subject: b.Subject, Description: b.Description,
		Parent: b.Parent, Before: derefOr(b.Before), After: derefOr(b.After),
		AddBlockedBy: derefOr(b.AddBlockedBy), RemoveBlockedBy: derefOr(b.RemoveBlockedBy),
		LeasedUntil: b.LeasedUntil, Metadata: derefOr(b.Metadata),
		Delete: derefOr(b.Delete), Force: derefOr(b.Force),
		IdempotencyKey: idemKey(r.Params.IdempotencyKey),
	}
	res, err := a.tasks.Update(ctx, IdentityFrom(ctx), p)
	if err != nil {
		return nil, err
	}
	return gen.UpdateTask200JSONResponse(taskResultToGen(res)), nil
}

func (a *api) ClaimTask(ctx context.Context, r gen.ClaimTaskRequestObject) (gen.ClaimTaskResponseObject, error) {
	in := ab2bus.TaskClaimInput{ID: r.Id, IdempotencyKey: idemKey(r.Params.IdempotencyKey)}
	if r.Body != nil {
		in.LeasedUntil = derefOr(r.Body.LeasedUntil)
	}
	res, err := a.tasks.Claim(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err
	}
	return gen.ClaimTask200JSONResponse(taskResultToGen(res)), nil
}

func (a *api) ReleaseTask(ctx context.Context, r gen.ReleaseTaskRequestObject) (gen.ReleaseTaskResponseObject, error) {
	res, err := a.tasks.Release(ctx, IdentityFrom(ctx), ab2bus.TaskReleaseInput{
		ID: r.Id, IdempotencyKey: idemKey(r.Params.IdempotencyKey),
	})
	if err != nil {
		return nil, err
	}
	return gen.ReleaseTask200JSONResponse(taskResultToGen(res)), nil
}
