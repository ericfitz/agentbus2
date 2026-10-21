package server

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Peek answers the read-only pending counts (Decision (Eric, 2026-10-21),
// Milestone 6 item 2). It takes no Idempotency-Key: it writes nothing.
func (a *api) Peek(ctx context.Context, req gen.PeekRequestObject) (gen.PeekResponseObject, error) {
	r, err := a.delivery.Peek(ctx, IdentityFrom(ctx), ab2bus.PeekInput{
		Session: req.Session, IncludeOwn: derefOr(req.Params.IncludeOwn),
	})
	if err != nil {
		return nil, err
	}
	out := gen.Peek{Count: r.Count}
	out.Channels = make([]struct {
		Channel string `json:"channel"`
		Count   int    `json:"count"`
	}, 0, len(r.Channels))
	for _, c := range r.Channels {
		out.Channels = append(out.Channels, struct {
			Channel string `json:"channel"`
			Count   int    `json:"count"`
		}{Channel: c.Channel, Count: c.Count})
	}
	return gen.Peek200JSONResponse(out), nil
}
