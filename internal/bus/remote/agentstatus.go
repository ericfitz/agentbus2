package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/gen"
)

// fromGenAgentStatus copies the tag as a string: a tag this build does not
// know is passed through, not rejected (open note: agent-status 1).
func fromGenAgentStatus(g gen.AgentStatus) ab2bus.AgentStatus {
	return ab2bus.AgentStatus{
		Identity: g.Identity, Revision: g.Revision, Tag: ab2bus.StatusTag(g.Tag),
		LastTag: ab2bus.StatusTag(derefStr(g.LastTag)), Text: g.Text, Problem: derefStr(g.Problem),
		Session: g.Session, DisplayName: g.DisplayName, SetAt: g.SetAt,
	}
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (b *Bus) SetAgentStatus(ctx context.Context, in ab2bus.SetAgentStatusInput) (ab2bus.AgentStatus, error) {
	if in.Session == "" {
		return ab2bus.AgentStatus{}, ab2bus.Errorf(ab2bus.CodeValidation, false, "session is required")
	}
	body := gen.SetAgentStatusJSONRequestBody{Tag: string(in.Tag), Text: in.Text, Problem: strPtr(in.Problem)}
	r, err := b.c.SetAgentStatusWithResponse(ctx, in.Session, &gen.SetAgentStatusParams{IdempotencyKey: key(in.IdempotencyKey)}, body)
	if err != nil {
		return ab2bus.AgentStatus{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.AgentStatus{}, err
	}
	if r.JSON200 == nil {
		return ab2bus.AgentStatus{}, unparsable(r.StatusCode())
	}
	return fromGenAgentStatus(*r.JSON200), nil
}

func (b *Bus) AgentStatuses(ctx context.Context) ([]ab2bus.AgentStatusEntry, error) {
	r, err := b.c.ListAgentStatusesWithResponse(ctx)
	if err != nil {
		return nil, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, unparsable(r.StatusCode())
	}
	out := make([]ab2bus.AgentStatusEntry, 0, len(*r.JSON200))
	for _, e := range *r.JSON200 {
		kind, err := fromGenIdentityKind("kind", string(e.Kind))
		if err != nil {
			return nil, err
		}
		out = append(out, ab2bus.AgentStatusEntry{Identity: e.Identity, Kind: kind, Live: e.Live, Status: fromGenAgentStatus(e.Status)})
	}
	return out, nil
}

func (b *Bus) AgentStatusHistory(ctx context.Context, identity string) ([]ab2bus.AgentStatus, error) {
	if identity == "" {
		return nil, ab2bus.Errorf(ab2bus.CodeValidation, false, "identity is required")
	}
	r, err := b.c.AgentStatusHistoryWithResponse(ctx, identity)
	if err != nil {
		return nil, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, unparsable(r.StatusCode())
	}
	out := make([]ab2bus.AgentStatus, 0, len(*r.JSON200))
	for _, s := range *r.JSON200 {
		out = append(out, fromGenAgentStatus(s))
	}
	return out, nil
}
