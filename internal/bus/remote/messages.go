package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Send posts the message; the idempotency key travels as the header.
//
// An empty channel is the one name this method judges itself: core Send answers
// validation for it, while the wire's empty path segment is the router's 404
// not_found, so the request is not sent (the plan's empty-name contingency, a
// Decision 80 departure limited to the empty name).
// pending Eric: Milestone 4 item N (A: keep the guard; B: core answers
// not_found for an empty channel and the guard goes).
func (b *Bus) Send(ctx context.Context, in ab2bus.SendInput) (ab2bus.SendResult, error) {
	if in.Channel == "" {
		return ab2bus.SendResult{}, ab2bus.Errorf(ab2bus.CodeValidation, false, "channel is required")
	}
	resp, err := b.c.SendMessageWithResponse(ctx, in.Channel,
		&gen.SendMessageParams{
			IdempotencyKey: key(in.IdempotencyKey),
		},
		gen.SendRequest{
			Content: in.Content, Subject: strPtr(in.Subject), Type: strPtr(in.Type),
			ReplyTo: in.ReplyTo, Metadata: mapPtr(in.Metadata), Tags: slicePtr(in.Tags),
			Session: strPtr(in.Session),
		})
	if err != nil {
		return ab2bus.SendResult{}, errorOf(ctx, 0, nil, nil, err)
	}
	if e := errorOf(ctx, resp.StatusCode(), resp.ApplicationproblemJSONDefault, resp.Body, nil); e != nil {
		return ab2bus.SendResult{}, e
	}
	if resp.JSON201 == nil {
		return ab2bus.SendResult{}, errorOf(ctx, 500, nil, resp.Body, nil)
	}
	return ab2bus.SendResult{Seq: resp.JSON201.Seq, MemoryID: resp.JSON201.MemoryId}, nil
}

// History reads a window of the channel; count 0 is absent.
func (b *Bus) History(ctx context.Context, in ab2bus.HistoryInput) ([]ab2bus.Message, error) {
	p := &gen.HistoryParams{Before: in.Before, After: in.After, Tags: queryTags(in.Tags)}
	if in.Count != 0 {
		p.Count = ptr(in.Count)
	}
	resp, err := b.c.HistoryWithResponse(ctx, in.Channel, p)
	if err != nil {
		return nil, errorOf(ctx, 0, nil, nil, err)
	}
	if e := errorOf(ctx, resp.StatusCode(), resp.ApplicationproblemJSONDefault, resp.Body, nil); e != nil {
		return nil, e
	}
	if resp.JSON200 == nil {
		return nil, errorOf(ctx, 500, nil, resp.Body, nil)
	}
	out := make([]ab2bus.Message, len(*resp.JSON200))
	for i, m := range *resp.JSON200 {
		out[i] = fromGenMessage(m)
	}
	return out, nil
}

// Search queries readable channels; empty strings and zero counts are absent.
func (b *Bus) Search(ctx context.Context, in ab2bus.SearchInput) (ab2bus.SearchResult, error) {
	p := &gen.SearchParams{
		Q: ptr(in.Query), Channel: strPtr(in.Channel), Sender: strPtr(in.Sender),
		Since: in.Since, Until: in.Until, Thread: in.Thread, Tags: queryTags(in.Tags),
		Cursor: strPtr(in.Cursor),
	}
	if in.Mode != "" {
		p.Mode = ptr(gen.SearchParamsMode(in.Mode))
	}
	if in.Count != 0 {
		p.Count = ptr(in.Count)
	}
	resp, err := b.c.SearchWithResponse(ctx, p)
	if err != nil {
		return ab2bus.SearchResult{}, errorOf(ctx, 0, nil, nil, err)
	}
	if e := errorOf(ctx, resp.StatusCode(), resp.ApplicationproblemJSONDefault, resp.Body, nil); e != nil {
		return ab2bus.SearchResult{}, e
	}
	if resp.JSON200 == nil {
		return ab2bus.SearchResult{}, errorOf(ctx, 500, nil, resp.Body, nil)
	}
	out := ab2bus.SearchResult{
		Next: deref(resp.JSON200.Next), SemanticUnavailable: deref(resp.JSON200.SemanticUnavailable),
		Hits: make([]ab2bus.SearchHit, len(resp.JSON200.Hits)),
	}
	for i, h := range resp.JSON200.Hits {
		out.Hits[i] = ab2bus.SearchHit{Message: fromGenMessage(h.Message), Score: h.Score}
	}
	return out, nil
}

// queryTags is the tags query parameter: nil for an empty list. A
// form/explode:false query array cannot encode the empty set (the validator
// rejects "tags="), and the core reads an empty tag list as no filter, so the
// empty list travels as an absent parameter (a wire-encoding rule).
func queryTags(tags []string) *[]string {
	if len(tags) == 0 {
		return nil
	}
	return &tags
}
