package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) GetMemory(_ context.Context, _ int64) (ab2bus.Message, error) {
	return ab2bus.Message{}, notImplemented("GetMemory")
}

func (b *Bus) Edit(_ context.Context, _ ab2bus.EditMemoryInput) (ab2bus.EditResult, error) {
	return ab2bus.EditResult{}, notImplemented("Edit")
}

func (b *Bus) Delete(_ context.Context, _ ab2bus.DeleteMemoryInput) error {
	return notImplemented("Delete")
}

func (b *Bus) Revisions(_ context.Context, _ int64) ([]ab2bus.Message, error) {
	return nil, notImplemented("Revisions")
}

func (b *Bus) Create(_ context.Context, _ ab2bus.TaskCreateInput) (ab2bus.Task, error) {
	return ab2bus.Task{}, notImplemented("Create")
}

func (b *Bus) GetTask(_ context.Context, _ int64) (ab2bus.Task, error) {
	return ab2bus.Task{}, notImplemented("GetTask")
}

func (b *Bus) List(_ context.Context, _ ab2bus.TaskListInput) ([]ab2bus.TaskSummary, error) {
	return nil, notImplemented("List")
}

func (b *Bus) Update(_ context.Context, _ ab2bus.TaskPatch) (ab2bus.TaskUpdateResult, error) {
	return ab2bus.TaskUpdateResult{}, notImplemented("Update")
}

func (b *Bus) Claim(_ context.Context, _ ab2bus.TaskClaimInput) (ab2bus.TaskUpdateResult, error) {
	return ab2bus.TaskUpdateResult{}, notImplemented("Claim")
}

func (b *Bus) Release(_ context.Context, _ ab2bus.TaskReleaseInput) (ab2bus.TaskUpdateResult, error) {
	return ab2bus.TaskUpdateResult{}, notImplemented("Release")
}
