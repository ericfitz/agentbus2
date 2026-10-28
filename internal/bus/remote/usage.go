package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Usage is the administrator's database usage reading (Decision 48).
func (b *Bus) Usage(ctx context.Context) (ab2bus.Usage, error) {
	r, err := b.c.AdminUsageWithResponse(ctx)
	if err != nil {
		return ab2bus.Usage{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.Usage{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.Usage{}, unparsable(r.StatusCode())
	}
	return fromGenUsage(*r.JSON200)
}

// fromGenUsage maps the wire usage reading; a tier outside the five is an
// internal error naming the member.
func fromGenUsage(g gen.Usage) (ab2bus.Usage, error) {
	tier := ab2bus.PressureTier(g.Tier)
	if !ab2bus.ValidPressureTier(tier) {
		return ab2bus.Usage{}, unknownEnum("storage.tier", string(g.Tier))
	}
	return ab2bus.Usage{
		Bytes: g.Bytes, BudgetBytes: g.BudgetBytes, Evictable: g.Evictable,
		DiskBytes: g.DiskBytes, DiskRatio: g.DiskRatio, Tier: tier, Notice: deref(g.Notice),
	}, nil
}
