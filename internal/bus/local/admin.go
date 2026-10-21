package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

// Status is the administrator's view (Decision 73); the channel operations of
// Admin are DeleteChannel and RenameChannel in channels.go.
func (b *Bus) Status(ctx context.Context) (ab2bus.Status, error) {
	return b.channels().Status(ctx, b.identity)
}
