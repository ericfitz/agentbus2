package server

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/gen"
)

func (a *api) AdminUsage(ctx context.Context, _ gen.AdminUsageRequestObject) (gen.AdminUsageResponseObject, error) {
	u, err := a.channels.Usage(ctx, IdentityFrom(ctx))
	if err != nil {
		return nil, err
	}
	return gen.AdminUsage200JSONResponse(usageToGen(u)), nil
}
