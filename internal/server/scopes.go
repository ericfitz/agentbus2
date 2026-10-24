package server

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// The scope and scoped-identity handlers (Slack bridge spec 4.1, 4.2,
// Decision 17). Every one is administrator only, enforced by the core.

func scopeToGen(s bus.Scope) gen.Scope {
	out := gen.Scope{
		Name: s.Name, State: gen.ScopeState(s.State),
		CreatedAt: s.CreatedAt, LastActivity: s.LastActivity,
	}
	if s.Retire != "" {
		r := gen.ScopeRetire(s.Retire)
		out.Retire = &r
	}
	if s.DoneAt != 0 {
		out.DoneAt = &s.DoneAt
	}
	// Channels and Identities are filled by Get only: nil means "not listed",
	// an empty slice an empty scope.
	if s.Channels != nil {
		out.Channels = &s.Channels
	}
	if s.Identities != nil {
		out.Identities = &s.Identities
	}
	return out
}

func (a *api) CreateScope(ctx context.Context, req gen.CreateScopeRequestObject) (gen.CreateScopeResponseObject, error) {
	sc, err := a.scopes.Create(ctx, IdentityFrom(ctx), bus.CreateScopeInput{Name: req.Body.Name, IdempotencyKey: idemKey(req.Params.IdempotencyKey)})
	if err != nil {
		return nil, err
	}
	return gen.CreateScope200JSONResponse(scopeToGen(sc)), nil
}

func (a *api) ListScopes(ctx context.Context, _ gen.ListScopesRequestObject) (gen.ListScopesResponseObject, error) {
	all, err := a.scopes.List(ctx, IdentityFrom(ctx))
	if err != nil {
		return nil, err
	}
	out := gen.ScopeList{Scopes: make([]gen.Scope, len(all))}
	for i, sc := range all {
		out.Scopes[i] = scopeToGen(sc)
	}
	return gen.ListScopes200JSONResponse(out), nil
}

func (a *api) GetScope(ctx context.Context, req gen.GetScopeRequestObject) (gen.GetScopeResponseObject, error) {
	sc, err := a.scopes.Get(ctx, IdentityFrom(ctx), req.Scope)
	if err != nil {
		return nil, err
	}
	return gen.GetScope200JSONResponse(scopeToGen(sc)), nil
}

// DoneScope passes the retire mode through; the document's enum has already
// refused any other value, and the core refuses it again.
func (a *api) DoneScope(ctx context.Context, req gen.DoneScopeRequestObject) (gen.DoneScopeResponseObject, error) {
	sc, err := a.scopes.Done(ctx, IdentityFrom(ctx), bus.DoneScopeInput{
		Name: req.Scope, Retire: bus.RetireMode(req.Body.Retire), IdempotencyKey: idemKey(req.Params.IdempotencyKey),
	})
	if err != nil {
		return nil, err
	}
	return gen.DoneScope200JSONResponse(scopeToGen(sc)), nil
}

// CreateAgentIdentity creates the agent identity of the body, optionally in a
// scope, with its one-time enrollment token. The response carries the token,
// so it is never replayed from a receipt: the operation takes no key.
func (a *api) CreateAgentIdentity(ctx context.Context, req gen.CreateAgentIdentityRequestObject) (gen.CreateAgentIdentityResponseObject, error) {
	in := bus.CreateAgentIdentityInput{Name: req.Body.Name}
	if req.Body.Scope != nil {
		in.Scope = *req.Body.Scope
	}
	res, err := (identities.Service{Core: a.core}).CreateAgentIdentityAs(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err
	}
	out := gen.AgentIdentity{Identity: res.Identity, Created: res.Created, EnrollmentToken: res.EnrollmentToken, ExpiresAt: res.ExpiresAt}
	if res.Scope != "" {
		out.Scope = &res.Scope
	}
	return gen.CreateAgentIdentity200JSONResponse(out), nil
}
