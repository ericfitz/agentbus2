package server

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// AdminReset wipes the bus data (Decision 48, Eric 2026-10-21 Milestone 5 item
// 1). An absent scope is data, the document's default; the validator has
// already refused any value outside the enum.
func (a *api) AdminReset(ctx context.Context, req gen.AdminResetRequestObject) (gen.AdminResetResponseObject, error) {
	scope := bus.ResetData
	if req.Params.Scope != nil {
		scope = bus.ResetScope(*req.Params.Scope)
	}
	if err := (identities.Service{Core: a.core}).Reset(ctx, IdentityFrom(ctx), idemKey(req.Params.IdempotencyKey), scope); err != nil {
		return nil, err
	}
	return gen.AdminReset204Response{}, nil
}

// ResetCredential removes every registered key of the path identity
// (Decisions 58, 73). A bearer already minted stays valid until it expires.
func (a *api) ResetCredential(ctx context.Context, req gen.ResetCredentialRequestObject) (gen.ResetCredentialResponseObject, error) {
	n, err := (identities.Service{Core: a.core}).ResetCredentialAs(ctx, IdentityFrom(ctx), req.Identity, idemKey(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return gen.ResetCredential200JSONResponse{Identity: req.Identity, KeysRemoved: n}, nil
}
