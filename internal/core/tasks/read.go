package tasks

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Get returns the live task id with its derived fields. It needs read on the
// list. A task found abandoned is reclaimed first, in a Store.Append of its
// own (the reclaimed revision is a write, as v1 tryFixUpAbandoned); a failure
// to reclaim is logged and the snapshot already read is served, because a read
// must not fail because the write could not happen (the next read, update or
// maintenance pass reclaims).
func (s Service) Get(ctx context.Context, identity string, id int64) (bus.Task, error) {
	for pass := 0; ; pass++ {
		var (
			t         task
			ch        store.Channel
			abandoned bool
		)
		err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
			t, abandoned = task{}, false
			pr, err := access.Resolve(ctx, tx, identity)
			if err != nil {
				return err
			}
			if ch, err = s.taskChannelOf(ctx, tx, id); err != nil {
				return err
			}
			if err := s.RequireChannelOr(ctx, tx, pr, ch.ID, access.Read, func() error { return noLiveRevision(id) }); err != nil {
				return err
			}
			ts, err := loadTasks(ctx, tx, ch)
			if err != nil {
				return err
			}
			found := taskByID(ts, id)
			if found == nil {
				return core.E(bus.CodeNotFound, "memory %d is not a task", id)
			}
			t = *found
			ab, err := s.abandonedSet(ctx, tx, []task{t}, s.NowMs())
			abandoned = ab[id]
			return err
		})
		if err != nil {
			return bus.Task{}, core.FromStore(err)
		}
		if !abandoned || pass > 0 {
			return t.Task, nil
		}
		if _, err := s.reclaim(ctx, ch.ID, []int64{id}, s.NowMs()); err != nil {
			s.Log.Warn("task reclaim failed; serving the snapshot already read", "channel", ch.Name, "task", id, "err", err)
			return t.Task, nil
		}
	}
}

// List returns the list's live tasks in depth-first tree order, filtered by
// status and owner (the order is kept). It needs read. A bad status is a
// validation error. Abandoned tasks are reclaimed first, as in Get; a list
// with none stays read-only. The result is trimmed to result_default_kib.
func (s Service) List(ctx context.Context, identity string, in bus.TaskListInput) ([]bus.TaskSummary, error) {
	if in.Channel == "" {
		return nil, core.E(bus.CodeValidation, "channel is required")
	}
	if in.Status != "" && !isTaskStatus(in.Status) {
		return nil, core.E(bus.CodeValidation, "status must be pending, in_progress, or completed")
	}
	for pass := 0; ; pass++ {
		var (
			ts        []task
			ch        store.Channel
			abandoned bool
		)
		err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
			ts, abandoned = nil, false
			pr, err := access.Resolve(ctx, tx, identity)
			if err != nil {
				return err
			}
			if ch, err = s.taskChannel(ctx, tx, in.Channel); err != nil {
				return err
			}
			if err := s.Access.Require(ctx, tx, pr, access.Channel{ID: ch.ID}, access.Read); err != nil {
				return err
			}
			if ts, err = loadTasks(ctx, tx, ch); err != nil {
				return err
			}
			ab, err := s.abandonedSet(ctx, tx, ts, s.NowMs())
			abandoned = len(ab) > 0
			return err
		})
		if err != nil {
			return nil, core.FromStore(err)
		}
		if abandoned && pass == 0 {
			if _, err := s.reclaim(ctx, ch.ID, nil, s.NowMs()); err == nil {
				continue
			} else {
				s.Log.Warn("task reclaim failed; serving the snapshot already read", "channel", ch.Name, "err", err)
			}
		}
		return s.summaries(ts, in), nil
	}
}

func (s Service) summaries(ts []task, in bus.TaskListInput) []bus.TaskSummary {
	_, depth := taskTree(ts)
	sums := make([]bus.TaskSummary, 0, len(ts))
	for _, t := range ts {
		if in.Status != "" && t.Status != in.Status {
			continue
		}
		if in.Owner != "" && t.Owner != in.Owner {
			continue
		}
		sums = append(sums, bus.TaskSummary{
			ID: t.ID, Subject: t.Subject, Status: t.Status, Owner: t.Owner,
			Parent: t.Parent, Depth: depth[t.ID], OpenBlockers: t.OpenBlockers,
			LeasedUntil: t.LeasedUntil,
			HasDetails:  t.Description != "" || len(t.Metadata) > 0 || len(t.BlockedBy) > 0,
		})
	}
	limit := min(s.Cfg.ResultDefaultKiB*1024, rules.TrimHardCeilingBytes)
	return rules.TrimToBytes(sums, func(x bus.TaskSummary) int { return rules.MarshalLen(x) }, limit)
}
