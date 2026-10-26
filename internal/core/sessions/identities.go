package sessions

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Identities lists the identities with at least one live session, by name,
// each with its live sessions by OpenedAt (v1 Discover/liveSessions).
func (s Service) Identities(ctx context.Context, identity string) ([]bus.IdentityInfo, error) {
	var out []bus.IdentityInfo
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out = nil
		p, err := access.Resolve(ctx, tx, identity)
		if err != nil {
			return err
		}
		// Only the identities the caller may see (spec 4.1 (c), 4.5).
		all, err := s.Access.Identities(ctx, tx, p)
		if err != nil {
			return err
		}
		for _, id := range all {
			live, err := s.LiveSessions(ctx, tx, id.Name)
			if err != nil {
				return err
			}
			if len(live) == 0 {
				continue
			}
			info := bus.IdentityInfo{Name: id.Name, Kind: bus.IdentityKind(id.Kind), Admin: id.Admin}
			for _, row := range live {
				info.Sessions = append(info.Sessions, bus.SessionInfo{
					ID: row.ID, Identity: row.Identity, DisplayName: row.DisplayName, OpenedAt: row.OpenedAt,
				})
			}
			out = append(out, info)
		}
		return nil
	})
	if err != nil {
		return nil, core.FromStore(err)
	}
	return out, nil
}
