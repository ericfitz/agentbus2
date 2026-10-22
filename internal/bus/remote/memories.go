package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// mtResponse is the part of every generated response the error mapping reads.
type mtResponse interface {
	StatusCode() int
	GetBody() []byte
	GetApplicationproblemJSONDefault() *gen.Problem
}

// mtCheck maps a generated response (or the transport error) to nil or a Bus
// error. ok is whether the typed success member for the expected status is
// set; a 2xx without it is an unparsable success and reported as internal.
func mtCheck[R mtResponse](ctx context.Context, r R, err error, ok func(R) bool) error {
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	if e := errorOf(ctx, r.StatusCode(), r.GetApplicationproblemJSONDefault(), r.GetBody(), nil); e != nil {
		return e
	}
	if !ok(r) {
		return unparsable(r.StatusCode())
	}
	return nil
}

func (b *Bus) GetMemory(ctx context.Context, id int64) (ab2bus.Message, error) {
	r, err := b.c.GetMemoryWithResponse(ctx, id)
	if err := mtCheck(ctx, r, err, func(r *gen.GetMemoryResponse) bool { return r.JSON200 != nil }); err != nil {
		return ab2bus.Message{}, err
	}
	return fromGenMessage(*r.JSON200), nil
}

func (b *Bus) Edit(ctx context.Context, in ab2bus.EditMemoryInput) (ab2bus.EditResult, error) {
	body := gen.EditMemoryRequest{
		Content: in.Content, Subject: in.Subject, Type: in.Type,
		Metadata: in.Metadata, Tags: slicePtr(in.Tags), IfRevision: int64Ptr(in.IfRevision),
	}
	r, err := b.c.EditMemoryWithResponse(ctx, in.ID, &gen.EditMemoryParams{IdempotencyKey: key(in.IdempotencyKey)}, body)
	if err := mtCheck(ctx, r, err, func(r *gen.EditMemoryResponse) bool { return r.JSON201 != nil }); err != nil {
		return ab2bus.EditResult{}, err
	}
	e := r.JSON201
	return ab2bus.EditResult{Seq: e.Seq, MemoryID: e.MemoryId, Revision: e.Revision, Replaced: e.Replaced}, nil
}

func (b *Bus) Delete(ctx context.Context, in ab2bus.DeleteMemoryInput) error {
	r, err := b.c.DeleteMemoryWithResponse(ctx, in.ID, &gen.DeleteMemoryParams{IdempotencyKey: key(in.IdempotencyKey)})
	return mtCheck(ctx, r, err, func(*gen.DeleteMemoryResponse) bool { return true })
}

func (b *Bus) Revisions(ctx context.Context, id int64) ([]ab2bus.Message, error) {
	r, err := b.c.ListMemoryRevisionsWithResponse(ctx, id)
	if err := mtCheck(ctx, r, err, func(r *gen.ListMemoryRevisionsResponse) bool { return r.JSON200 != nil }); err != nil {
		return nil, err
	}
	out := make([]ab2bus.Message, len(*r.JSON200))
	for i, m := range *r.JSON200 {
		out[i] = fromGenMessage(m)
	}
	return out, nil
}
