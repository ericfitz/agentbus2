package server

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

func (a *api) GetMemory(ctx context.Context, r gen.GetMemoryRequestObject) (gen.GetMemoryResponseObject, error) {
	m, err := a.memories.Get(ctx, IdentityFrom(ctx), r.Id)
	if err != nil {
		return nil, err
	}
	return gen.GetMemory200JSONResponse(toGenMessage(m)), nil
}

func (a *api) EditMemory(ctx context.Context, r gen.EditMemoryRequestObject) (gen.EditMemoryResponseObject, error) {
	if r.Body == nil {
		return nil, ab2bus.Errorf(ab2bus.CodeValidation, false, "the request body is required")
	}
	b := r.Body
	in := ab2bus.EditMemoryInput{
		ID: r.Id, Subject: b.Subject, Content: b.Content, Type: b.Type, Metadata: b.Metadata,
		IdempotencyKey: idemKey(r.Params.IdempotencyKey),
	}
	if b.IfRevision != nil {
		in.IfRevision = *b.IfRevision
	}
	if b.Tags != nil {
		// A present empty list stays non-nil: it clears; absent keeps.
		in.Tags = append([]string{}, *b.Tags...)
	}
	res, err := a.memories.Edit(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err
	}
	return gen.EditMemory201JSONResponse{Seq: res.Seq, MemoryId: res.MemoryID, Revision: res.Revision, Replaced: res.Replaced}, nil
}

func (a *api) ListMemoryRevisions(ctx context.Context, r gen.ListMemoryRevisionsRequestObject) (gen.ListMemoryRevisionsResponseObject, error) {
	ms, err := a.memories.Revisions(ctx, IdentityFrom(ctx), r.Id)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListMemoryRevisions200JSONResponse, len(ms))
	for i, m := range ms {
		out[i] = toGenMessage(m)
	}
	return out, nil
}

func (a *api) DeleteMemory(ctx context.Context, r gen.DeleteMemoryRequestObject) (gen.DeleteMemoryResponseObject, error) {
	err := a.memories.Delete(ctx, IdentityFrom(ctx), ab2bus.DeleteMemoryInput{ID: r.Id, IdempotencyKey: idemKey(r.Params.IdempotencyKey)})
	if err != nil {
		return nil, err
	}
	return gen.DeleteMemory204Response{}, nil
}
