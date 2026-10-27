package channels

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Ported from v1 internal/bus/receive.go (Subscribe, Unsubscribe) and
// internal/bus/tags.go (SubscribeTags, UnsubscribeTags, TagSubscriptions),
// keyed by identity (Decision 37).

// Subscribe subscribes the identity to a channel it can read. From "" or
// "now" starts after the current high-water mark, "oldest" at the oldest
// retained message (max(evicted-1, 0)); anything else is validation. An
// existing subscription keeps its position.
func (s Service) Subscribe(ctx context.Context, identity string, in bus.SubscribeInput) error {
	from := in.From
	if from == "" {
		from = "now"
	}
	if from != "now" && from != "oldest" {
		return core.E(bus.CodeValidation, "from must be now or oldest")
	}
	payload := struct{ Op, Channel, From string }{"subscribe", in.Channel, from}
	var res struct{}
	return s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx, p access.Principal) error {
		ch, err := s.ChannelByName(ctx, tx, in.Channel)
		if err != nil {
			return err
		}
		if err := s.Access.Require(ctx, tx, p, access.Channel{ID: ch.ID}, access.Deliver); err != nil {
			return err
		}
		var acked int64
		if from == "oldest" {
			acked = max(ch.EvictedBeforeSeq-1, 0)
		} else if acked, err = tx.MaxSeq(ctx); err != nil {
			return err
		}
		return tx.Subscribe(ctx, identity, ch.ID, acked, s.NowMs())
	})
}

// Unsubscribe removes a subscription; it needs no permission (Decision 95),
// so a revoked reader can always leave. An unknown channel is not_found; not
// being subscribed is a no-op.
func (s Service) Unsubscribe(ctx context.Context, identity string, in bus.UnsubscribeInput) error {
	payload := struct{ Op, Channel string }{"unsubscribe", in.Channel}
	var res struct{}
	return s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx, p access.Principal) error {
		ch, err := s.ChannelByName(ctx, tx, in.Channel)
		if err != nil {
			return err
		}
		// No permission is needed (Decision 95), but a channel outside the
		// principal's scope does not exist for it: Unsubscribe is the scope gate
		// alone (the Unsubscribe action).
		if err := s.Access.Require(ctx, tx, p, access.Channel{ID: ch.ID}, access.Unsubscribe); err != nil {
			return err
		}
		return tx.Unsubscribe(ctx, identity, ch.ID)
	})
}

// normalizeSet validates one AND set: one to MaxTags patterns.
func normalizeSet(patterns []string) ([]string, error) {
	ps, err := rules.NormalizeTagPatterns(patterns)
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, core.E(bus.CodeValidation, "a tag subscription needs at least one pattern")
	}
	return ps, nil
}

// SubscribeTags adds one AND set of tag patterns, effective from the current
// high-water mark; adding the same set again is a no-op.
func (s Service) SubscribeTags(ctx context.Context, identity string, in bus.TagSubscribeInput) error {
	ps, err := normalizeSet(in.Patterns)
	if err != nil {
		return err
	}
	payload := struct {
		Op       string
		Patterns []string
	}{"subscribe_tags", ps}
	var res struct{}
	return s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx, p access.Principal) error {
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		tps := make([]store.TagPattern, len(ps))
		for i, p := range ps {
			tps[i] = store.NewTagPattern(p)
		}
		return tx.SubscribeTags(ctx, store.TagSubscription{
			Identity: identity, TagsKey: rules.TagsKey(ps), Patterns: tps, CreatedSeq: seq,
		})
	})
}

// UnsubscribeTags removes one set; removing an absent set is a no-op.
func (s Service) UnsubscribeTags(ctx context.Context, identity string, in bus.TagSubscribeInput) error {
	ps, err := normalizeSet(in.Patterns)
	if err != nil {
		return err
	}
	payload := struct {
		Op       string
		Patterns []string
	}{"unsubscribe_tags", ps}
	var res struct{}
	return s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx, p access.Principal) error {
		return tx.UnsubscribeTags(ctx, identity, rules.TagsKey(ps))
	})
}

// TagSubscriptions lists the identity's sets in order of their key.
func (s Service) TagSubscriptions(ctx context.Context, identity string) ([][]string, error) {
	out := [][]string{}
	err := s.read(ctx, identity, func(ctx context.Context, tx store.Tx, p access.Principal) error {
		out = out[:0]
		subs, err := tx.TagSubscriptions(ctx, identity)
		if err != nil {
			return err
		}
		for _, sub := range subs {
			set := make([]string, len(sub.Patterns))
			for i, p := range sub.Patterns {
				set[i] = p.Pattern
			}
			out = append(out, set)
		}
		return nil
	})
	return out, err
}
