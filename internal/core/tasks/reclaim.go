package tasks

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/store"
)

// leaseExpired reports an in_progress task whose lease has run out. A task
// with no lease (LeasedUntil 0) never lease-expires.
func leaseExpired(t task, now int64) bool {
	return t.Status == "in_progress" && t.LeasedUntil != 0 && t.LeasedUntil <= now
}

// abandonedSet reports which of ts are abandoned at now: in_progress with an
// expired lease, or with an owner that has no live session (a row with
// ClosedAt == 0 and LeaseExpiresAt plus the skew not before now;
// Decision (Eric, 2026-10-21): Milestone 3 item 13). An in_progress task
// with an empty owner (possible only from a corrupt write) is abandoned: an
// empty identity never has a session, and ListSessions("") would list every
// identity's. Sessions are read once per distinct owner.
func (s Service) abandonedSet(ctx context.Context, tx store.Tx, ts []task, now int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	alive := map[string]bool{}
	for _, t := range ts {
		if t.Status != "in_progress" {
			continue
		}
		if leaseExpired(t, now) || t.Owner == "" {
			out[t.ID] = true
			continue
		}
		live, seen := alive[t.Owner]
		if !seen {
			rows, err := tx.ListSessions(ctx, t.Owner)
			if err != nil {
				return nil, core.FromStore(err)
			}
			for _, r := range rows {
				if s.IsLive(r, now) {
					live = true
					break
				}
			}
			alive[t.Owner] = live
		}
		if !live {
			out[t.ID] = true
		}
	}
	return out, nil
}

// reclaimAbandoned returns every abandoned task among ids (all tasks of the
// list when ids is nil) to pending, owner and lease cleared, as a revision of
// type "reclaimed" with no sender (v1 reclaimAbandoned). It runs in the
// caller's Store.Append transaction, is not rate-charged and returns how many
// it reclaimed; fx records the last row written for the hint.
func (s Service) reclaimAbandoned(ctx context.Context, tx store.Tx, ch store.Channel, now int64, ids []int64, fx *effects) (int, error) {
	ts, err := loadTasks(ctx, tx, ch)
	if err != nil {
		return 0, err
	}
	targets := ts
	if ids != nil {
		targets = nil
		for _, id := range ids {
			if t := taskByID(ts, id); t != nil {
				targets = append(targets, *t)
			}
		}
	}
	ab, err := s.abandonedSet(ctx, tx, targets, now)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range targets {
		if !ab[t.ID] {
			continue
		}
		t.Status, t.Owner, t.LeasedUntil = "pending", "", 0
		if _, err := s.writeRevision(ctx, tx, ch, "", t, "reclaimed", fx); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// reclaim runs reclaimAbandoned in a Store.Append transaction of its own, for
// a read that found an abandoned task, and publishes a hint after the commit.
func (s Service) reclaim(ctx context.Context, channelID int64, ids []int64, now int64) (int, error) {
	var (
		n  int
		fx effects
	)
	err := s.Store.Append(ctx, func(ctx context.Context, tx store.Tx) error {
		n, fx = 0, effects{}
		ch, err := tx.Channel(ctx, channelID)
		if err != nil {
			return core.FromStore(err)
		}
		if ch.Kind != store.KindTasks {
			return core.E(bus.CodeValidation, "%q is not a task list", ch.Name)
		}
		n, err = s.reclaimAbandoned(ctx, tx, ch, now, ids, &fx)
		return err
	})
	if err != nil {
		return 0, core.FromStore(err)
	}
	if fx.seq != 0 {
		s.Publish(notify.Hint{ChannelID: fx.channelID, Seq: fx.seq})
	}
	return n, nil
}

// ReclaimChannel reclaims every abandoned task of the list at now in one
// Store.Append and returns how many (the maintenance job's body, Task 11; a
// second call finds nothing). A channel that is not a task list is a
// validation error.
func (s Service) ReclaimChannel(ctx context.Context, channelID int64, now int64) (int, error) {
	return s.reclaim(ctx, channelID, nil, now)
}
