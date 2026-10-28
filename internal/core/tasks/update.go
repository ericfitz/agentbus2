package tasks

import (
	"context"
	"errors"
	"reflect"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/store"
)

// openBlockers returns the entries of blockedBy that are still live in ts and
// not completed. Unlike fillDerived it checks an arbitrary candidate list,
// for the patched task's not-yet-committed blocked_by.
func openBlockers(ts []task, blockedBy []int64) []int64 {
	var open []int64
	for _, bid := range blockedBy {
		if blocker := taskByID(ts, bid); blocker != nil && blocker.Status != "completed" {
			open = append(open, bid)
		}
	}
	return open
}

// applyPatch is pure: it returns the patched task or the rule violation.
// Rule order is the design's: delete combination, delete (there is no owner
// guard: write on the list permits changing any task), scalar fields, owner
// existence, status, lease, links, order, no-op. ts includes cur itself;
// ownerKnown reports whether a candidate owner is a registered identity
// (Decision 81).
func applyPatch(ts []task, cur task, p bus.TaskPatch, as string, now int64, ownerKnown func(string) (bool, error)) (task, error) {
	// Rule 1: delete cannot be combined with any other change.
	if p.Delete && (p.Status != nil || p.Owner != nil || p.Subject != nil || p.Description != nil ||
		p.Parent != nil || p.Before != 0 || p.After != 0 || len(p.AddBlockedBy) > 0 ||
		len(p.RemoveBlockedBy) > 0 || p.LeasedUntil != nil || p.Metadata != nil) {
		return task{}, core.E(bus.CodeValidation, "delete cannot be combined with other changes")
	}

	// Rule 4: delete, refused while the task has subtasks.
	if p.Delete {
		var children []int64
		for _, t := range ts {
			if t.Parent == cur.ID {
				children = append(children, t.ID)
			}
		}
		if len(children) > 0 {
			return task{}, core.E(bus.CodeConflict, "task %d has subtasks: %v", cur.ID, children)
		}
		return cur, nil
	}

	// Rule 5: apply scalar fields onto a working copy.
	patched := cur
	if p.Subject != nil {
		if err := validateTaskSubject(*p.Subject); err != nil {
			return task{}, err
		}
		patched.Subject = *p.Subject
	}
	if p.Description != nil {
		patched.Description = *p.Description
	}
	if p.Metadata != nil {
		merged := make(map[string]string, len(patched.Metadata)+len(p.Metadata))
		for k, v := range patched.Metadata {
			merged[k] = v
		}
		for k, v := range p.Metadata {
			if v == "" {
				delete(merged, k)
			} else {
				merged[k] = v
			}
		}
		if len(merged) == 0 {
			merged = nil
		}
		patched.Metadata = merged
	}
	if p.Owner != nil {
		patched.Owner = *p.Owner
	}
	if p.Parent != nil {
		patched.Parent = *p.Parent
	}
	if len(p.RemoveBlockedBy) > 0 || len(p.AddBlockedBy) > 0 {
		removeSet := make(map[int64]bool, len(p.RemoveBlockedBy))
		for _, id := range p.RemoveBlockedBy {
			removeSet[id] = true
		}
		kept := make([]int64, 0, len(patched.BlockedBy))
		for _, id := range patched.BlockedBy {
			if !removeSet[id] {
				kept = append(kept, id)
			}
		}
		patched.BlockedBy = dedupeIDs(append(kept, p.AddBlockedBy...))
	}

	// Rule 6: a new non-empty owner must be a registered identity.
	if patched.Owner != "" && patched.Owner != cur.Owner {
		known, err := ownerKnown(patched.Owner)
		if err != nil {
			return task{}, err
		}
		if !known {
			return task{}, core.E(bus.CodeNotFound, "identity %q is not registered", patched.Owner)
		}
	}

	// Rule 7: status transitions.
	if p.Status != nil {
		ns := *p.Status
		if !isTaskStatus(ns) {
			return task{}, core.E(bus.CodeValidation, "status must be pending, in_progress, or completed")
		}
		if cur.Status == "completed" && ns == "in_progress" {
			return task{}, core.E(bus.CodeValidation, "reopen the task (status pending) before starting it")
		}
		switch ns {
		case "pending":
			// Only a real transition into pending (release) clears an
			// unspecified owner; pending to pending must not disturb an
			// existing assignment.
			if cur.Status != "pending" && (p.Owner == nil || *p.Owner == "") {
				patched.Owner = ""
			}
			patched.LeasedUntil = 0
		case "completed":
			if patched.Owner == "" {
				patched.Owner = as
			}
			patched.LeasedUntil = 0
		case "in_progress":
			// Only a transition into in_progress needs to be unblocked;
			// renewing an in_progress task must not be disturbed by a
			// blocker reopened since.
			if cur.Status != "in_progress" {
				if open := openBlockers(ts, patched.BlockedBy); len(open) > 0 {
					return task{}, core.E(bus.CodeConflict, "task %d is blocked by %v", cur.ID, open)
				}
			}
		}
		patched.Status = ns
	}

	// An in_progress task always needs an owner, whatever the patch touched.
	if patched.Status == "in_progress" && patched.Owner == "" {
		return task{}, core.E(bus.CodeValidation, "an in_progress task needs an owner")
	}

	// Rule 8: lease, validated first, then cleared unless in_progress.
	if p.LeasedUntil != nil {
		if *p.LeasedUntil != 0 && *p.LeasedUntil <= now {
			return task{}, core.E(bus.CodeValidation, "leased_until must be in the future (or 0 to clear)")
		}
		patched.LeasedUntil = *p.LeasedUntil
	}
	if patched.Status != "in_progress" {
		patched.LeasedUntil = 0
	}

	// Rule 9: links. A blocked_by id no longer live is pruned on this write; an
	// id this patch adds must still resolve, not self-reference and not cycle.
	if len(patched.BlockedBy) > 0 {
		added := make(map[int64]bool, len(p.AddBlockedBy))
		for _, id := range p.AddBlockedBy {
			added[id] = true
		}
		live := make([]int64, 0, len(patched.BlockedBy))
		for _, id := range patched.BlockedBy {
			if added[id] || taskByID(ts, id) != nil {
				live = append(live, id)
			}
		}
		patched.BlockedBy = live
	}
	// The parent is validated only when this patch names one; a stale stored
	// parent is tolerated (the task is a root in the tree).
	check := patched
	if p.Parent == nil {
		check.Parent = 0
	}
	if err := validateTaskLinks(ts, check); err != nil {
		return task{}, err
	}

	// Rule 10: order, recomputed only when this patch moves the task.
	if (p.Parent != nil && *p.Parent != cur.Parent) || p.Before != 0 || p.After != 0 {
		rank, err := placeRank(ts, cur.ID, patched.Parent, p.Before, p.After)
		if err != nil {
			return task{}, err
		}
		patched.Rank = rank
	}

	// Rule 11: no-op. Pruning a dead blocked_by id counts as a change.
	if reflect.DeepEqual(patched, cur) {
		return cur, nil
	}
	return patched, nil
}

// Update is the single write path for every task rule: patch, claim and
// release all funnel through it (v1 TaskUpdate). Order: identity, the
// receipt, the live revision and kind, write, reclaim of the target if it is
// abandoned (in this transaction, so a refused patch rolls the reclaim back),
// the patch, the size gate, the rate limit, the write. The limiter is charged
// after the commit.
func (s Service) Update(ctx context.Context, identity string, p bus.TaskPatch) (bus.TaskUpdateResult, error) {
	key := p.IdempotencyKey
	p.IdempotencyKey = ""
	var (
		out      bus.TaskUpdateResult
		fx       effects
		replayed bool
	)
	write := func(want string) error {
		err := s.Store.Append(ctx, func(ctx context.Context, tx store.Tx) error {
			out, fx, replayed = bus.TaskUpdateResult{}, effects{}, false // fn may run again (Decision 26)
			pr, err := access.Resolve(ctx, tx, identity)
			if err != nil {
				return err
			}
			replayed, err = s.WithReceipt(ctx, tx, identity, key, p, &out, func() error {
				var err error
				out, err = s.update(ctx, tx, identity, pr, p, &fx, want)
				return err
			})
			return err
		})
		if errors.Is(err, core.ErrInspectedChanged) {
			return err
		}
		return core.FromStore(err)
	}
	var err error
	if !s.Inspecting() || p.Delete || (p.Subject == nil && p.Description == nil && p.Metadata == nil) {
		err = write("")
	} else {
		prepare := func(ctx context.Context, tx store.Tx) (core.Prepared, error) {
			pr, err := access.Resolve(ctx, tx, identity)
			if err != nil {
				return core.Prepared{}, err
			}
			var r bus.TaskUpdateResult
			hit, _, err := s.CheckReceipt(ctx, tx, identity, key, p, &r)
			if err != nil || hit {
				return core.Prepared{}, err // a replay needs no inspection
			}
			pl, err := s.planUpdate(ctx, tx, identity, pr, p, true, &effects{})
			if err != nil {
				return core.Prepared{}, err
			}
			prep := taskPrepared(core.OpTaskUpdate, identity, pr, pl.ch, p.ID, pl.patched.Task, pl.size)
			// open note: write-inspection 8 (only a changed subject, description or metadata is inspected)
			prep.Inspect = contentChanged(pl.cur, pl.patched)
			return prep, nil
		}
		err = s.RunInspected(ctx, identity, prepare, write)
	}
	if err != nil {
		return bus.TaskUpdateResult{}, core.FromStore(err)
	}
	if !replayed {
		s.finish(identity, fx)
	}
	return out, nil
}

// updatePlan is what planUpdate decides. ts is nil and patched equals cur for
// a no-op; a delete carries only ch, ts and cur.
type updatePlan struct {
	ch      store.Channel
	ts      []task
	cur     task
	patched task
	size    int
}

// planUpdate runs update up to the rate check. It has no side effect except
// the reclaim and the sibling rerank of the write pass, both of which probe
// (the read pre-pass) skips: the reclaim, and on a rank collision the patch is
// applied again without its move fields (open note write-inspection 10). A
// delete charges rateCheck(0) here and a no-op returns with size 0.
// open note: write-inspection 10 (a provisional rank, no reclaim in the pre-pass)
func (s Service) planUpdate(ctx context.Context, tx store.Tx, identity string, pr access.Principal, p bus.TaskPatch, probe bool, fx *effects) (updatePlan, error) {
	kind := pr.Kind
	ch, err := s.taskChannelOf(ctx, tx, pr, p.ID, access.Write)
	if err != nil {
		return updatePlan{}, err
	}
	if !probe {
		if _, err := s.reclaimAbandoned(ctx, tx, ch, s.NowMs(), []int64{p.ID}, fx); err != nil {
			return updatePlan{}, err
		}
	}
	ts, err := loadTasks(ctx, tx, ch)
	if err != nil {
		return updatePlan{}, err
	}
	cur := taskByID(ts, p.ID)
	if cur == nil {
		return updatePlan{}, core.E(bus.CodeNotFound, "memory %d is not a task", p.ID)
	}
	ownerKnown := s.ownerKnown(ctx, tx, pr)
	patched, err := applyPatch(ts, *cur, p, identity, s.NowMs(), ownerKnown)
	var rc *rankCollision
	if errors.As(err, &rc) {
		if probe {
			q := p
			q.Parent, q.Before, q.After = nil, 0, 0
			patched, err = applyPatch(ts, *cur, q, identity, s.NowMs(), ownerKnown)
		} else {
			// Corrupt equal sibling ranks: renumber them in this transaction, then
			// redo the patch against the reloaded list.
			if ts, err = s.rerankSiblings(ctx, tx, ch, identity, kind, ts, rc.parent, fx); err != nil {
				return updatePlan{}, err
			}
			if cur = taskByID(ts, p.ID); cur == nil {
				return updatePlan{}, core.E(bus.CodeNotFound, "memory %d is not a task", p.ID)
			}
			patched, err = applyPatch(ts, *cur, p, identity, s.NowMs(), ownerKnown)
		}
	}
	if err != nil {
		return updatePlan{}, err
	}
	pl := updatePlan{ch: ch, ts: ts, cur: *cur, patched: patched}
	if p.Delete {
		if err := s.rateCheck(identity, 0, fx); err != nil {
			return updatePlan{}, err
		}
		return pl, nil
	}
	if reflect.DeepEqual(patched, *cur) {
		return pl, nil
	}
	if pl.size, err = s.envelopeSize(ch, identity, "", patched.Task); err != nil {
		return updatePlan{}, err
	}
	if err := s.rateCheck(identity, pl.size, fx); err != nil {
		return updatePlan{}, err
	}
	return pl, nil
}

// update plans the patch, checks the digest the inspector judged (want, "" for
// none) and writes it.
func (s Service) update(ctx context.Context, tx store.Tx, identity string, pr access.Principal, p bus.TaskPatch, fx *effects, want string) (bus.TaskUpdateResult, error) {
	kind := pr.Kind
	pl, err := s.planUpdate(ctx, tx, identity, pr, p, false, fx)
	if err != nil {
		return bus.TaskUpdateResult{}, err
	}
	if want != "" && taskPrepared(core.OpTaskUpdate, identity, pr, pl.ch, p.ID, pl.patched.Task, pl.size).Digest() != want {
		return bus.TaskUpdateResult{}, core.ErrInspectedChanged
	}
	ch, cur, patched := pl.ch, pl.cur, pl.patched
	if p.Delete {
		if _, err := tx.TombstoneMessage(ctx, cur.seq, s.NowMs()); err != nil {
			return bus.TaskUpdateResult{}, core.FromStore(err)
		}
		return bus.TaskUpdateResult{Task: cur.Task, Replaced: cur.seq, Deleted: true}, nil
	}
	if reflect.DeepEqual(patched, cur) {
		return bus.TaskUpdateResult{Task: cur.Task}, nil
	}
	newTask, err := s.writeRevision(ctx, tx, ch, identity, kind, patched, "", fx)
	if err != nil {
		return bus.TaskUpdateResult{}, err
	}
	ts, err := loadTasks(ctx, tx, ch)
	if err != nil {
		return bus.TaskUpdateResult{}, err
	}
	final := taskByID(ts, newTask.ID)
	if final == nil {
		return bus.TaskUpdateResult{}, bus.Internal(errors.New("task missing after update"))
	}
	return bus.TaskUpdateResult{Task: final.Task, Replaced: cur.seq}, nil
}

// Claim is Update {owner: caller, status: in_progress, leased_until}.
func (s Service) Claim(ctx context.Context, identity string, in bus.TaskClaimInput) (bus.TaskUpdateResult, error) {
	st := "in_progress"
	p := bus.TaskPatch{ID: in.ID, Owner: &identity, Status: &st, IdempotencyKey: in.IdempotencyKey}
	if in.LeasedUntil != 0 {
		p.LeasedUntil = &in.LeasedUntil
	}
	return s.Update(ctx, identity, p)
}

// Release is Update {owner: "", status: pending, leased_until: 0}.
func (s Service) Release(ctx context.Context, identity string, in bus.TaskReleaseInput) (bus.TaskUpdateResult, error) {
	st, none, zero := "pending", "", int64(0)
	return s.Update(ctx, identity, bus.TaskPatch{ID: in.ID, Owner: &none, Status: &st, LeasedUntil: &zero, IdempotencyKey: in.IdempotencyKey})
}
