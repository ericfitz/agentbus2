package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/sessions"
)

func (b *Bus) sessions() sessions.Service { return sessions.Service{Core: b.c} }

// Open opens or resumes a session (Task 5).
func (b *Bus) Open(ctx context.Context, in ab2bus.OpenSessionInput) (ab2bus.Session, error) {
	return b.sessions().Open(ctx, b.identity, in)
}

// Close closes one of the identity's sessions.
func (b *Bus) Close(ctx context.Context, in ab2bus.CloseSessionInput) error {
	return b.sessions().Close(ctx, b.identity, in)
}

// Identities lists the identities with a live session.
func (b *Bus) Identities(ctx context.Context) ([]ab2bus.IdentityInfo, error) {
	return b.sessions().Identities(ctx, b.identity)
}
