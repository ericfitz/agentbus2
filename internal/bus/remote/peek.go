package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Peek is GET /v1/sessions/{session}/peek; include_own is sent only when true.
func (b *Bus) Peek(ctx context.Context, in ab2bus.PeekInput) (ab2bus.PeekResult, error) {
	params := &gen.PeekParams{}
	if in.IncludeOwn {
		params.IncludeOwn = ptr(true)
	}
	r, err := b.c.PeekWithResponse(ctx, in.Session, params)
	if err != nil {
		return ab2bus.PeekResult{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.PeekResult{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.PeekResult{}, unparsable(r.StatusCode())
	}
	out := ab2bus.PeekResult{Count: r.JSON200.Count, Channels: make([]ab2bus.PeekChannel, 0, len(r.JSON200.Channels))}
	for _, c := range r.JSON200.Channels {
		out.Channels = append(out.Channels, ab2bus.PeekChannel{Channel: c.Channel, Count: c.Count})
	}
	return out, nil
}
