package tasks

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Create adds a task to a task list, positioned among its siblings, with its
// parent and blocked_by links validated (v1 TaskCreate). Order: shape checks,
// identity, the receipt (so a keyed retry replays even after the list is
// gone), the channel and its kind, write, links, rank, size gate, rate limit,
// the append. The rate limiter is charged after the commit.
func (s Service) Create(ctx context.Context, identity string, in bus.TaskCreateInput) (bus.Task, error) {
	key := in.IdempotencyKey
	in.IdempotencyKey = ""
	if in.Channel == "" {
		return bus.Task{}, core.E(bus.CodeValidation, "channel is required")
	}
	if err := validateTaskSubject(in.Subject); err != nil {
		return bus.Task{}, err
	}
	var (
		out      bus.Task
		fx       effects
		replayed bool
	)
	err := s.Store.Append(ctx, func(ctx context.Context, tx store.Tx) error {
		out, fx, replayed = bus.Task{}, effects{}, false
		if _, err := s.RequireIdentity(ctx, tx, identity); err != nil {
			return err
		}
		var err error
		replayed, err = s.WithReceipt(ctx, tx, identity, key, in, &out, func() error {
			var err error
			out, err = s.create(ctx, tx, identity, in, &fx)
			return err
		})
		return err
	})
	if err != nil {
		return bus.Task{}, core.FromStore(err)
	}
	if !replayed {
		s.finish(identity, fx)
	}
	return out, nil
}

func (s Service) create(ctx context.Context, tx store.Tx, identity string, in bus.TaskCreateInput, fx *effects) (bus.Task, error) {
	ch, err := s.taskChannel(ctx, tx, in.Channel)
	if err != nil {
		return bus.Task{}, err
	}
	if err := s.Authorize(ctx, tx, identity, ch.ID, store.PermWrite); err != nil {
		return bus.Task{}, err
	}
	ts, err := loadTasks(ctx, tx, ch)
	if err != nil {
		return bus.Task{}, err
	}
	t := task{Task: bus.Task{
		Channel: in.Channel, Subject: in.Subject, Description: in.Description, Status: "pending",
		Parent: in.Parent, Rank: "V", BlockedBy: dedupeIDs(in.BlockedBy), Metadata: in.Metadata,
	}}
	if err := validateTaskLinks(ts, t); err != nil {
		return bus.Task{}, err
	}
	rank, err := placeRank(ts, 0, in.Parent, in.Before, in.After)
	var rc *rankCollision
	if errors.As(err, &rc) {
		if ts, err = s.rerankSiblings(ctx, tx, ch, identity, ts, rc.parent, fx); err != nil {
			return bus.Task{}, err
		}
		rank, err = placeRank(ts, 0, in.Parent, in.Before, in.After)
	}
	if err != nil {
		return bus.Task{}, err
	}
	t.Rank = rank
	size, err := s.envelopeSize(ch, identity, "", t.Task)
	if err != nil {
		return bus.Task{}, err
	}
	if err := s.rateCheck(identity, size, fx); err != nil {
		return bus.Task{}, err
	}
	doc, err := taskContent(t.Task)
	if err != nil {
		return bus.Task{}, err
	}
	seq, _, err := s.Append(ctx, tx, core.AppendInput{
		ChannelID: ch.ID, Channel: ch.Name, Kind: store.KindTasks, Sender: identity,
		Subject: t.Subject, Content: doc, TaskRow: true,
	})
	if err != nil {
		return bus.Task{}, err
	}
	fx.channelID, fx.seq = ch.ID, seq
	if ts, err = loadTasks(ctx, tx, ch); err != nil {
		return bus.Task{}, err
	}
	created := taskByID(ts, seq)
	if created == nil {
		return bus.Task{}, bus.Internal(errors.New("task missing after insert"))
	}
	return created.Task, nil
}

// rerankSiblings renumbers every task sharing the effective parent with
// strictly increasing ranks, keeping the current order (rank, then id), and
// returns the reloaded list. Only tasks whose rank changes get a "reranked"
// revision. It repairs an already corrupt list and is not rate-charged; the
// revisions carry the caller's name though it may not own the task.
func (s Service) rerankSiblings(ctx context.Context, tx store.Tx, ch store.Channel, as string, ts []task, parent int64, fx *effects) ([]task, error) {
	var sib []task
	for _, t := range ts {
		if effectiveParent(ts, t.Parent) == parent {
			sib = append(sib, t)
		}
	}
	sortSiblings(sib)
	rank := ""
	for _, t := range sib {
		if rank == "" {
			rank = "V"
		} else {
			rank = rules.RankAfter(rank)
		}
		if t.Rank == rank {
			continue
		}
		t.Rank = rank
		if _, err := s.writeRevision(ctx, tx, ch, as, t, "reranked", fx); err != nil {
			return nil, err
		}
	}
	return loadTasks(ctx, tx, ch)
}
