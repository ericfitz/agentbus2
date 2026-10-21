package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

// Peek is delivery.Service.Peek as b's identity.
func (b *Bus) Peek(ctx context.Context, in ab2bus.PeekInput) (ab2bus.PeekResult, error) {
	return b.delivery().Peek(ctx, b.identity, in)
}
