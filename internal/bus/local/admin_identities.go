package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/identities"
)

func (b *Bus) identities() identities.Service { return identities.Service{Core: b.c} }

// Reset wipes the bus data for the bound administrator; an empty scope is data.
func (b *Bus) Reset(ctx context.Context, in ab2bus.ResetInput) error {
	return b.identities().Reset(ctx, b.identity, in.IdempotencyKey, in.Scope)
}

// ResetCredential removes every registered key of in.Identity.
func (b *Bus) ResetCredential(ctx context.Context, in ab2bus.ResetCredentialInput) (ab2bus.ResetCredentialResult, error) {
	n, err := b.identities().ResetCredentialAs(ctx, b.identity, in.Identity, in.IdempotencyKey)
	if err != nil {
		return ab2bus.ResetCredentialResult{}, err
	}
	return ab2bus.ResetCredentialResult{Identity: in.Identity, KeysRemoved: n}, nil
}
