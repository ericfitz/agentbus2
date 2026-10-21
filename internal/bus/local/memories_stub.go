// This file is replaced by Task 7: memories.

package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) GetMemory(ctx context.Context, id int64) (ab2bus.Message, error) {
	return ab2bus.Message{}, ErrNotImplemented
}

func (b *Bus) Edit(ctx context.Context, in ab2bus.EditMemoryInput) (ab2bus.EditResult, error) {
	return ab2bus.EditResult{}, ErrNotImplemented
}

func (b *Bus) Delete(ctx context.Context, in ab2bus.DeleteMemoryInput) error {
	return ErrNotImplemented
}

func (b *Bus) Revisions(ctx context.Context, id int64) ([]ab2bus.Message, error) {
	return nil, ErrNotImplemented
}
