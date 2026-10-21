package tasks

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Service is the task aggregate over a Core (Decision 7). Every operation
// takes the acting identity; local.Bus binds it.
type Service struct{ *core.Core }

// effects are what a transaction body leaves for after the commit: the rate
// limiter charge and the notifier hint. A body resets them on every run,
// because Store.Append may run it more than once (Decision 26).
type effects struct {
	channelID int64
	seq       int64 // the last row written, 0 when none
	charged   bool
	bytes     int
}

// finish runs the post-commit effects of a write that was not a receipt
// replay (a replay wrote nothing and charges nothing).
func (s Service) finish(identity string, fx effects) {
	if fx.charged {
		s.Limits.Charge(identity, fx.bytes, s.Now())
	}
	if fx.seq != 0 {
		s.Publish(notify.Hint{ChannelID: fx.channelID, Seq: fx.seq})
	}
}

// rateCheck refuses with rate_limited, charging nothing, when the identity's
// buckets cannot pay for a row of n bytes; the charge is taken after the
// commit (finish).
func (s Service) rateCheck(identity string, n int, fx *effects) error {
	if !s.Limits.Check(identity, n, s.Now()) {
		return core.E(bus.CodeRateLimited, "send_messages_per_second rate limit exceeded for %s", identity)
	}
	fx.charged, fx.bytes = true, n
	return nil
}

// taskChannel looks a channel up by name and requires the tasks kind.
func (s Service) taskChannel(ctx context.Context, tx store.Tx, name string) (store.Channel, error) {
	ch, err := s.ChannelByName(ctx, tx, name)
	if err != nil {
		return store.Channel{}, err
	}
	if ch.Kind != store.KindTasks {
		return store.Channel{}, core.E(bus.CodeValidation, "%q is not a task list", name)
	}
	return ch, nil
}

// taskChannelOf returns the channel of the live revision of memory id, which
// must be a task list.
func (s Service) taskChannelOf(ctx context.Context, tx store.Tx, id int64) (store.Channel, error) {
	_, ch, err := s.LiveRevision(ctx, tx, id)
	if err != nil {
		return store.Channel{}, err
	}
	if ch.Kind != store.KindTasks {
		return store.Channel{}, core.E(bus.CodeNotFound, "memory %d is not a task", id)
	}
	return ch, nil
}

// ownerKnown is rule 6 (Decision 81): an owner must be a registered
// identity. The reserved names everyone and admins are not.
func (s Service) ownerKnown(ctx context.Context, tx store.Tx) func(string) (bool, error) {
	return func(owner string) (bool, error) {
		if owner == "" || owner == store.Everyone || owner == store.Admins {
			return false, nil
		}
		_, err := tx.Identity(ctx, owner)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, core.FromStore(err)
		}
		return true, nil
	}
}

// writeRevision tombstones t's live revision and appends the next one,
// returning t with seq, Revision, UpdatedAt and UpdatedBy updated (v1
// writeTaskRevision). It runs inside the caller's Store.Append transaction.
// as is "" for the reclaim.
func (s Service) writeRevision(ctx context.Context, tx store.Tx, ch store.Channel, as string, t task, typ string, fx *effects) (task, error) {
	doc, err := taskContent(t.Task)
	if err != nil {
		return task{}, err
	}
	now := s.NowMs()
	if _, err := tx.TombstoneMessage(ctx, t.seq, now); err != nil {
		return task{}, core.FromStore(err)
	}
	seq, _, err := s.Append(ctx, tx, core.AppendInput{
		ChannelID: ch.ID, Channel: ch.Name, Kind: store.KindTasks, Sender: as,
		Type: typ, Subject: t.Subject, Content: doc, TaskRow: true,
	})
	if err != nil {
		return task{}, err
	}
	if err := tx.SetMemory(ctx, seq, t.ID, t.Revision+1); err != nil {
		return task{}, core.FromStore(err)
	}
	fx.channelID, fx.seq = ch.ID, seq
	t.seq = seq
	t.Revision++
	t.UpdatedAt = now
	t.UpdatedBy = as
	return t, nil
}

// envelopeSize is the size gate for a task row.
func (s Service) envelopeSize(ch store.Channel, as, typ string, t bus.Task) (int, error) {
	doc, err := taskContent(t)
	if err != nil {
		return 0, err
	}
	return s.EnvelopeSize(core.AppendInput{
		Channel: ch.Name, Kind: store.KindTasks, Sender: as, Type: typ, Subject: t.Subject, Content: doc,
	}, as)
}
