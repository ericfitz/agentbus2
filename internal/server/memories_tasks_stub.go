package server

// Stubs replaced by Task 8. Each answers 500 with a detail naming it.

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/gen"
)

func (a *api) GetMemory(context.Context, gen.GetMemoryRequestObject) (gen.GetMemoryResponseObject, error) {
	return nil, notImplemented("GetMemory")
}

func (a *api) EditMemory(context.Context, gen.EditMemoryRequestObject) (gen.EditMemoryResponseObject, error) {
	return nil, notImplemented("EditMemory")
}

func (a *api) ListMemoryRevisions(context.Context, gen.ListMemoryRevisionsRequestObject) (gen.ListMemoryRevisionsResponseObject, error) {
	return nil, notImplemented("ListMemoryRevisions")
}

func (a *api) DeleteMemory(context.Context, gen.DeleteMemoryRequestObject) (gen.DeleteMemoryResponseObject, error) {
	return nil, notImplemented("DeleteMemory")
}

func (a *api) CreateTask(context.Context, gen.CreateTaskRequestObject) (gen.CreateTaskResponseObject, error) {
	return nil, notImplemented("CreateTask")
}

func (a *api) ListTasks(context.Context, gen.ListTasksRequestObject) (gen.ListTasksResponseObject, error) {
	return nil, notImplemented("ListTasks")
}

func (a *api) GetTask(context.Context, gen.GetTaskRequestObject) (gen.GetTaskResponseObject, error) {
	return nil, notImplemented("GetTask")
}

func (a *api) UpdateTask(context.Context, gen.UpdateTaskRequestObject) (gen.UpdateTaskResponseObject, error) {
	return nil, notImplemented("UpdateTask")
}

func (a *api) ClaimTask(context.Context, gen.ClaimTaskRequestObject) (gen.ClaimTaskResponseObject, error) {
	return nil, notImplemented("ClaimTask")
}

func (a *api) ReleaseTask(context.Context, gen.ReleaseTaskRequestObject) (gen.ReleaseTaskResponseObject, error) {
	return nil, notImplemented("ReleaseTask")
}
