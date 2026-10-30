package sqlstore

import (
	"context"
	"fmt"
	"slices"

	"github.com/ericfitz/agentbus2/internal/store"
)

// The agent_statuses table is new in v2 (issue #10); no v1 source.

type agentStatusRow struct {
	Identity    string `db:"identity"`
	Revision    int64  `db:"revision"`
	Tag         string `db:"tag"`
	Text        string `db:"text"`
	Problem     string `db:"problem"`
	SessionID   string `db:"session_id"`
	DisplayName string `db:"display_name"`
	SetAt       int64  `db:"set_at"`
}

const agentStatusColumns = "identity, revision, tag, text, problem, session_id, display_name, set_at"

func (r agentStatusRow) status() store.AgentStatus {
	return store.AgentStatus(r)
}

func statusesOf(rows []agentStatusRow) []store.AgentStatus {
	out := make([]store.AgentStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.status())
	}
	return out
}

func (t *tx) AppendAgentStatus(ctx context.Context, s store.AgentStatus) (int64, error) {
	if err := t.writable(); err != nil {
		return 0, err
	}
	if err := checkText("agent status", "identity", s.Identity, "tag", s.Tag, "text", s.Text,
		"problem", s.Problem, "session id", s.SessionID, "display name", s.DisplayName); err != nil {
		return 0, err
	}
	var rev int64
	if err := t.getW(ctx, &rev, "SELECT COALESCE(MAX(revision), 0) + 1 FROM agent_statuses WHERE identity = ?", s.Identity); err != nil {
		return 0, err
	}
	_, err := t.execW(ctx, "INSERT INTO agent_statuses ("+agentStatusColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		s.Identity, rev, s.Tag, s.Text, s.Problem, s.SessionID, s.DisplayName, s.SetAt)
	if isForeignKey(err) {
		return 0, fmt.Errorf("%w: identity %q", store.ErrNotFound, s.Identity)
	}
	if err != nil {
		return 0, err
	}
	return rev, nil
}

func (t *tx) PruneAgentStatuses(ctx context.Context, identity string, keep int) (int, error) {
	if err := t.writable(); err != nil {
		return 0, err
	}
	if keep < 1 {
		return 0, fmt.Errorf("%w: agent status keep %d must be at least 1", store.ErrInvalid, keep)
	}
	if err := checkText("agent status", "identity", identity); err != nil {
		return 0, err
	}
	res, err := t.execW(ctx, "DELETE FROM agent_statuses WHERE identity = ? AND revision <= (SELECT MAX(revision) FROM agent_statuses WHERE identity = ?) - ?",
		identity, identity, keep)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (t *tx) LatestAgentStatuses(ctx context.Context) ([]store.AgentStatus, error) {
	var rows []agentStatusRow
	err := t.sel(ctx, &rows, "SELECT "+agentStatusColumns+" FROM agent_statuses a WHERE revision = (SELECT MAX(revision) FROM agent_statuses WHERE identity = a.identity)")
	if err != nil {
		return nil, err
	}
	out := statusesOf(rows)
	sortByBytes(out, func(s store.AgentStatus) string { return s.Identity })
	return out, nil
}

func (t *tx) AgentStatusHistory(ctx context.Context, identity string, limit int) ([]store.AgentStatus, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	if badText(identity) {
		return []store.AgentStatus{}, nil
	}
	var rows []agentStatusRow
	err := t.sel(ctx, &rows, "SELECT "+agentStatusColumns+" FROM agent_statuses WHERE identity = ? ORDER BY revision DESC LIMIT ?", identity, limit)
	if err != nil {
		return nil, err
	}
	out := statusesOf(rows)
	slices.Reverse(out)
	return out, nil
}
