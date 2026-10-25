package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

// Health is the server health document at the caller's tier.
func (b *Bus) Health(ctx context.Context) (ab2bus.Health, error) {
	r, err := b.c.HealthWithResponse(ctx)
	if err != nil {
		return ab2bus.Health{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.Health{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.Health{}, unparsable(r.StatusCode())
	}
	return fromGenHealth(*r.JSON200)
}
