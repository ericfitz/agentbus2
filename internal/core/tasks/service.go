package tasks

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
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

// taskChannel looks a channel up by name, requires action on it (the scope
// gate runs before any kind check, so a scoped caller learns nothing about a
// channel outside its scope) and then requires the tasks kind.
func (s Service) taskChannel(ctx context.Context, tx store.Tx, pr access.Principal, name string, action access.Action) (store.Channel, error) {
	ch, err := s.ChannelByName(ctx, tx, name)
	if err != nil {
		return store.Channel{}, err
	}
	if err := s.Access.Require(ctx, tx, pr, access.Channel{ID: ch.ID}, action); err != nil {
		return store.Channel{}, err
	}
	if ch.Kind != store.KindTasks {
		return store.Channel{}, core.E(bus.CodeValidation, "%q is not a task list", name)
	}
	return ch, nil
}

// taskChannelOf returns the channel of the live revision of memory id, which
// must be a task list. The access check on action runs before the kind check,
// and a channel outside the caller's scope reads as an unknown id.
func (s Service) taskChannelOf(ctx context.Context, tx store.Tx, pr access.Principal, id int64, action access.Action) (store.Channel, error) {
	_, ch, err := s.LiveRevision(ctx, tx, id)
	if err != nil {
		return store.Channel{}, err
	}
	if err := s.Access.Require(ctx, tx, pr, access.Channel{ID: ch.ID}, action, access.WithNotFound(core.MemoryNotLiveFmt, id)); err != nil {
		return store.Channel{}, err
	}
	if ch.Kind != store.KindTasks {
		return store.Channel{}, core.E(bus.CodeNotFound, "memory %d is not a task", id)
	}
	return ch, nil
}

// ownerKnown is rule 6 (Decision 81): an owner must be a registered
// identity. The reserved names everyone and admins are not.
func (s Service) ownerKnown(ctx context.Context, tx store.Tx, pr access.Principal) func(string) (bool, error) {
	return func(owner string) (bool, error) {
		if owner == "" || owner == store.Everyone || owner == store.Admins {
			return false, nil
		}
		d, err := s.Access.Check(ctx, tx, pr, access.Identity{Name: owner}, access.See)
		if err != nil {
			return false, err
		}
		return d.Allowed, nil
	}
}

// writeRevision tombstones t's live revision and appends the next one,
// returning t with seq, Revision, UpdatedAt and UpdatedBy updated (v1
// writeTaskRevision). It runs inside the caller's Store.Append transaction.
// as is "" for the reclaim, which is marked agent: a system write.
func (s Service) writeRevision(ctx context.Context, tx store.Tx, ch store.Channel, as string, kind store.IdentityKind, t task, typ string, fx *effects) (task, error) {
	doc, err := taskContent(t.Task)
	if err != nil {
		return task{}, err
	}
	now := s.NowMs()
	if _, err := tx.TombstoneMessage(ctx, t.seq, now); err != nil {
		return task{}, core.FromStore(err)
	}
	seq, _, err := s.Append(ctx, tx, core.AppendInput{
		ChannelID: ch.ID, Channel: ch.Name, Kind: store.KindTasks, Sender: as, SenderKind: kind,
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
