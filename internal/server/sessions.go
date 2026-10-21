package server

import (
	"context"
	"math"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Sessions, receive, watch and identities (Decisions 15, 16, 40, 42, 45, 68,
// 83). Every failure is returned as nil, err; the strict server's response
// error hook writes the problem (choice 4). Watch passes ctx straight to the
// service: the strict wrapper gives it r.Context(), so a client that goes
// away releases the handler.

func (a *api) OpenSession(ctx context.Context, req gen.OpenSessionRequestObject) (gen.OpenSessionResponseObject, error) {
	var in ab2bus.OpenSessionInput
	if b := req.Body; b != nil {
		in.Hint = derefOr(b.Hint)
		in.ResumeToken = derefOr(b.ResumeToken)
		for _, c := range derefOr(b.Cursors) {
			in.Cursors = append(in.Cursors, ab2bus.OfferedCursor{Channel: c.Channel, Seq: c.Seq})
		}
	}
	s, err := a.sessions.Open(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err
	}
	out := gen.Session{
		DisplayName: s.DisplayName, Id: s.ID, Identity: s.Identity,
		ResumeToken: s.ResumeToken, Resumed: s.Resumed,
		Pending:         make([]gen.PendingChannel, 0, len(s.Pending)),
		RejectedCursors: make([]gen.RejectedCursor, 0, len(s.RejectedCursors)),
	}
	for _, p := range s.Pending {
		out.Pending = append(out.Pending, gen.PendingChannel{Channel: p.Channel, Pending: p.Pending})
	}
	for _, r := range s.RejectedCursors {
		out.RejectedCursors = append(out.RejectedCursors, gen.RejectedCursor{Channel: r.Channel, Reason: gen.RejectedCursorReason(r.Reason)})
	}
	return gen.OpenSession201JSONResponse(out), nil
}

func (a *api) CloseSession(ctx context.Context, req gen.CloseSessionRequestObject) (gen.CloseSessionResponseObject, error) {
	in := ab2bus.CloseSessionInput{Session: req.Session, IdempotencyKey: derefOr(req.Params.IdempotencyKey)}
	if err := a.sessions.Close(ctx, IdentityFrom(ctx), in); err != nil {
		return nil, err
	}
	return gen.CloseSession204Response{}, nil
}

func (a *api) Receive(ctx context.Context, req gen.ReceiveRequestObject) (gen.ReceiveResponseObject, error) {
	in := ab2bus.ReceiveInput{Session: req.Session}
	if b := req.Body; b != nil {
		in.Ack = derefOr(b.Ack)
		in.Count = derefOr(b.Count) // absent is 0; the core defaults it
		in.Channels = derefOr(b.Channels)
		in.IncludeOwn = derefOr(b.IncludeOwn)
	}
	r, err := a.delivery.Receive(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err
	}
	out := gen.ReceiveResult{
		AckIgnored: r.AckIgnored, Batch: r.Batch, Redelivered: r.Redelivered,
		Instruction: optStr(r.Instruction), Notice: optStr(r.Notice),
		Gaps:     make([]gen.Gap, 0, len(r.Gaps)),
		Messages: make([]gen.Message, 0, len(r.Messages)),
	}
	for _, g := range r.Gaps {
		out.Gaps = append(out.Gaps, gen.Gap{Channel: g.Channel, From: g.From, To: g.To})
	}
	for _, m := range r.Messages {
		out.Messages = append(out.Messages, toGenMessage(m))
	}
	return gen.Receive200JSONResponse(out), nil
}

func (a *api) Watch(ctx context.Context, req gen.WatchRequestObject) (gen.WatchResponseObject, error) {
	in := ab2bus.WatchInput{
		Session:    req.Session,
		After:      derefOr(req.Params.After),
		IncludeOwn: derefOr(req.Params.IncludeOwn),
	}
	if t := req.Params.Timeout; t != nil {
		d, err := watchTimeout(float64(*t))
		if err != nil {
			return nil, err
		}
		in.Timeout = d
	}
	r, err := a.delivery.Watch(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err // replaced is a *bus.Error: the hook writes the 409
	}
	if !r.Woke {
		return gen.Watch204Response{}, nil
	}
	chans := r.Channels
	if chans == nil {
		chans = []string{}
	}
	return gen.Watch200JSONResponse(gen.Wake{Seq: r.Seq, Count: r.Count, Channels: chans}), nil
}

func (a *api) ListIdentities(ctx context.Context, _ gen.ListIdentitiesRequestObject) (gen.ListIdentitiesResponseObject, error) {
	ids, err := a.sessions.Identities(ctx, IdentityFrom(ctx))
	if err != nil {
		return nil, err
	}
	out := make(gen.ListIdentities200JSONResponse, 0, len(ids))
	for _, i := range ids {
		g := gen.Identity{Name: i.Name, Admin: i.Admin, Sessions: make([]gen.SessionInfo, 0, len(i.Sessions))}
		for _, s := range i.Sessions {
			g.Sessions = append(g.Sessions, gen.SessionInfo{Id: s.ID, Identity: s.Identity, DisplayName: s.DisplayName, OpenedAt: s.OpenedAt})
		}
		out = append(out, g)
	}
	return out, nil
}

// watchTimeout converts the wire's fractional seconds to a Duration without
// leaving the range a float to int64 conversion defines: that conversion is
// implementation-defined out of range (arm64 saturates, amd64 gives
// math.MinInt64), so the same request would be a clamp on one platform and a
// "negative" validation on the other. NaN and the infinities are validation;
// anything a Duration cannot hold saturates at its end, where the core clamps
// the top to the configured cap (Decision 42) and rejects the bottom as
// negative.
func watchTimeout(seconds float64) (time.Duration, error) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, ab2bus.Errorf(ab2bus.CodeValidation, false, "timeout must be a finite number of seconds")
	}
	ns := seconds * float64(time.Second)
	switch {
	case ns >= float64(math.MaxInt64):
		return time.Duration(math.MaxInt64), nil
	case ns <= float64(math.MinInt64):
		return time.Duration(math.MinInt64), nil
	}
	return time.Duration(ns), nil
}
