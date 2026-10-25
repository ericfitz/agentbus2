package server

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/health"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Health answers GET /v1/health: the document tiered by the caller (Decision
// 73). The operation's security is optional, so the validator never refuses
// it; a credential the Authenticator rejected is a 401 here (spec 2026-10-25,
// agent decision 2), never the probe tier.
func (a *api) Health(ctx context.Context, _ gen.HealthRequestObject) (gen.HealthResponseObject, error) {
	if err := authErrorFrom(ctx); err != nil {
		return nil, core.E(bus.CodeUnauthenticated, "invalid credential: %s", err.Error())
	}
	h, err := health.Service{Core: a.core}.Health(ctx, IdentityFrom(ctx), a.version)
	if err != nil {
		return nil, err
	}
	return gen.Health200JSONResponse(healthToGen(h)), nil
}
