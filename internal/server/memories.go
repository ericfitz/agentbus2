package server

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// memoryMessageToGen converts a memory revision to its wire form. It is local
// to this file (not shared with the send and history handlers) so that the
// aggregate tasks stay independent.
func memoryMessageToGen(m ab2bus.Message) gen.Message {
	out := gen.Message{
		Seq: m.Seq, Channel: m.Channel, Sender: m.Sender, Display: m.Display,
		CreatedAt: m.CreatedAt, Type: m.Type, Subject: m.Subject, Content: m.Content,
		ReplyTo: m.ReplyTo, MemoryId: m.MemoryID, Revision: m.Revision,
	}
	if m.Session != "" {
		s := m.Session
		out.Session = &s
	}
	if m.Metadata != nil {
		md := m.Metadata
		out.Metadata = &md
	}
	if m.Tags != nil {
		tg := m.Tags
		out.Tags = &tg
	}
	if m.MatchedTags != nil {
		mt := m.MatchedTags
		out.MatchedTags = &mt
	}
	return out
}

func mtIdemKey(k *string) string {
	if k == nil {
		return ""
	}
	return *k
}

func (a *api) GetMemory(ctx context.Context, r gen.GetMemoryRequestObject) (gen.GetMemoryResponseObject, error) {
	m, err := a.memories.Get(ctx, IdentityFrom(ctx), r.Id)
	if err != nil {
		return nil, err
	}
	return gen.GetMemory200JSONResponse(memoryMessageToGen(m)), nil
}

func (a *api) EditMemory(ctx context.Context, r gen.EditMemoryRequestObject) (gen.EditMemoryResponseObject, error) {
	if r.Body == nil {
		return nil, &ab2bus.Error{Code: ab2bus.CodeValidation, Message: "request body required"}
	}
	b := r.Body
	in := ab2bus.EditMemoryInput{
		ID: r.Id, Subject: b.Subject, Content: b.Content, IdempotencyKey: mtIdemKey(r.Params.IdempotencyKey),
	}
	if b.Type != nil {
		in.Type = *b.Type
	}
	if b.Metadata != nil {
		in.Metadata = *b.Metadata
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
		out[i] = memoryMessageToGen(m)
	}
	return out, nil
}

func (a *api) DeleteMemory(ctx context.Context, r gen.DeleteMemoryRequestObject) (gen.DeleteMemoryResponseObject, error) {
	err := a.memories.Delete(ctx, IdentityFrom(ctx), ab2bus.DeleteMemoryInput{ID: r.Id, IdempotencyKey: mtIdemKey(r.Params.IdempotencyKey)})
	if err != nil {
		return nil, err
	}
	return gen.DeleteMemory204Response{}, nil
}
