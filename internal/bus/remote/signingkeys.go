package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// fromGenSigningKey converts a wire key, refusing a state, provider or
// algorithm outside the closed sets.
func fromGenSigningKey(g gen.SigningKey) (ab2bus.SigningKey, error) {
	if !g.State.Valid() {
		return ab2bus.SigningKey{}, unknownWire("state", string(g.State))
	}
	if !g.Provider.Valid() {
		return ab2bus.SigningKey{}, unknownWire("provider", string(g.Provider))
	}
	if !g.Algorithm.Valid() {
		return ab2bus.SigningKey{}, unknownWire("algorithm", string(g.Algorithm))
	}
	return ab2bus.SigningKey{
		KID: g.Kid, State: string(g.State), Provider: string(g.Provider), ProviderRef: g.ProviderRef,
		Algorithm: string(g.Algorithm), CreatedAt: g.CreatedAt, RetiredAt: g.RetiredAt, RemoveAfter: g.RemoveAfter,
	}, nil
}

// ListSigningKeys lists the server's signing key set, the active key first.
func (b *Bus) ListSigningKeys(ctx context.Context) ([]ab2bus.SigningKey, error) {
	r, err := b.c.ListSigningKeysWithResponse(ctx)
	if err != nil {
		return nil, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, unparsable(r.StatusCode())
	}
	out := make([]ab2bus.SigningKey, 0, len(r.JSON200.Keys))
	for _, g := range r.JSON200.Keys {
		k, err := fromGenSigningKey(g)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

// RotateSigningKey rotates the active signing key.
func (b *Bus) RotateSigningKey(ctx context.Context, in ab2bus.RotateSigningKeyInput) (ab2bus.SigningKeyRotation, error) {
	params := &gen.RotateSigningKeyParams{IdempotencyKey: key(in.IdempotencyKey)}
	if in.RevokePrevious {
		params.RevokePrevious = &in.RevokePrevious
	}
	r, err := b.c.RotateSigningKeyWithResponse(ctx, params)
	if err != nil {
		return ab2bus.SigningKeyRotation{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.SigningKeyRotation{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.SigningKeyRotation{}, unparsable(r.StatusCode())
	}
	out := ab2bus.SigningKeyRotation{Revoked: r.JSON200.Revoked}
	if out.Active, err = fromGenSigningKey(r.JSON200.Active); err != nil {
		return ab2bus.SigningKeyRotation{}, err
	}
	if r.JSON200.Previous != nil {
		if out.Previous, err = fromGenSigningKey(*r.JSON200.Previous); err != nil {
			return ab2bus.SigningKeyRotation{}, err
		}
	}
	return out, nil
}
