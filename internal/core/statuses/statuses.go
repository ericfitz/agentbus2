package statuses

import (
	"context"

	"github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/core"
	"github.com/ericfitz/agentmesh/internal/core/access"
	"github.com/ericfitz/agentmesh/internal/core/rules"
	"github.com/ericfitz/agentmesh/internal/store"
)

// Set records the caller's status from one of its live sessions. The order
// is: validate (no Store call), then one transaction that renews the
// session's lease, checks the access rule and the rate limit, appends the
// next revision and prunes to StatusHistoryCount (the insert and the prune
// share the transaction, so a failure between them rolls back whole).
// State (Definition of Done D): a replayed idempotency key returns the
// stored result and adds no revision; two unkeyed calls add two revisions in
// commit order, the last wins; a failed call leaves nothing behind; a second
// prune deletes nothing.
func (s Service) Set(ctx context.Context, identity string, in bus.SetAgentStatusInput) (bus.AgentStatus, error) {
	if err := rules.CheckStatusTag(in.Tag); err != nil {
		return bus.AgentStatus{}, err
	}
	text, err := rules.NormalizeLine("text", in.Text)
	if err != nil {
		return bus.AgentStatus{}, err
	}
	if text == "" {
		return bus.AgentStatus{}, core.E(bus.CodeValidation, "text is required: one line saying what you are doing now")
	}
	problem, err := rules.NormalizeLine("problem", in.Problem)
	if err != nil {
		return bus.AgentStatus{}, err
	}
	if in.Session == "" {
		return bus.AgentStatus{}, core.E(bus.CodeValidation, "session is required")
	}
	in.Text, in.Problem = text, problem
	key := in.IdempotencyKey
	in.IdempotencyKey = ""
	size := len(in.Tag) + len(text) + len(problem)

	var res bus.AgentStatus
	var replayed bool
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		res = bus.AgentStatus{}
		p, err := access.Resolve(ctx, tx, identity)
		if err != nil {
			return err
		}
		replayed, err = s.WithReceipt(ctx, tx, identity, key, in, &res, func() error {
			sess, err := s.Sessions.RequireAs(ctx, tx, p, in.Session)
			if err != nil {
				return err
			}
			if err := s.Access.Require(ctx, tx, p, access.Identity{Name: p.Name}, access.SetStatus); err != nil {
				return err
			}
			// open note: agent-status 8: one operation of len(tag)+len(text)+len(problem) bytes.
			if !s.Limits.Check(identity, size, s.Now()) {
				return core.E(bus.CodeRateLimited, "send_messages_per_second rate limit exceeded for %s", identity)
			}
			row := store.AgentStatus{
				Identity: identity, Tag: string(in.Tag), Text: text, Problem: problem,
				SessionID: sess.ID, DisplayName: sess.DisplayName, SetAt: s.NowMs(),
			}
			rev, err := tx.AppendAgentStatus(ctx, row)
			if err != nil {
				return core.FromStore(err)
			}
			row.Revision = rev
			if _, err := tx.PruneAgentStatuses(ctx, identity, s.Cfg.StatusHistoryCount); err != nil {
				return core.FromStore(err)
			}
			res = core.AgentStatusToBus(row)
			return nil
		})
		return err
	})
	if err != nil {
		return bus.AgentStatus{}, core.FromStore(err)
	}
	if !replayed {
		s.Limits.Charge(identity, size, s.Now())
	}
	return res, nil
}

// List returns the latest status of every identity the caller may see, by
// identity in byte order. An entry is Live when the identity has a live
// session; otherwise its Tag reads offline and LastTag keeps the stored tag
// (open note: agent-status 2). The result is never nil.
func (s Service) List(ctx context.Context, identity string) ([]bus.AgentStatusEntry, error) {
	out := []bus.AgentStatusEntry{}
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out = []bus.AgentStatusEntry{}
		p, err := access.Resolve(ctx, tx, identity)
		if err != nil {
			return err
		}
		visible, err := s.Access.Identities(ctx, tx, p)
		if err != nil {
			return err
		}
		kinds := make(map[string]store.IdentityKind, len(visible))
		for _, id := range visible {
			kinds[id.Name] = id.Kind
		}
		latest, err := tx.LatestAgentStatuses(ctx)
		if err != nil {
			return err
		}
		for _, r := range latest {
			kind, ok := kinds[r.Identity]
			if !ok {
				continue
			}
			live, err := s.LiveSessions(ctx, tx, r.Identity)
			if err != nil {
				return err
			}
			st := core.AgentStatusToBus(r)
			st.LastTag = st.Tag
			isLive := len(live) > 0
			if !isLive {
				st.Tag = bus.StatusOffline
			}
			out = append(out, bus.AgentStatusEntry{
				Identity: r.Identity, Kind: bus.IdentityKind(kind), Live: isLive, Status: st,
			})
		}
		return nil
	})
	if err != nil {
		return nil, core.FromStore(err)
	}
	return out, nil
}

// History returns the identity's stored statuses, oldest first, capped at the
// current StatusHistoryCount (rows above a lowered count stay until the
// identity's next set). An unknown or out-of-scope identity is not_found. The
// result is never nil.
func (s Service) History(ctx context.Context, identity, of string) ([]bus.AgentStatus, error) {
	out := []bus.AgentStatus{}
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out = []bus.AgentStatus{}
		p, err := access.Resolve(ctx, tx, identity)
		if err != nil {
			return err
		}
		if err := s.Access.Require(ctx, tx, p, access.Identity{Name: of}, access.See); err != nil {
			return err
		}
		rows, err := tx.AgentStatusHistory(ctx, of, s.Cfg.StatusHistoryCount)
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, core.AgentStatusToBus(r))
		}
		return nil
	})
	if err != nil {
		return nil, core.FromStore(err)
	}
	return out, nil
}
