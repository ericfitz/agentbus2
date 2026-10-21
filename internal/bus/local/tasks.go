package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/tasks"
)

func (b *Bus) tasks() tasks.Service { return tasks.Service{Core: b.c} }

func (b *Bus) Create(ctx context.Context, in ab2bus.TaskCreateInput) (ab2bus.Task, error) {
	return b.tasks().Create(ctx, b.identity, in)
}

func (b *Bus) GetTask(ctx context.Context, id int64) (ab2bus.Task, error) {
	return b.tasks().Get(ctx, b.identity, id)
}

func (b *Bus) List(ctx context.Context, in ab2bus.TaskListInput) ([]ab2bus.TaskSummary, error) {
	return b.tasks().List(ctx, b.identity, in)
}

func (b *Bus) Update(ctx context.Context, p ab2bus.TaskPatch) (ab2bus.TaskUpdateResult, error) {
	return b.tasks().Update(ctx, b.identity, p)
}

func (b *Bus) Claim(ctx context.Context, in ab2bus.TaskClaimInput) (ab2bus.TaskUpdateResult, error) {
	return b.tasks().Claim(ctx, b.identity, in)
}

func (b *Bus) Release(ctx context.Context, in ab2bus.TaskReleaseInput) (ab2bus.TaskUpdateResult, error) {
	return b.tasks().Release(ctx, b.identity, in)
}
