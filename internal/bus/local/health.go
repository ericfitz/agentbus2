package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/health"
	"github.com/ericfitz/agentbus2/internal/version"
)

// Health is the server health document at the caller's tier.
func (b *Bus) Health(ctx context.Context) (ab2bus.Health, error) {
	return health.Service{Core: b.c}.Health(ctx, b.identity, version.Version)
}
