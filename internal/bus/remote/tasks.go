package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

func fromGenTaskResult(r gen.TaskUpdateResult) ab2bus.TaskUpdateResult {
	return ab2bus.TaskUpdateResult{Task: fromGenTask(r.Task), Replaced: r.Replaced, Deleted: deref(r.Deleted)}
}

func (b *Bus) Create(ctx context.Context, in ab2bus.TaskCreateInput) (ab2bus.Task, error) {
	body := gen.TaskCreateRequest{
		Subject: in.Subject, Description: strPtr(in.Description), Parent: int64Ptr(in.Parent),
		Before: int64Ptr(in.Before), After: int64Ptr(in.After),
		BlockedBy: slicePtr(in.BlockedBy), Metadata: mapPtr(in.Metadata),
	}
	r, err := b.c.CreateTaskWithResponse(ctx, in.Channel, &gen.CreateTaskParams{IdempotencyKey: key(in.IdempotencyKey)}, body)
	if err := mtCheck(ctx, r, err, func(r *gen.CreateTaskResponse) bool { return r.JSON201 != nil }); err != nil {
		return ab2bus.Task{}, err
	}
	return fromGenTask(*r.JSON201), nil
}

func (b *Bus) GetTask(ctx context.Context, id int64) (ab2bus.Task, error) {
	r, err := b.c.GetTaskWithResponse(ctx, id)
	if err := mtCheck(ctx, r, err, func(r *gen.GetTaskResponse) bool { return r.JSON200 != nil }); err != nil {
		return ab2bus.Task{}, err
	}
	return fromGenTask(*r.JSON200), nil
}

func (b *Bus) List(ctx context.Context, in ab2bus.TaskListInput) ([]ab2bus.TaskSummary, error) {
	r, err := b.c.ListTasksWithResponse(ctx, in.Channel, &gen.ListTasksParams{Status: strPtr(in.Status), Owner: strPtr(in.Owner)})
	if err := mtCheck(ctx, r, err, func(r *gen.ListTasksResponse) bool { return r.JSON200 != nil }); err != nil {
		return nil, err
	}
	out := make([]ab2bus.TaskSummary, len(*r.JSON200))
	for i, s := range *r.JSON200 {
		out[i] = fromGenTaskSummary(s)
	}
	return out, nil
}

func (b *Bus) Update(ctx context.Context, p ab2bus.TaskPatch) (ab2bus.TaskUpdateResult, error) {
	body := gen.TaskPatch{
		Status: p.Status, Owner: p.Owner, Subject: p.Subject, Description: p.Description,
		Parent: p.Parent, Before: int64Ptr(p.Before), After: int64Ptr(p.After),
		AddBlockedBy: slicePtr(p.AddBlockedBy), RemoveBlockedBy: slicePtr(p.RemoveBlockedBy),
		LeasedUntil: p.LeasedUntil, Metadata: mapPtr(p.Metadata),
	}
	if p.Delete {
		body.Delete = ptr(true)
	}
	if p.Force {
		body.Force = ptr(true)
	}
	r, err := b.c.UpdateTaskWithResponse(ctx, p.ID, &gen.UpdateTaskParams{IdempotencyKey: key(p.IdempotencyKey)}, body)
	if err := mtCheck(ctx, r, err, func(r *gen.UpdateTaskResponse) bool { return r.JSON200 != nil }); err != nil {
		return ab2bus.TaskUpdateResult{}, err
	}
	return fromGenTaskResult(*r.JSON200), nil
}

func (b *Bus) Claim(ctx context.Context, in ab2bus.TaskClaimInput) (ab2bus.TaskUpdateResult, error) {
	r, err := b.c.ClaimTaskWithResponse(ctx, in.ID, &gen.ClaimTaskParams{IdempotencyKey: key(in.IdempotencyKey)},
		gen.ClaimTaskRequest{LeasedUntil: int64Ptr(in.LeasedUntil)})
	if err := mtCheck(ctx, r, err, func(r *gen.ClaimTaskResponse) bool { return r.JSON200 != nil }); err != nil {
		return ab2bus.TaskUpdateResult{}, err
	}
	return fromGenTaskResult(*r.JSON200), nil
}

func (b *Bus) Release(ctx context.Context, in ab2bus.TaskReleaseInput) (ab2bus.TaskUpdateResult, error) {
	r, err := b.c.ReleaseTaskWithResponse(ctx, in.ID, &gen.ReleaseTaskParams{IdempotencyKey: key(in.IdempotencyKey)})
	if err := mtCheck(ctx, r, err, func(r *gen.ReleaseTaskResponse) bool { return r.JSON200 != nil }); err != nil {
		return ab2bus.TaskUpdateResult{}, err
	}
	return fromGenTaskResult(*r.JSON200), nil
}
