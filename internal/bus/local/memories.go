package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/memories"
)

// GetMemory is memories.Service.Get as b's identity. The name differs from
// the service's because Bus embeds Memories and Tasks.
func (b *Bus) GetMemory(ctx context.Context, id int64) (ab2bus.Message, error) {
	return memories.Service{Core: b.c}.Get(ctx, b.identity, id)
}

// Edit is memories.Service.Edit as b's identity.
func (b *Bus) Edit(ctx context.Context, in ab2bus.EditMemoryInput) (ab2bus.EditResult, error) {
	return memories.Service{Core: b.c}.Edit(ctx, b.identity, in)
}

// Delete is memories.Service.Delete as b's identity.
func (b *Bus) Delete(ctx context.Context, in ab2bus.DeleteMemoryInput) error {
	return memories.Service{Core: b.c}.Delete(ctx, b.identity, in)
}

// Revisions is memories.Service.Revisions as b's identity.
func (b *Bus) Revisions(ctx context.Context, id int64) ([]ab2bus.Message, error) {
	return memories.Service{Core: b.c}.Revisions(ctx, b.identity, id)
}
