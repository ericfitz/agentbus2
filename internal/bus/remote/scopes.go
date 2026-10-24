package remote

import (
	"context"
	"fmt"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// unknownWire is the internal error of a closed enumeration member this
// client does not know (a server speaking another protocol).
func unknownWire(field, v string) error {
	return &ab2bus.Error{Code: ab2bus.CodeInternal, Message: fmt.Sprintf("server sent unknown %s %q", field, v)}
}

// fromGenScope converts a wire scope, refusing a state or retire mode outside
// the closed sets.
func fromGenScope(g gen.Scope) (ab2bus.Scope, error) {
	out := ab2bus.Scope{
		Name: g.Name, CreatedAt: g.CreatedAt, DoneAt: deref(g.DoneAt), LastActivity: g.LastActivity,
		Channels: deref(g.Channels), Identities: deref(g.Identities),
	}
	switch s := ab2bus.ScopeState(g.State); s {
	case ab2bus.ScopeOpen, ab2bus.ScopeDone, ab2bus.ScopeRetired:
		out.State = s
	default:
		return ab2bus.Scope{}, unknownWire("state", string(g.State))
	}
	if g.Retire != nil {
		switch m := ab2bus.RetireMode(*g.Retire); m {
		case ab2bus.RetireWithBridge, ab2bus.RetireNormal:
			out.Retire = m
		default:
			return ab2bus.Scope{}, unknownWire("retire", string(*g.Retire))
		}
	}
	return out, nil
}

// fromGenBridge converts a wire bridge, refusing a state outside the closed
// set.
func fromGenBridge(g gen.Bridge) (ab2bus.Bridge, error) {
	out := ab2bus.Bridge{
		Channel: g.Channel, Scope: g.Scope, SlackChannelID: deref(g.SlackChannelId), SlackName: g.SlackName,
		CreatedAt: g.CreatedAt, ArchivedAt: deref(g.ArchivedAt),
	}
	switch s := ab2bus.BridgeState(g.State); s {
	case ab2bus.BridgeCreating, ab2bus.BridgeLive, ab2bus.BridgeArchived:
		out.State = s
	default:
		return ab2bus.Bridge{}, unknownWire("state", string(g.State))
	}
	return out, nil
}

// CreateScope makes an open scope.
func (b *Bus) CreateScope(ctx context.Context, in ab2bus.CreateScopeInput) (ab2bus.Scope, error) {
	r, err := b.c.CreateScopeWithResponse(ctx, &gen.CreateScopeParams{IdempotencyKey: key(in.IdempotencyKey)}, gen.CreateScopeJSONRequestBody{Name: in.Name})
	if err != nil {
		return ab2bus.Scope{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.Scope{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.Scope{}, unparsable(r.StatusCode())
	}
	return fromGenScope(*r.JSON200)
}

// ListScopes lists the open and done scopes.
func (b *Bus) ListScopes(ctx context.Context) ([]ab2bus.Scope, error) {
	r, err := b.c.ListScopesWithResponse(ctx)
	if err != nil {
		return nil, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, unparsable(r.StatusCode())
	}
	out := make([]ab2bus.Scope, len(r.JSON200.Scopes))
	for i, g := range r.JSON200.Scopes {
		sc, err := fromGenScope(g)
		if err != nil {
			return nil, err
		}
		out[i] = sc
	}
	return out, nil
}

// GetScope returns one scope with its channel and identity names.
func (b *Bus) GetScope(ctx context.Context, name string) (ab2bus.Scope, error) {
	r, err := b.c.GetScopeWithResponse(ctx, name)
	if err != nil {
		return ab2bus.Scope{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.Scope{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.Scope{}, unparsable(r.StatusCode())
	}
	return fromGenScope(*r.JSON200)
}

// DoneScope marks a scope done. The retire mode is sent as given: the server
// refuses a value outside the enum with validation.
func (b *Bus) DoneScope(ctx context.Context, in ab2bus.DoneScopeInput) (ab2bus.Scope, error) {
	r, err := b.c.DoneScopeWithResponse(ctx, in.Name, &gen.DoneScopeParams{IdempotencyKey: key(in.IdempotencyKey)},
		gen.DoneScopeJSONRequestBody{Retire: gen.DoneScopeRequestRetire(in.Retire)})
	if err != nil {
		return ab2bus.Scope{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.Scope{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.Scope{}, unparsable(r.StatusCode())
	}
	return fromGenScope(*r.JSON200)
}

// CreateAgentIdentity creates an agent identity, optionally in a scope, and
// its one-time enrollment token.
func (b *Bus) CreateAgentIdentity(ctx context.Context, in ab2bus.CreateAgentIdentityInput) (ab2bus.AgentIdentity, error) {
	body := gen.CreateAgentIdentityJSONRequestBody{Name: in.Name}
	if in.Scope != "" {
		body.Scope = &in.Scope
	}
	r, err := b.c.CreateAgentIdentityWithResponse(ctx, body)
	if err != nil {
		return ab2bus.AgentIdentity{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.AgentIdentity{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.AgentIdentity{}, unparsable(r.StatusCode())
	}
	g := r.JSON200
	return ab2bus.AgentIdentity{Identity: g.Identity, Scope: deref(g.Scope), Created: g.Created, EnrollmentToken: g.EnrollmentToken, ExpiresAt: g.ExpiresAt}, nil
}

// CreateBridge bridges a channel to a new private Slack channel.
func (b *Bus) CreateBridge(ctx context.Context, in ab2bus.CreateBridgeInput) (ab2bus.BridgeCreated, error) {
	body := gen.CreateBridgeJSONRequestBody{Channel: in.Channel}
	if in.SlackName != "" {
		body.SlackName = &in.SlackName
	}
	r, err := b.c.CreateBridgeWithResponse(ctx, body)
	if err != nil {
		return ab2bus.BridgeCreated{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.BridgeCreated{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.BridgeCreated{}, unparsable(r.StatusCode())
	}
	br, err := fromGenBridge(r.JSON200.Bridge)
	if err != nil {
		return ab2bus.BridgeCreated{}, err
	}
	out := ab2bus.BridgeCreated{Bridge: br, NotInvited: make([]ab2bus.BridgeNotInvited, len(r.JSON200.NotInvited))}
	for i, n := range r.JSON200.NotInvited {
		out.NotInvited[i] = ab2bus.BridgeNotInvited{Identity: n.Identity, Reason: n.Reason}
	}
	return out, nil
}

// ListBridges lists every bridge.
func (b *Bus) ListBridges(ctx context.Context) ([]ab2bus.Bridge, error) {
	r, err := b.c.ListBridgesWithResponse(ctx)
	if err != nil {
		return nil, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, unparsable(r.StatusCode())
	}
	out := make([]ab2bus.Bridge, len(r.JSON200.Bridges))
	for i, g := range r.JSON200.Bridges {
		br, err := fromGenBridge(g)
		if err != nil {
			return nil, err
		}
		out[i] = br
	}
	return out, nil
}

// GetBridge returns the bridge of a channel.
func (b *Bus) GetBridge(ctx context.Context, channel string) (ab2bus.Bridge, error) {
	r, err := b.c.GetBridgeWithResponse(ctx, channel)
	if err != nil {
		return ab2bus.Bridge{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.Bridge{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.Bridge{}, unparsable(r.StatusCode())
	}
	return fromGenBridge(*r.JSON200)
}

// RemoveBridge archives the bridge of a channel.
func (b *Bus) RemoveBridge(ctx context.Context, in ab2bus.RemoveBridgeInput) error {
	r, err := b.c.RemoveBridgeWithResponse(ctx, in.Channel)
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	return errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil)
}
