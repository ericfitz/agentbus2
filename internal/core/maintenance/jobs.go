package maintenance

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/embed"
	"github.com/ericfitz/agentbus2/internal/store"
)

const hourMs = 3_600_000

// lapsedSessions deletes sessions whose lease lapsed before the retention
// window, closed ones included, with their cursors (Decision 79; v1
// internal/bus/sessions.go stale-session sweep).
func (s Service) lapsedSessions(ctx context.Context, now int64) (int, error) {
	cutoff := now - int64(s.Cfg.SessionRetentionHours)*hourMs
	return s.loopChunks(ctx, func() (n int, err error) {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			n, err = tx.DeleteSessions(ctx, cutoff, cleanupChunk)
			return err
		})
		return n, err
	})
}

// abandonedTasks reclaims the abandoned tasks of every task list (v1
// reclaimAllAbandonedTasks). One list failing does not stop the others.
func (s Service) abandonedTasks(ctx context.Context, now int64) (int, error) {
	var chs []store.Channel
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		chs, err = tx.ListChannelsByKind(ctx, store.KindTasks)
		return err
	})
	if err != nil {
		return 0, err
	}
	total := 0
	var errs []error
	for _, ch := range chs {
		if s.pastDeadline(ctx) {
			break
		}
		n, err := s.Tasks.ReclaimChannel(ctx, ch.ID, now)
		total += n
		if err != nil && bus.CodeOf(err) != bus.CodeNotFound { // deleted meanwhile
			errs = append(errs, err)
		}
	}
	return total, errors.Join(errs...)
}

// receipts deletes expired idempotency receipts.
func (s Service) receipts(ctx context.Context, now int64) (int, error) {
	return s.loopChunks(ctx, func() (n int, err error) {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			n, err = tx.DeleteExpiredReceipts(ctx, now, cleanupChunk)
			return err
		})
		return n, err
	})
}

// messageRetention deletes ordinary messages older than MessageRetentionHours,
// oldest first, and raises each affected channel's evicted-before mark to the
// highest deleted seq plus one (v1 deleteOrdinaryChunk).
func (s Service) messageRetention(ctx context.Context, now int64) (int, error) {
	cutoff := now - int64(s.Cfg.MessageRetentionHours)*hourMs
	return s.loopChunks(ctx, func() (int, error) { return s.deleteOrdinaryChunk(ctx, cutoff) })
}

// deleteOrdinaryChunk deletes one chunk of ordinary messages created before
// createdBefore, oldest first, in one transaction, and raises each affected
// channel's evicted-before mark to the highest deleted seq plus one (v1
// deleteOrdinaryChunk).
func (s Service) deleteOrdinaryChunk(ctx context.Context, createdBefore int64) (n int, err error) {
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		n = 0
		rows, err := tx.OrdinaryMessagesBefore(ctx, createdBefore, cleanupChunk)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		seqs := make([]int64, len(rows))
		maxByChannel := map[int64]int64{}
		for i, r := range rows {
			seqs[i] = r.Seq
			maxByChannel[r.ChannelID] = max(maxByChannel[r.ChannelID], r.Seq)
		}
		if err := tx.DeleteMessages(ctx, seqs); err != nil {
			return err
		}
		for id, m := range maxByChannel {
			if err := tx.SetEvictedBefore(ctx, id, m+1); err != nil {
				return err
			}
		}
		n = len(rows)
		return nil
	})
	return n, err
}

// tombstonePurge deletes tombstones older than TombstoneMinHours.
func (s Service) tombstonePurge(ctx context.Context, now int64) (int, error) {
	before := now - int64(s.Cfg.TombstoneMinHours)*hourMs
	return s.loopChunks(ctx, func() (n int, err error) {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			n = 0
			seqs, err := tx.ExpiredTombstones(ctx, before, cleanupChunk)
			if err != nil || len(seqs) == 0 {
				return err
			}
			if err := tx.DeleteMessages(ctx, seqs); err != nil {
				return err
			}
			n = len(seqs)
			return nil
		})
		return n, err
	})
}

// taskExpiry deletes every task list inactive for TaskExpiryHours, with its
// tasks whatever their status and its subscriptions (v1 taskExpiry; 0
// disables). Candidates are collected first; each is re-checked against the
// same cutoff inside the deleting transaction, which closes the gap against a
// task write landing in between.
func (s Service) taskExpiry(ctx context.Context, now int64) (int, error) {
	if s.Cfg.TaskExpiryHours <= 0 {
		return 0, nil
	}
	cutoff := now - int64(s.Cfg.TaskExpiryHours)*hourMs
	var stale []store.Channel
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		stale = nil
		chs, err := tx.ListChannelsByKind(ctx, store.KindTasks)
		if err != nil {
			return err
		}
		for _, ch := range chs {
			last, err := tx.ChannelLastActivity(ctx, ch.ID)
			if err != nil {
				return err
			}
			if last < cutoff {
				stale = append(stale, ch)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, ch := range stale {
		if s.pastDeadline(ctx) {
			break
		}
		var gone bool
		err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			gone = false
			last, err := tx.ChannelLastActivity(ctx, ch.ID)
			if err != nil {
				return err
			}
			if last >= cutoff {
				return nil
			}
			if err := tx.DeleteChannel(ctx, ch.ID); err != nil {
				return err
			}
			gone = true
			return nil
		})
		if errors.Is(err, store.ErrNotFound) {
			continue // deleted concurrently
		}
		if err != nil {
			return deleted, err
		}
		if gone {
			deleted++
			s.Log.Info("task channel expired", "channel", ch.Name)
		}
	}
	return deleted, nil
}

// emptyChannels deletes every ordinary channel, except `general`, that has no
// messages and no subscriptions and was created more than
// EmptyChannelReapHours ago (v1 internal/bus/channels.go reapEmptyChannels;
// Decision (Eric, 2026-10-21), Milestone 3 item 8). Task lists and memory
// channels are never ordinary, so the Store never offers them. Candidates are
// listed first and each is deleted by a conditional statement that re-checks
// emptiness, so a message or subscription landing in between keeps its
// channel. Idempotent: a second run finds nothing.
func (s Service) emptyChannels(ctx context.Context, now int64) (int, error) {
	cutoff := now - int64(s.Cfg.EmptyChannelReapHours)*hourMs
	var cands []store.Channel
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		cands, err = tx.EmptyChannelsBefore(ctx, cutoff)
		return err
	})
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, ch := range cands {
		if ch.Name == channels.GeneralChannel {
			continue
		}
		if s.pastDeadline(ctx) {
			break
		}
		var gone bool
		err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			gone, err = tx.DeleteChannelIfEmpty(ctx, ch.ID, cutoff)
			return err
		})
		if err != nil {
			return deleted, err
		}
		if gone {
			deleted++
			s.Log.Info("empty channel reaped", "channel", ch.Name)
		}
	}
	return deleted, nil
}

// memoryExpiry tombstones live memories not accessed for MemoryExpiryHours
// (v1 memoryExpiryChunk; 0 disables). Tasks are never memories to expire: the
// Store's ExpiredMemories excludes tasks-kind channels.
func (s Service) memoryExpiry(ctx context.Context, now int64) (int, error) {
	if s.Cfg.MemoryExpiryHours <= 0 {
		return 0, nil
	}
	cutoff := now - int64(s.Cfg.MemoryExpiryHours)*hourMs
	expired := 0
	_, err := s.loopChunks(ctx, func() (selected int, err error) {
		var done int
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			selected, done = 0, 0
			refs, err := tx.ExpiredMemories(ctx, cutoff, cleanupChunk)
			if err != nil {
				return err
			}
			selected = len(refs)
			for _, r := range refs {
				ok, err := s.TombstoneMemory(ctx, tx, r.MemoryID, r.Seq, now)
				if err != nil {
					return err
				}
				if ok {
					done++
				}
			}
			return nil
		})
		if err == nil {
			expired += done
		}
		return selected, err
	})
	return expired, err
}

// embeddings runs up to embedBatchesPT embedding batches. The HTTP calls carry
// embed.RequestTimeout in real time (the endpoint does not read the fake
// clock).
func (s Service) embeddings(ctx context.Context, _ int64) (int, error) {
	total := 0
	for i := 0; i < embedBatchesPT && !s.pastDeadline(ctx); i++ {
		n, err := embed.EmbedBatch(ctx, s.Core)
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
	return total, nil
}
