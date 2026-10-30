package store

import "context"

// AgentStatus is one row of an identity's status history (issue #10): the
// agent's own account of what it is doing. The Store applies no rule to Tag
// (the core validates it), only TextRule to every text field.
type AgentStatus struct {
	Identity string
	// Revision is assigned by AppendAgentStatus; the caller's value is ignored.
	Revision    int64
	Tag         string
	Text        string
	Problem     string
	SessionID   string
	DisplayName string
	SetAt       int64 // Unix milliseconds
}

// AgentStatuses is the append-only per-identity status history. Rows go with
// their identity (foreign key cascade), and ResetData deletes them.
type AgentStatuses interface {
	// AppendAgentStatus inserts the identity's next revision (max+1, 1 when
	// none; s.Revision is ignored) and returns it. ErrNotFound: unknown
	// identity (foreign key); ErrInvalid: TextRule on a text field, named.
	// Write transactions only (ErrReadOnly in Read).
	AppendAgentStatus(ctx context.Context, s AgentStatus) (int64, error)
	// PruneAgentStatuses deletes the identity's rows with revision <=
	// max-keep and returns how many. ErrInvalid: keep < 1. Write
	// transactions only. A second call deletes nothing.
	PruneAgentStatuses(ctx context.Context, identity string, keep int) (int, error)
	// LatestAgentStatuses returns each identity's highest revision, by
	// identity in byte order.
	LatestAgentStatuses(ctx context.Context) ([]AgentStatus, error)
	// AgentStatusHistory returns the identity's newest limit rows, oldest
	// first; empty for an unknown identity; ErrInvalid: limit <= 0.
	AgentStatusHistory(ctx context.Context, identity string, limit int) ([]AgentStatus, error)
}
