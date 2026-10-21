package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

// Usage is the administrator's database usage reading (Decision 48).
func (b *Bus) Usage(ctx context.Context) (ab2bus.Usage, error) {
	return b.channels().Usage(ctx, b.identity)
}
