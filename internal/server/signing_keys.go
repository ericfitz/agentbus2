package server

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// SigningKeys is the seam to the signing key rules (issue #6): the server
// imports no key provider. Each method receives the authenticated identity
// and checks that it is an administrator.
type SigningKeys interface {
	ListSigningKeys(ctx context.Context, admin string) ([]bus.SigningKey, error)
	RotateSigningKey(ctx context.Context, admin string, in bus.RotateSigningKeyInput) (bus.SigningKeyRotation, error)
}

// signingKeys is Options.SigningKeys, or the refusing stand-in.
func (a *api) signingKeys() SigningKeys {
	if a.opts.SigningKeys == nil {
		return noSigningKeys{}
	}
	return a.opts.SigningKeys
}

// noSigningKeys answers every call unavailable (not retryable) with
// bus.SigningKeysNotConfigured.
type noSigningKeys struct{}

func signingKeysNotConfigured() error {
	return bus.Errorf(bus.CodeUnavailable, false, "%s", bus.SigningKeysNotConfigured)
}

func (noSigningKeys) ListSigningKeys(context.Context, string) ([]bus.SigningKey, error) {
	return nil, signingKeysNotConfigured()
}

func (noSigningKeys) RotateSigningKey(context.Context, string, bus.RotateSigningKeyInput) (bus.SigningKeyRotation, error) {
	return bus.SigningKeyRotation{}, signingKeysNotConfigured()
}

func signingKeyToGen(k bus.SigningKey) gen.SigningKey {
	return gen.SigningKey{
		Kid: k.KID, State: gen.SigningKeyState(k.State), Provider: gen.SigningKeyProvider(k.Provider),
		ProviderRef: k.ProviderRef, Algorithm: gen.SigningKeyAlgorithm(k.Algorithm),
		CreatedAt: k.CreatedAt, RetiredAt: k.RetiredAt, RemoveAfter: k.RemoveAfter,
	}
}

func (a *api) ListSigningKeys(ctx context.Context, _ gen.ListSigningKeysRequestObject) (gen.ListSigningKeysResponseObject, error) {
	ks, err := a.signingKeys().ListSigningKeys(ctx, IdentityFrom(ctx))
	if err != nil {
		return nil, err
	}
	// keys is required on the wire: an empty array, never null.
	out := gen.SigningKeyList{Keys: make([]gen.SigningKey, len(ks))}
	for i, k := range ks {
		out.Keys[i] = signingKeyToGen(k)
	}
	return gen.ListSigningKeys200JSONResponse(out), nil
}

func (a *api) RotateSigningKey(ctx context.Context, req gen.RotateSigningKeyRequestObject) (gen.RotateSigningKeyResponseObject, error) {
	in := bus.RotateSigningKeyInput{IdempotencyKey: idemKey(req.Params.IdempotencyKey)}
	if req.Params.RevokePrevious != nil {
		in.RevokePrevious = *req.Params.RevokePrevious
	}
	res, err := a.signingKeys().RotateSigningKey(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err
	}
	out := gen.SigningKeyRotation{Active: signingKeyToGen(res.Active), Revoked: res.Revoked}
	if res.Previous.KID != "" {
		p := signingKeyToGen(res.Previous)
		out.Previous = &p
	}
	return gen.RotateSigningKey200JSONResponse(out), nil
}
