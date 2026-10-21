package remote

import (
	"context"
	"fmt"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Sessions, receive, watch and identities. Session ids go to the generated
// client as given (choice 17, Decision 80), with one exception: Close refuses
// an empty session id itself (see Close).

func (b *Bus) Open(ctx context.Context, in ab2bus.OpenSessionInput) (ab2bus.Session, error) {
	body := gen.OpenSessionJSONRequestBody{Hint: strPtr(in.Hint), ResumeToken: strPtr(in.ResumeToken)}
	if len(in.Cursors) > 0 {
		cs := make([]gen.OfferedCursor, len(in.Cursors))
		for i, c := range in.Cursors {
			cs[i] = gen.OfferedCursor{Channel: c.Channel, Seq: c.Seq}
		}
		body.Cursors = &cs
	}
	r, err := b.c.OpenSessionWithResponse(ctx, body)
	if err != nil {
		return ab2bus.Session{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.Session{}, err
	}
	if r.JSON201 == nil {
		return ab2bus.Session{}, unparsableSuccess(r.StatusCode())
	}
	s := r.JSON201
	out := ab2bus.Session{
		ID: s.Id, Identity: s.Identity, DisplayName: s.DisplayName,
		ResumeToken: s.ResumeToken, Resumed: s.Resumed,
	}
	for _, c := range s.RejectedCursors {
		out.RejectedCursors = append(out.RejectedCursors, ab2bus.RejectedCursor{Channel: c.Channel, Reason: string(c.Reason)})
	}
	for _, p := range s.Pending {
		out.Pending = append(out.Pending, ab2bus.PendingChannel{Channel: p.Channel, Pending: p.Pending})
	}
	return out, nil
}

// Close ends the caller's session. An empty session id is answered locally
// with validation and no request is sent: core sessions.Close says
// "session is required" (validation) for it, while on the wire the empty
// segment of DELETE /v1/sessions/ is the validator router's 404 not_found, so
// without this guard the two transports would answer differently. This is the
// plan's empty-name contingency, the only client-side rule in this package and
// a departure from Decision 80 (the rules stay in the core).
// pending Eric: Milestone 4 item 6 (A: keep the guard; B: core answers
// not_found for an empty session id, as Receive and Watch do through Require,
// and the guard goes).
func (b *Bus) Close(ctx context.Context, in ab2bus.CloseSessionInput) error {
	if in.Session == "" {
		return ab2bus.Errorf(ab2bus.CodeValidation, false, "session is required")
	}
	r, err := b.c.CloseSessionWithResponse(ctx, in.Session, &gen.CloseSessionParams{IdempotencyKey: key(in.IdempotencyKey)})
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	return errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil)
}

func (b *Bus) Receive(ctx context.Context, in ab2bus.ReceiveInput) (ab2bus.ReceiveResult, error) {
	body := gen.ReceiveJSONRequestBody{Ack: strPtr(in.Ack), Channels: slicePtr(in.Channels)}
	if in.Count != 0 {
		body.Count = &in.Count
	}
	if in.IncludeOwn {
		body.IncludeOwn = ptr(true)
	}
	r, err := b.c.ReceiveWithResponse(ctx, in.Session, body)
	if err != nil {
		return ab2bus.ReceiveResult{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.ReceiveResult{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.ReceiveResult{}, unparsableSuccess(r.StatusCode())
	}
	w := r.JSON200
	out := ab2bus.ReceiveResult{
		Batch: w.Batch, Redelivered: w.Redelivered, AckIgnored: w.AckIgnored,
		Notice: deref(w.Notice), Instruction: deref(w.Instruction),
	}
	for _, m := range w.Messages {
		out.Messages = append(out.Messages, fromGenMessage(m))
	}
	for _, g := range w.Gaps {
		out.Gaps = append(out.Gaps, ab2bus.Gap{Channel: g.Channel, From: g.From, To: g.To})
	}
	return out, nil
}

func (b *Bus) Watch(ctx context.Context, in ab2bus.WatchInput) (ab2bus.WatchResult, error) {
	params := &gen.WatchParams{After: int64Ptr(in.After), Timeout: seconds(in.Timeout)}
	if in.IncludeOwn {
		params.IncludeOwn = ptr(true)
	}
	r, err := b.c.WatchWithResponse(ctx, in.Session, params)
	if err != nil {
		return ab2bus.WatchResult{}, errorOf(ctx, 0, nil, nil, err)
	}
	if r.StatusCode() == 204 {
		return ab2bus.WatchResult{Woke: false}, nil
	}
	p := r.ApplicationproblemJSON409
	if p == nil {
		p = r.ApplicationproblemJSONDefault
	}
	if err := errorOf(ctx, r.StatusCode(), p, r.Body, nil); err != nil {
		return ab2bus.WatchResult{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.WatchResult{}, unparsableSuccess(r.StatusCode())
	}
	return ab2bus.WatchResult{Woke: true, Seq: r.JSON200.Seq, Count: r.JSON200.Count, Channels: r.JSON200.Channels}, nil
}

func (b *Bus) Identities(ctx context.Context) ([]ab2bus.IdentityInfo, error) {
	r, err := b.c.ListIdentitiesWithResponse(ctx)
	if err != nil {
		return nil, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, unparsableSuccess(r.StatusCode())
	}
	out := make([]ab2bus.IdentityInfo, 0, len(*r.JSON200))
	for _, i := range *r.JSON200 {
		info := ab2bus.IdentityInfo{Name: i.Name, Admin: i.Admin}
		for _, s := range i.Sessions {
			info.Sessions = append(info.Sessions, ab2bus.SessionInfo{ID: s.Id, Identity: s.Identity, DisplayName: s.DisplayName, OpenedAt: s.OpenedAt})
		}
		out = append(out, info)
	}
	return out, nil
}

// unparsableSuccess is a 2xx whose body did not parse as the documented shape.
func unparsableSuccess(status int) error {
	return &ab2bus.Error{Code: ab2bus.CodeInternal, Message: fmt.Sprintf("HTTP %d: response body is not the documented shape", status)}
}
