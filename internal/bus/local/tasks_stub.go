// This file is replaced by Task 8: tasks.

package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) Create(ctx context.Context, in ab2bus.TaskCreateInput) (ab2bus.Task, error) {
	return ab2bus.Task{}, ErrNotImplemented
}

func (b *Bus) GetTask(ctx context.Context, id int64) (ab2bus.Task, error) {
	return ab2bus.Task{}, ErrNotImplemented
}

func (b *Bus) List(ctx context.Context, in ab2bus.TaskListInput) ([]ab2bus.TaskSummary, error) {
	return nil, ErrNotImplemented
}

func (b *Bus) Update(ctx context.Context, p ab2bus.TaskPatch) (ab2bus.TaskUpdateResult, error) {
	return ab2bus.TaskUpdateResult{}, ErrNotImplemented
}

func (b *Bus) Claim(ctx context.Context, in ab2bus.TaskClaimInput) (ab2bus.TaskUpdateResult, error) {
	return ab2bus.TaskUpdateResult{}, ErrNotImplemented
}

func (b *Bus) Release(ctx context.Context, in ab2bus.TaskReleaseInput) (ab2bus.TaskUpdateResult, error) {
	return ab2bus.TaskUpdateResult{}, ErrNotImplemented
}
