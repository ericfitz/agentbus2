package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
)

func (b *Bus) scopes() scopes.Service { return scopes.Service{Core: b.c} }

// CreateScope makes an open scope (administrator only).
func (b *Bus) CreateScope(ctx context.Context, in ab2bus.CreateScopeInput) (ab2bus.Scope, error) {
	return b.scopes().Create(ctx, b.identity, in)
}

// ListScopes lists the open and done scopes.
func (b *Bus) ListScopes(ctx context.Context) ([]ab2bus.Scope, error) {
	return b.scopes().List(ctx, b.identity)
}

// GetScope returns one scope with its channel and identity names.
func (b *Bus) GetScope(ctx context.Context, name string) (ab2bus.Scope, error) {
	return b.scopes().Get(ctx, b.identity, name)
}

// DoneScope marks a scope done with its retirement mode.
func (b *Bus) DoneScope(ctx context.Context, in ab2bus.DoneScopeInput) (ab2bus.Scope, error) {
	return b.scopes().Done(ctx, b.identity, in)
}

// CreateAgentIdentity creates an agent identity, optionally in a scope, and
// its one-time enrollment token.
func (b *Bus) CreateAgentIdentity(ctx context.Context, in ab2bus.CreateAgentIdentityInput) (ab2bus.AgentIdentity, error) {
	return b.identities().CreateAgentIdentityAs(ctx, b.identity, in)
}

// The bridge operations: local mode has no Slack.
func errBridgeNotConfigured() error {
	return ab2bus.Errorf(ab2bus.CodeUnavailable, false, "%s", ab2bus.BridgeNotConfigured)
}

func (b *Bus) CreateBridge(context.Context, ab2bus.CreateBridgeInput) (ab2bus.BridgeCreated, error) {
	return ab2bus.BridgeCreated{}, errBridgeNotConfigured()
}

func (b *Bus) ListBridges(context.Context) ([]ab2bus.Bridge, error) {
	return nil, errBridgeNotConfigured()
}

func (b *Bus) GetBridge(context.Context, string) (ab2bus.Bridge, error) {
	return ab2bus.Bridge{}, errBridgeNotConfigured()
}

func (b *Bus) RemoveBridge(context.Context, ab2bus.RemoveBridgeInput) error {
	return errBridgeNotConfigured()
}
