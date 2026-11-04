package tasks

import (
	"context"

	"github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/core"
	"github.com/ericfitz/agentmesh/internal/store"
)

// SnapshotTask is one live task as the scope canvas shows it: the fields a
// reader of the list needs, with the tree position and the abandonment flags
// already derived.
type SnapshotTask struct {
	ID           int64
	Subject      string
	Status       string
	Assignee     string
	Parent       int64 // the effective parent: 0 for a root, also when the parent is gone
	Depth        int
	OpenBlockers []int64
	LeasedUntil  int64
	Abandoned    bool
	LeaseExpired bool
}

// Snapshot reads the live tasks of the list in tree order (loadTasks) at now
// for the caller's transaction. It is read-only: it never reclaims an
// abandoned task and never records a read mark, so a periodic observer leaves
// no revision behind. A channel that is not a task list is a validation
// error.
func (s Service) Snapshot(ctx context.Context, tx store.Tx, ch store.Channel, now int64) ([]SnapshotTask, error) {
	if ch.Kind != store.KindTasks {
		return nil, core.E(bus.CodeValidation, "%q is not a task list", ch.Name)
	}
	ts, err := loadTasks(ctx, tx, ch)
	if err != nil {
		return nil, err
	}
	abandoned, err := s.abandonedSet(ctx, tx, ts, now)
	if err != nil {
		return nil, err
	}
	_, depth := taskTree(ts)
	out := make([]SnapshotTask, 0, len(ts))
	for _, t := range ts {
		out = append(out, SnapshotTask{
			ID: t.ID, Subject: t.Subject, Status: t.Status, Assignee: t.Owner,
			Parent: effectiveParent(ts, t.Parent), Depth: depth[t.ID],
			OpenBlockers: t.OpenBlockers, LeasedUntil: t.LeasedUntil,
			Abandoned: abandoned[t.ID], LeaseExpired: leaseExpired(t, now),
		})
	}
	return out, nil
}
