package bus

import "context"

// Task is a task as returned to callers (ported from v1 tasks.go). Channel is
// the task list's name.
type Task struct {
	ID           int64             `json:"id"`
	Channel      string            `json:"channel"`
	Revision     int64             `json:"revision"`
	Subject      string            `json:"subject"`
	Description  string            `json:"description,omitempty"`
	Status       string            `json:"status"`
	Owner        string            `json:"owner,omitempty"`
	Parent       int64             `json:"parent,omitempty"`
	Rank         string            `json:"rank"`
	BlockedBy    []int64           `json:"blocked_by,omitempty"`
	LeasedUntil  int64             `json:"leased_until,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Blocked      bool              `json:"blocked"`
	OpenBlockers []int64           `json:"open_blockers,omitempty"`
	UpdatedAt    int64             `json:"updated_at"`
	UpdatedBy    string            `json:"updated_by"`
}

// TaskSummary is one row of a task list result.
type TaskSummary struct {
	ID           int64   `json:"id"`
	Subject      string  `json:"subject"`
	Status       string  `json:"status"`
	Owner        string  `json:"owner,omitempty"`
	Parent       int64   `json:"parent,omitempty"`
	Depth        int     `json:"depth"`
	OpenBlockers []int64 `json:"open_blockers,omitempty"`
	LeasedUntil  int64   `json:"leased_until,omitempty"`
	HasDetails   bool    `json:"has_details,omitempty"`
}

// TaskCreateInput creates a task. Before and After are mutually exclusive
// sibling ids, 0 meaning unset.
type TaskCreateInput struct {
	Channel        string            `json:"channel"`
	Subject        string            `json:"subject"`
	Description    string            `json:"description,omitempty"`
	Parent         int64             `json:"parent,omitempty"`
	Before         int64             `json:"before,omitempty"`
	After          int64             `json:"after,omitempty"`
	BlockedBy      []int64           `json:"blocked_by,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

// TaskListInput lists a task list, optionally filtered.
type TaskListInput struct {
	Channel string `json:"channel"`
	Status  string `json:"status,omitempty"`
	Owner   string `json:"owner,omitempty"`
}

// TaskPatch is the update input: absent fields are unchanged. Before and
// After are mutually exclusive sibling ids, 0 meaning unset.
type TaskPatch struct {
	ID              int64             `json:"task_id"`
	Status          *string           `json:"status,omitempty"`
	Owner           *string           `json:"owner,omitempty"`
	Subject         *string           `json:"subject,omitempty"`
	Description     *string           `json:"description,omitempty"`
	Parent          *int64            `json:"parent,omitempty"`
	Before          int64             `json:"before,omitempty"`
	After           int64             `json:"after,omitempty"`
	AddBlockedBy    []int64           `json:"add_blocked_by,omitempty"`
	RemoveBlockedBy []int64           `json:"remove_blocked_by,omitempty"`
	LeasedUntil     *int64            `json:"leased_until,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	Delete          bool              `json:"delete,omitempty"`
	IdempotencyKey  string            `json:"idempotency_key,omitempty"`
}

// TaskUpdateResult is the update result.
type TaskUpdateResult struct {
	Task     Task  `json:"task"`
	Replaced int64 `json:"replaced"` // seq of the revision this one replaced
	Deleted  bool  `json:"deleted,omitempty"`
}

// TaskClaimInput claims a task for the caller, optionally with a lease.
type TaskClaimInput struct {
	ID             int64
	LeasedUntil    int64
	IdempotencyKey string
}

// TaskReleaseInput releases a task the caller owns.
type TaskReleaseInput struct {
	ID             int64
	IdempotencyKey string
}

// Tasks is the task seam. The getter is GetTask, not Get (see Memories).
type Tasks interface {
	Create(ctx context.Context, in TaskCreateInput) (Task, error)
	GetTask(ctx context.Context, id int64) (Task, error)
	List(ctx context.Context, in TaskListInput) ([]TaskSummary, error)
	Update(ctx context.Context, p TaskPatch) (TaskUpdateResult, error)
	Claim(ctx context.Context, in TaskClaimInput) (TaskUpdateResult, error)
	Release(ctx context.Context, in TaskReleaseInput) (TaskUpdateResult, error)
}
