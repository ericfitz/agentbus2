package server

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// genAgentStatus converts a status to its wire form. The tag is a plain
// string (open note: agent-status 1); problem and last_tag are absent when
// empty.
func genAgentStatus(a ab2bus.AgentStatus) gen.AgentStatus {
	g := gen.AgentStatus{
		Identity: a.Identity, Revision: a.Revision, Tag: string(a.Tag), Text: a.Text,
		Session: a.Session, DisplayName: a.DisplayName, SetAt: a.SetAt,
	}
	if a.Problem != "" {
		g.Problem = &a.Problem
	}
	if a.LastTag != "" {
		lt := string(a.LastTag)
		g.LastTag = &lt
	}
	return g
}

func (a *api) SetAgentStatus(ctx context.Context, req gen.SetAgentStatusRequestObject) (gen.SetAgentStatusResponseObject, error) {
	in := ab2bus.SetAgentStatusInput{Session: req.Session, IdempotencyKey: derefOr(req.Params.IdempotencyKey)}
	if b := req.Body; b != nil {
		in.Tag, in.Text, in.Problem = ab2bus.StatusTag(b.Tag), b.Text, derefOr(b.Problem)
	}
	st, err := a.statuses.Set(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err
	}
	return gen.SetAgentStatus200JSONResponse(genAgentStatus(st)), nil
}

func (a *api) ListAgentStatuses(ctx context.Context, _ gen.ListAgentStatusesRequestObject) (gen.ListAgentStatusesResponseObject, error) {
	es, err := a.statuses.List(ctx, IdentityFrom(ctx))
	if err != nil {
		return nil, err
	}
	out := make(gen.ListAgentStatuses200JSONResponse, 0, len(es))
	for _, e := range es {
		out = append(out, gen.AgentStatusEntry{
			Identity: e.Identity, Kind: gen.AgentStatusEntryKind(e.Kind), Live: e.Live, Status: genAgentStatus(e.Status),
		})
	}
	return out, nil
}

func (a *api) AgentStatusHistory(ctx context.Context, req gen.AgentStatusHistoryRequestObject) (gen.AgentStatusHistoryResponseObject, error) {
	rows, err := a.statuses.History(ctx, IdentityFrom(ctx), req.Identity)
	if err != nil {
		return nil, err
	}
	out := make(gen.AgentStatusHistory200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, genAgentStatus(r))
	}
	return out, nil
}
