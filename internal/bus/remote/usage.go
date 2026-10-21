package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
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
	g := *r.JSON200
	return ab2bus.Usage{Bytes: g.Bytes, BudgetBytes: g.BudgetBytes, Evictable: g.Evictable, Notice: deref(g.Notice)}, nil
}
