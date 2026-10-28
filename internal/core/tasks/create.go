package tasks

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Create adds a task to a task list, positioned among its siblings, with its
// parent and blocked_by links validated (v1 TaskCreate). Order: shape checks,
// identity, the receipt (so a keyed retry replays even after the list is
// gone), the channel and its kind, write, links, rank, size gate, rate limit,
// the append. The rate limiter is charged after the commit. With an inspector
// configured the create runs under write inspection (core.RunInspected).
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
	prepare := func(ctx context.Context, tx store.Tx) (core.Prepared, error) {
		pr, err := access.Resolve(ctx, tx, identity)
		if err != nil {
			return core.Prepared{}, err
		}
		var r bus.Task
		hit, _, err := s.CheckReceipt(ctx, tx, identity, key, in, &r)
		if err != nil || hit {
			return core.Prepared{}, err // a replay needs no inspection
		}
		pl, err := s.planCreate(ctx, tx, identity, pr, in, true, &effects{})
		if err != nil {
			return core.Prepared{}, err
		}
		return taskPrepared(core.OpTaskCreate, identity, pr, pl.ch, 0, pl.t.Task, pl.size), nil
	}
	write := func(want string) error {
		err := s.Store.Append(ctx, func(ctx context.Context, tx store.Tx) error {
			out, fx, replayed = bus.Task{}, effects{}, false // fn may run again (Decision 26)
			pr, err := access.Resolve(ctx, tx, identity)
			if err != nil {
				return err
			}
			replayed, err = s.WithReceipt(ctx, tx, identity, key, in, &out, func() error {
				var err error
				out, err = s.create(ctx, tx, identity, pr, in, &fx, want)
				return err
			})
			return err
		})
		if errors.Is(err, core.ErrInspectedChanged) {
			return err
		}
		return core.FromStore(err)
	}
	if err := s.RunInspected(ctx, identity, prepare, write); err != nil {
		return bus.Task{}, core.FromStore(err)
	}
	if !replayed {
		s.finish(identity, fx)
	}
	return out, nil
}

// createPlan is what planCreate decides: the list, the new task and the
// envelope size.
type createPlan struct {
	ch   store.Channel
	t    task
	size int
}

// planCreate runs create up to the rate check. It writes nothing, except the
// sibling rerank of a corrupt list, which probe (the read pre-pass) replaces
// with the provisional rank V (open note write-inspection 10).
func (s Service) planCreate(ctx context.Context, tx store.Tx, identity string, pr access.Principal, in bus.TaskCreateInput, probe bool, fx *effects) (createPlan, error) {
	kind := pr.Kind
	ch, err := s.taskChannel(ctx, tx, pr, in.Channel, access.Write)
	if err != nil {
		return createPlan{}, err
	}
	ts, err := loadTasks(ctx, tx, ch)
	if err != nil {
		return createPlan{}, err
	}
	t := task{Task: bus.Task{
		Channel: in.Channel, Subject: in.Subject, Description: in.Description, Status: "pending",
		Parent: in.Parent, Rank: "V", BlockedBy: dedupeIDs(in.BlockedBy), Metadata: in.Metadata,
	}}
	if err := validateTaskLinks(ts, t); err != nil {
		return createPlan{}, err
	}
	rank, err := placeRank(ts, 0, in.Parent, in.Before, in.After)
	var rc *rankCollision
	if errors.As(err, &rc) {
		if probe {
			rank, err = "V", nil
		} else {
			if ts, err = s.rerankSiblings(ctx, tx, ch, identity, kind, ts, rc.parent, fx); err != nil {
				return createPlan{}, err
			}
			rank, err = placeRank(ts, 0, in.Parent, in.Before, in.After)
		}
	}
	if err != nil {
		return createPlan{}, err
	}
	t.Rank = rank
	size, err := s.envelopeSize(ch, identity, "", t.Task)
	if err != nil {
		return createPlan{}, err
	}
	if err := s.rateCheck(identity, size, fx); err != nil {
		return createPlan{}, err
	}
	return createPlan{ch: ch, t: t, size: size}, nil
}

// create plans the task, checks the digest the inspector judged (want, "" for
// none) and appends it.
func (s Service) create(ctx context.Context, tx store.Tx, identity string, pr access.Principal, in bus.TaskCreateInput, fx *effects, want string) (bus.Task, error) {
	kind := pr.Kind
	pl, err := s.planCreate(ctx, tx, identity, pr, in, false, fx)
	if err != nil {
		return bus.Task{}, err
	}
	if want != "" && taskPrepared(core.OpTaskCreate, identity, pr, pl.ch, 0, pl.t.Task, pl.size).Digest() != want {
		return bus.Task{}, core.ErrInspectedChanged
	}
	ch, t := pl.ch, pl.t
	doc, err := taskContent(t.Task)
	if err != nil {
		return bus.Task{}, err
	}
	seq, _, err := s.Append(ctx, tx, core.AppendInput{
		ChannelID: ch.ID, Channel: ch.Name, Kind: store.KindTasks, Sender: identity, SenderKind: kind,
		Subject: t.Subject, Content: doc, TaskRow: true,
	})
	if err != nil {
		return bus.Task{}, err
	}
	fx.channelID, fx.seq = ch.ID, seq
	ts, err := loadTasks(ctx, tx, ch)
	if err != nil {
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
func (s Service) rerankSiblings(ctx context.Context, tx store.Tx, ch store.Channel, as string, kind store.IdentityKind, ts []task, parent int64, fx *effects) ([]task, error) {
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
		if _, err := s.writeRevision(ctx, tx, ch, as, kind, t, "reranked", fx); err != nil {
			return nil, err
		}
	}
	return loadTasks(ctx, tx, ch)
}
