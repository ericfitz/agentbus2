package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/core/health"
	"github.com/ericfitz/agentmesh/internal/version"
)

// Health is the server health document at the caller's tier.
func (b *Bus) Health(ctx context.Context) (ab2bus.Health, error) {
	return health.Service{Core: b.c}.Health(ctx, b.identity, version.Version)
}
