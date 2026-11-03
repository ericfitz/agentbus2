package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/core/statuses"
)

func (b *Bus) statuses() statuses.Service {
	return statuses.Service{Core: b.c, Sessions: b.sessions()}
}

// SetAgentStatus sets the caller's status from one of its live sessions.
func (b *Bus) SetAgentStatus(ctx context.Context, in ab2bus.SetAgentStatusInput) (ab2bus.AgentStatus, error) {
	return b.statuses().Set(ctx, b.identity, in)
}

// AgentStatuses lists every visible identity with a stored status.
func (b *Bus) AgentStatuses(ctx context.Context) ([]ab2bus.AgentStatusEntry, error) {
	return b.statuses().List(ctx, b.identity)
}

// AgentStatusHistory lists one identity's stored statuses, oldest first.
func (b *Bus) AgentStatusHistory(ctx context.Context, identity string) ([]ab2bus.AgentStatus, error) {
	return b.statuses().History(ctx, b.identity, identity)
}
