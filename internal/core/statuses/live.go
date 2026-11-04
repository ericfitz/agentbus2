package statuses

import (
	"context"

	"github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/core"
	"github.com/ericfitz/agentmesh/internal/store"
)

// LiveStatuses returns the latest status of each named identity that has one
// and a live session now. Offline and unknown names are absent, a duplicate
// name is read once, and the result is never nil. It writes nothing and
// applies no access rule: the caller names the identities it may show.
func (s Service) LiveStatuses(ctx context.Context, tx store.Tx, identities []string) (map[string]bus.AgentStatus, error) {
	out := map[string]bus.AgentStatus{}
	if len(identities) == 0 {
		return out, nil
	}
	want := make(map[string]bool, len(identities))
	for _, n := range identities {
		want[n] = true
	}
	latest, err := tx.LatestAgentStatuses(ctx)
	if err != nil {
		return nil, core.FromStore(err)
	}
	for _, r := range latest {
		if !want[r.Identity] {
			continue
		}
		live, err := s.LiveSessions(ctx, tx, r.Identity)
		if err != nil {
			return nil, err
		}
		if len(live) > 0 {
			out[r.Identity] = core.AgentStatusToBus(r)
		}
	}
	return out, nil
}
