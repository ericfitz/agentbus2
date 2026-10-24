package server

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// SendMessage maps the request to messaging.Service.Send; the Idempotency-Key
// header is the receipt key (Decision 66).
func (a *api) SendMessage(ctx context.Context, req gen.SendMessageRequestObject) (gen.SendMessageResponseObject, error) {
	if req.Body == nil {
		return nil, bus.Errorf(bus.CodeValidation, false, "the request body is required")
	}
	b := req.Body
	res, err := a.messaging.Send(ctx, IdentityFrom(ctx), bus.SendInput{
		Channel:        req.Channel,
		Subject:        derefOr(b.Subject),
		Content:        b.Content,
		Type:           derefOr(b.Type),
		ReplyTo:        b.ReplyTo,
		Metadata:       derefOr(b.Metadata),
		Tags:           derefOr(b.Tags),
		Session:        derefOr(b.Session),
		IdempotencyKey: derefOr(req.Params.IdempotencyKey),
	})
	if err != nil {
		return nil, err
	}
	return gen.SendMessage201JSONResponse{Seq: res.Seq, MemoryId: res.MemoryID}, nil
}

// History maps the query to messaging.Service.History; count absent is 0.
func (a *api) History(ctx context.Context, req gen.HistoryRequestObject) (gen.HistoryResponseObject, error) {
	p := req.Params
	msgs, err := a.messaging.History(ctx, IdentityFrom(ctx), bus.HistoryInput{
		Channel: req.Channel, Before: p.Before, After: p.After,
		Count: derefOr(p.Count), Tags: derefOr(p.Tags), Thread: p.Thread,
	})
	if err != nil {
		return nil, err
	}
	out := make(gen.History200JSONResponse, len(msgs))
	for i, m := range msgs {
		out[i] = toGenMessage(m)
	}
	return out, nil
}

// Search maps the query to search.Service.Search.
func (a *api) Search(ctx context.Context, req gen.SearchRequestObject) (gen.SearchResponseObject, error) {
	p := req.Params
	in := bus.SearchInput{
		Query: derefOr(p.Q), Channel: derefOr(p.Channel), Sender: derefOr(p.Sender),
		Since: p.Since, Until: p.Until, Thread: p.Thread, Tags: derefOr(p.Tags),
		Count: derefOr(p.Count), Cursor: derefOr(p.Cursor),
	}
	if p.Mode != nil {
		in.Mode = string(*p.Mode)
	}
	res, err := a.search.Search(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err
	}
	out := gen.Search200JSONResponse{Hits: make([]gen.SearchHit, len(res.Hits))}
	for i, h := range res.Hits {
		out.Hits[i] = gen.SearchHit{Message: toGenMessage(h.Message), Score: h.Score}
	}
	if res.Next != "" {
		n := res.Next
		out.Next = &n
	}
	if res.SemanticUnavailable {
		t := true
		out.SemanticUnavailable = &t
	}
	return out, nil
}
