package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Reset sends the scope query parameter only when in.Scope is set: the
// document's default is data, so "" and data are the same request.
func (b *Bus) Reset(ctx context.Context, in ab2bus.ResetInput) error {
	p := &gen.AdminResetParams{IdempotencyKey: key(in.IdempotencyKey)}
	if in.Scope != "" {
		s := gen.AdminResetParamsScope(in.Scope)
		p.Scope = &s
	}
	r, err := b.c.AdminResetWithResponse(ctx, p)
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	return errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil)
}

// ResetCredential removes every registered key of in.Identity.
func (b *Bus) ResetCredential(ctx context.Context, in ab2bus.ResetCredentialInput) (ab2bus.ResetCredentialResult, error) {
	r, err := b.c.ResetCredentialWithResponse(ctx, in.Identity, &gen.ResetCredentialParams{IdempotencyKey: key(in.IdempotencyKey)})
	if err != nil {
		return ab2bus.ResetCredentialResult{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.ResetCredentialResult{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.ResetCredentialResult{}, unparsable(r.StatusCode())
	}
	return ab2bus.ResetCredentialResult{Identity: r.JSON200.Identity, KeysRemoved: r.JSON200.KeysRemoved}, nil
}
