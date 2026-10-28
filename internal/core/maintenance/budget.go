package maintenance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// capacityKind is the notice kind Status and Receive report.
const capacityKind = "capacity"

const (
	// idleTaskChunk is the idle task lists one chunk deletes, each in a
	// transaction of its own (a list cascades to its tasks, so its size is
	// unbounded): the allowance is overshot by at most this many lists.
	idleTaskChunk = 16
	// idleMemoryChunk is the idle memories one chunk purges in one transaction.
	idleMemoryChunk = 100
)

// sweepOutcome is what evictToBudget leaves for capacityNotice in the same
// tick: the usage the sweep ended with. Tick puts a pointer to one in the
// context of its jobs; RunJob does not, so a job run alone measures for
// itself.
type sweepOutcome struct {
	final store.Usage
	done  bool
}

type outcomeKey struct{}

// measure is the engine's enforced measure (Store.Usage unless a test
// replaced it).
func (s Service) measure(ctx context.Context) (store.Usage, error) {
	if s.usage != nil {
		return s.usage(ctx)
	}
	return s.Store.Usage(ctx)
}

// budget is the enforced budget in bytes and the name of the engine's
// setting: BudgetOverride (tests; the name stays the engine's) else the
// setting.
func (s Service) budget() (bytes int64, setting string, err error) {
	bytes, setting, err = core.Budget(s.Cfg, s.Store.Engine())
	if err != nil {
		return 0, "", err
	}
	if s.BudgetOverride > 0 {
		bytes = s.BudgetOverride
	}
	return bytes, setting, nil
}

// sweepStep is one step of the backstop: a chunk function that deletes one
// bounded batch and reports the rows and the bytes it deleted.
type sweepStep struct {
	name string
	// cutoff is the time (Unix ms) the step deletes before; 0 for a step with
	// no age cutoff. The sweep log records it.
	cutoff int64
	run    func(ctx context.Context) (rows int, bytes int64, err error)
}

// evictToBudget is the budget backstop on both engines (Eric's decisions
// 2026-10-27, ADR 0004 Postgres budget addendum). The age jobs run first; this
// job starts only once usage exceeds the budget, then deletes in Eric's order
// (orphaned bridge rows; expired tombstones, then expired enrollment tokens;
// idle task lists, then idle memories; ordinary messages) until usage is at the
// target or the tick's allowance (usage less the target, measured at the start)
// is spent, whichever is first. The floors hold under any pressure: an
// ordinary message younger than MessageFloorHours and a tombstone younger than
// TombstoneFloorHours are never offered; a task list or memory with activity
// inside IdleWindowHours is never offered.
//
// The counted bytes (the Store's size_bytes sums, cascades included) are what
// the allowance spends, so a measure that does not fall at once (SQLite pages
// not yet reclaimed, Postgres bloat) cannot make one tick delete everything.
// Where the measure is not Logical (SQLite) the sweep also reclaims freed pages
// and re-reads usage before each chunk, so it ends at the target without
// waiting for the allowance. The sweep logs a warning when it deleted rows and
// usage did not fall.
//
// State: a second run at or below the budget deletes nothing (the start test);
// overlapping runs are serialized by the maintenance lock (a failover overlap
// is bounded by two allowances, and the floors hold); a sweep killed midway
// leaves committed chunks, each with its evicted_before_seq, and no tier or
// counter state to repair.
func (s Service) evictToBudget(ctx context.Context, now int64) (int, error) {
	u0, err := s.measure(ctx)
	if err != nil {
		return 0, err
	}
	budget, _, err := s.budget()
	if err != nil {
		return 0, err
	}
	target := core.TargetBytes(budget, s.Cfg.CleanupFreePercent)
	final := u0
	total := 0
	if u0.Bytes > budget {
		allowance := u0.Bytes - target
		var deleted int64
		remaining := func() int64 { return allowance - deleted }
		cut := core.EvictionCutoffs(s.Cfg, now)
		steps := []sweepStep{
			{"orphaned bridge messages", 0, s.orphanChunk},
			{"expired tombstones", cut.TombstoneBefore, func(ctx context.Context) (int, int64, error) {
				return s.expiredTombstoneChunk(ctx, cut.TombstoneBefore)
			}},
			{"expired enrollment tokens", now, func(ctx context.Context) (int, int64, error) { return s.expiredTokenChunk(ctx, now) }},
			{"idle task lists", cut.IdleBefore, func(ctx context.Context) (int, int64, error) {
				return s.idleTaskChannelChunk(ctx, cut.IdleBefore, remaining())
			}},
			{"idle memories", cut.IdleBefore, func(ctx context.Context) (int, int64, error) {
				return s.idleMemoryChunk(ctx, cut.IdleBefore, remaining())
			}},
			{"ordinary messages", cut.MessageBefore, func(ctx context.Context) (int, int64, error) {
				return s.deleteOrdinaryChunk(ctx, cut.MessageBefore)
			}},
		}
		perStep := make([]any, 0, 2*len(steps))
		for _, st := range steps {
			stepRows := 0
			reached := false
			for deleted < allowance && !s.pastDeadline(ctx) {
				if !u0.Logical {
					if err := s.Store.Reclaim(ctx); err != nil {
						return total, err
					}
					u, err := s.measure(ctx)
					if err != nil {
						return total, err
					}
					if u.Bytes <= target {
						reached = true
						break
					}
				}
				rows, bytes, err := st.run(ctx)
				total += rows
				stepRows += rows
				deleted += bytes
				if err != nil {
					return total, err
				}
				if rows == 0 {
					break
				}
			}
			perStep = append(perStep, st.name, stepRows)
			if st.cutoff != 0 {
				perStep = append(perStep, st.name+" cutoff", time.UnixMilli(st.cutoff).UTC().Format(time.RFC3339))
			}
			if reached {
				break
			}
		}
		if final, err = s.measure(ctx); err != nil {
			return total, err
		}
		if total > 0 && final.Bytes >= u0.Bytes {
			s.Log.Warn("budget eviction did not lower usage", "before", u0.Bytes, "after", final.Bytes, "deleted_bytes", deleted)
		}
		s.Log.Info("budget eviction swept", append([]any{"engine", s.Store.Engine(), "before", u0.Bytes, "after", final.Bytes,
			"allowance", allowance, "deleted_bytes", deleted}, perStep...)...)
	}
	if o, ok := ctx.Value(outcomeKey{}).(*sweepOutcome); ok {
		o.final, o.done = final, true
	}
	return total, s.Store.Reclaim(ctx)
}

// orphanChunk deletes one chunk of bridge_messages rows whose message is gone.
// The bytes are those of the rows named: DeleteBridgeMessages skips a ref
// that is no longer orphaned, but a seq is never reused (Store.NextSeq is a
// counter), so a row once orphaned stays orphaned and the count is exact.
func (s Service) orphanChunk(ctx context.Context) (n int, bytes int64, err error) {
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		n, bytes = 0, 0
		refs, err := tx.OrphanedBridgeMessages(ctx, cleanupChunk)
		if err != nil || len(refs) == 0 {
			return err
		}
		if err := tx.DeleteBridgeMessages(ctx, refs); err != nil {
			return err
		}
		n = len(refs)
		for _, r := range refs {
			bytes += r.Bytes
		}
		return nil
	})
	return n, bytes, err
}

// expiredTombstoneChunk deletes one chunk of tombstones older than before.
func (s Service) expiredTombstoneChunk(ctx context.Context, before int64) (n int, bytes int64, err error) {
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		n, bytes = 0, 0
		refs, err := tx.ExpiredTombstones(ctx, before, cleanupChunk)
		if err != nil || len(refs) == 0 {
			return err
		}
		seqs := make([]int64, len(refs))
		var b int64
		for i, r := range refs {
			seqs[i] = r.Seq
			b += r.Bytes
		}
		if err := tx.DeleteMessages(ctx, seqs); err != nil {
			return err
		}
		n, bytes = len(seqs), b
		return nil
	})
	return n, bytes, err
}

// expiredTokenChunk deletes one chunk of enrollment tokens that expired
// before now.
func (s Service) expiredTokenChunk(ctx context.Context, now int64) (n int, bytes int64, err error) {
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		n, bytes = 0, 0
		refs, err := tx.ExpiredEnrollmentTokens(ctx, now, cleanupChunk)
		if err != nil || len(refs) == 0 {
			return err
		}
		ids := make([]string, len(refs))
		var b int64
		for i, r := range refs {
			ids[i] = r.ID
			b += r.Bytes
		}
		if err := tx.DeleteEnrollmentTokens(ctx, ids); err != nil {
			return err
		}
		n, bytes = len(ids), b
		return nil
	})
	return n, bytes, err
}

// idleTaskChannelChunk deletes up to idleTaskChunk task lists with no activity
// since idleBefore, and stops once the counted bytes reach remaining (the
// sweep's allowance left; each list is atomic, so the overshoot is at most one
// list), each in a transaction of its own: ChannelBytes, then the
// conditional delete, which re-checks idleness, so a write landing after the
// listing keeps its list. A false result counts nothing; ErrNotFound (a list
// deleted since the listing) counts nothing and the chunk goes on.
//
// open note: postgres-budget 2: a TaskExpiryHours of 0 does not exempt task
// lists here. 0 disables the age rule; the backstop's own knob is
// idle_window_hours.
//
// Cost: the listing evaluates each task list's latest activity, which scans
// all of its messages, so one listing costs O(messages of all task lists).
// It runs once per chunk and only above the budget, never on the age path.
func (s Service) idleTaskChannelChunk(ctx context.Context, idleBefore, remaining int64) (n int, bytes int64, err error) {
	var cands []store.Channel
	if err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var e error
		cands, e = tx.IdleTaskChannels(ctx, idleBefore, idleTaskChunk)
		return e
	}); err != nil {
		return 0, 0, err
	}
	for _, ch := range cands {
		if s.pastDeadline(ctx) {
			break
		}
		var gone bool
		var b int64
		err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			gone, b = false, 0
			size, err := tx.ChannelBytes(ctx, ch.ID)
			if err != nil {
				return err
			}
			if gone, err = tx.DeleteIdleTaskChannel(ctx, ch.ID, idleBefore); err != nil {
				return err
			}
			if gone {
				b = size
			}
			return nil
		})
		if errors.Is(err, store.ErrNotFound) {
			continue // deleted concurrently
		}
		if err != nil {
			return n, bytes, err
		}
		if gone {
			n++
			bytes += b
			s.Log.Info("idle task list evicted", "channel", ch.Name)
			if bytes >= remaining {
				break
			}
		}
	}
	return n, bytes, nil
}

// idleMemoryChunk purges up to idleMemoryChunk memories with no activity
// since idleBefore (a new revision or a read), every revision and the access
// row, in one transaction, and stops once the counted bytes reach remaining
// (the sweep's allowance left; each memory is atomic, so the overshoot is at
// most one memory). A memory is deleted outright, not tombstoned.
//
// open note: postgres-budget 2: a MemoryExpiryHours of 0 does not exempt
// memories here.
func (s Service) idleMemoryChunk(ctx context.Context, idleBefore, remaining int64) (n int, bytes int64, err error) {
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		n, bytes = 0, 0
		mems, err := tx.IdleMemories(ctx, idleBefore, idleMemoryChunk)
		if err != nil || len(mems) == 0 {
			return err
		}
		var b int64
		purged := 0
		for _, m := range mems {
			if err := tx.PurgeMemory(ctx, m.MemoryID); err != nil {
				return err
			}
			b += m.Bytes
			purged++
			if b >= remaining {
				break
			}
		}
		n, bytes = purged, b
		return nil
	})
	return n, bytes, err
}

// capacityNotice sets one of two capacity notices while usage exceeds the
// budget and clears it otherwise. The usage is the sweep's final value from
// the same tick when there was one, else a measure of its own. Above the
// budget the tier decides the text: exhausted (nothing left to evict) says
// capacity is exhausted, anything else says storage is over budget (health
// reports degraded for either, internal/core/health). It returns 1 when it
// set the notice, else 0. Idempotent.
func (s Service) capacityNotice(ctx context.Context, now int64) (int, error) {
	var u store.Usage
	if o, ok := ctx.Value(outcomeKey{}).(*sweepOutcome); ok && o.done {
		u = o.final
	} else {
		var err error
		if u, err = s.measure(ctx); err != nil {
			return 0, err
		}
	}
	budget, setting, err := s.budget()
	if err != nil {
		return 0, err
	}
	if u.Bytes <= budget {
		return 0, s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error { return tx.ClearNotice(ctx, capacityKind) })
	}
	target := core.TargetBytes(budget, s.Cfg.CleanupFreePercent)
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		left, err := tx.EvictionLeft(ctx, core.EvictionCutoffs(s.Cfg, now))
		if err != nil {
			return err
		}
		text := overBudgetText(setting, budget, u)
		if rules.StorageTier(u.Bytes, budget, target, left.Early(), left.Late()) == bus.PressureExhausted {
			text = exhaustedText(setting, budget, u)
		}
		return tx.SetNotice(ctx, store.Notice{Kind: capacityKind, Message: text, SetAt: now})
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func diskRatio(u store.Usage) float64 {
	if u.Bytes == 0 {
		return 0
	}
	return float64(u.DiskBytes) / float64(u.Bytes)
}

// overBudgetText is the notice while usage is over the budget and something
// is still evictable.
func overBudgetText(setting string, budget int64, u store.Usage) string {
	return fmt.Sprintf("agentbus2 storage is over budget: %s=%d MiB holds %d MiB (disk %d MiB, %.2fx); orphaned and expired rows, idle task lists and memories, and ordinary messages older than 1 hour are being deleted to make room",
		setting, budget>>20, u.Bytes>>20, u.DiskBytes>>20, diskRatio(u))
}

// exhaustedText is the notice when nothing is left to evict.
func exhaustedText(setting string, budget int64, u store.Usage) string {
	return fmt.Sprintf("agentbus2 capacity exhausted: %s=%d MiB (disk %d MiB, %.2fx) is filled by task lists and memories active within idle_window_hours, ordinary messages younger than 1 hour and tombstones younger than 72 hours, which are never evicted; raise %s and restart every replica, lower idle_window_hours, or have an administrator reset the bus to discard all data",
		setting, budget>>20, u.DiskBytes>>20, diskRatio(u), setting)
}
