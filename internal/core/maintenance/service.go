// Package maintenance runs the periodic cleanup jobs under the Store's
// cross-process lock (Decisions 49, 68, 79). Ported from v1 internal/bus/maintenance.go.
package maintenance

import (
	"context"
	"fmt"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/tasks"
)

const (
	// cleanupChunk is the rows one transaction of a chunked job touches (v1
	// cleanupChunk).
	cleanupChunk = 1000
	// embedBatchesPT is the embedding batches one tick runs (v1 embedBatchesPT).
	embedBatchesPT = 4

	lockName = "maintenance"
)

// Service is the maintenance aggregate over a Core.
type Service struct {
	*core.Core
	Tasks tasks.Service
	// BudgetOverride is the SQLite budget in bytes; tests only. 0 means
	// Cfg.SQLiteBudgetMiB << 20.
	BudgetOverride int64

	// jobs replaces Jobs() when non-nil (tests drive Tick with fakes).
	jobs []Job
}

// New builds a Service over c.
func New(c *core.Core) Service { return Service{Core: c, Tasks: tasks.Service{Core: c}} }

// Job is one maintenance job. Run returns how many rows it changed.
type Job struct {
	Name string
	Run  func(ctx context.Context, now int64) (n int, err error)
}

// JobResult is the outcome of one job in a tick.
type JobResult struct {
	Name    string
	N       int
	Err     error
	Skipped bool // the deadline passed before the job ran
}

// deadlineKey carries a tick's deadline (Unix ms on the injected clock) in
// the context the jobs receive.
type deadlineKey struct{}

// pastDeadline reports whether the tick's deadline, if any, has passed on the
// injected clock. It is compared with the clock, never a wall-clock context
// deadline, so tests drive it by advancing the fake clock (Decision 49).
func (s Service) pastDeadline(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	d, ok := ctx.Value(deadlineKey{}).(int64)
	return ok && s.NowMs() >= d
}

// Jobs lists the jobs in tick order.
func (s Service) Jobs() []Job {
	if s.jobs != nil {
		return s.jobs
	}
	return []Job{
		{"lapsed sessions", s.lapsedSessions},
		// After "lapsed sessions" so an owner whose session was just deleted
		// counts as dead in the same tick.
		{"abandoned tasks", s.abandonedTasks},
		{"receipts", s.receipts},
		{"message retention", s.messageRetention},
		{"tombstone purge", s.tombstonePurge},
		{"task expiry", s.taskExpiry},
		{"memory expiry", s.memoryExpiry},
		{"empty channels", s.emptyChannels},
		{"budget eviction", s.evictToBudget},
		{"capacity notices", s.capacityNotice},
		{"embeddings", s.embeddings},
	}
}

// Tick runs one maintenance pass if this core holds the maintenance lock
// (Store.TryLock; v1 Tick, takeLease). It calls TryLock on every tick, before
// any job, which renews the hold and detects a lock lost with a Postgres
// session. Another owner holding it is not an error: ran is false and nothing
// else happens (Decision 68). The lock is held across ticks and released by
// Release at shutdown. Each job runs in transactions of its own, chunked; an
// error is logged and recorded in its JobResult and never aborts the tick.
// Once the deadline (half the interval) has passed, the remaining jobs are
// Skipped.
//
// State: every job is idempotent, so a second tick finds nothing; two
// overlapping ticks are serialized by the lock; a tick killed mid-job leaves
// committed chunks and a lock that lapses at twice the interval (SQLite) or
// when the session ends (Postgres), after which the next tick continues from
// wherever the queries find work.
func (s Service) Tick(ctx context.Context) (ran bool, report []JobResult) {
	now := s.NowMs()
	interval := int64(s.Cfg.MaintenanceIntervalSeconds) * 1000
	held, err := s.Store.TryLock(ctx, lockName, s.Owner, now, now+2*interval)
	if err != nil {
		s.Log.Warn("maintenance lock", "err", err)
		return false, nil
	}
	if !held {
		return false, nil
	}
	dctx := context.WithValue(ctx, deadlineKey{}, now+interval/2)
	for _, j := range s.Jobs() {
		if s.pastDeadline(dctx) {
			s.Log.Info("maintenance deadline reached", "at", j.Name)
			report = append(report, JobResult{Name: j.Name, Skipped: true})
			continue
		}
		n, err := j.Run(dctx, s.NowMs())
		if err != nil {
			s.Log.Warn("maintenance job failed", "job", j.Name, "err", err)
		}
		report = append(report, JobResult{Name: j.Name, N: n, Err: err})
	}
	return true, report
}

// Release gives up the maintenance lock; Serve calls it once at shutdown. A
// lock this core does not hold is left alone and the error is nil.
func (s Service) Release(ctx context.Context) error {
	return s.Store.Unlock(ctx, lockName, s.Owner)
}

// RunJob runs one job by name, without the lock and without a deadline (tests
// and the operator command of Milestone 7). An unknown name is a validation
// error naming the known jobs.
func (s Service) RunJob(ctx context.Context, name string) (int, error) {
	jobs := s.Jobs()
	names := make([]string, len(jobs))
	for i, j := range jobs {
		if j.Name == name {
			return j.Run(ctx, s.NowMs())
		}
		names[i] = j.Name
	}
	return 0, core.E(bus.CodeValidation, "unknown maintenance job %q (known: %s)", name, strings.Join(names, ", "))
}

// loopChunks repeats step until it reports fewer than cleanupChunk rows (v1
// loopChunks) or the deadline passes, and returns the sum of the counts.
func (s Service) loopChunks(ctx context.Context, step func() (int, error)) (int, error) {
	total := 0
	for !s.pastDeadline(ctx) {
		n, err := step()
		total += n
		if err != nil {
			return total, fmt.Errorf("chunk: %w", err)
		}
		if n < cleanupChunk {
			break
		}
	}
	return total, nil
}
