package messaging

import (
	"context"
	"slices"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// History reads a window of a channel (v1 internal/bus/messages.go History).
// With Before and no After it is the newest-first window below Before,
// returned ascending; otherwise the range (After, Before) ascending from
// After. The result is trimmed in fetch order, so the rows nearest the
// caller's cursor survive.
func (s Service) History(ctx context.Context, identity string, in bus.HistoryInput) ([]bus.Message, error) {
	var out []bus.Message
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out = nil
		if _, err := s.RequireIdentity(ctx, tx, identity); err != nil {
			return err
		}
		ch, err := s.ChannelByName(ctx, tx, in.Channel)
		if err != nil {
			return err
		}
		if err := s.Authorize(ctx, tx, identity, ch.ID, store.PermRead); err != nil {
			return err
		}
		// The inputs are checked after registration, the channel and the read
		// permission, so a caller who may not read gets the auth code (v1 ran
		// auth first).
		count := in.Count
		if count == 0 {
			count = s.Cfg.ReceiveDefaultCount
		}
		if count < 1 {
			return core.E(bus.CodeValidation, "count must be at least 1")
		}
		count = min(count, s.Cfg.ReceiveMaxCount)
		norm, err := rules.NormalizeTagPatterns(in.Tags)
		if err != nil {
			return err // a rule's *bus.Error, already validation
		}
		patterns := make([]store.TagPattern, len(norm))
		for i, p := range norm {
			patterns[i] = store.NewTagPattern(p)
		}
		newestFirst := in.Before != nil && in.After == nil
		var rows []store.Message
		if newestFirst {
			rows, err = tx.MessagesBefore(ctx, ch.ID, *in.Before, patterns, store.MessageFilter{}, count)
		} else {
			from, to := int64(1), int64(0)
			if in.After != nil {
				from = *in.After + 1
			}
			if in.Before != nil {
				to = *in.Before - 1
			} else if to, err = tx.MaxSeq(ctx); err != nil {
				return core.FromStore(err)
			}
			rows, err = tx.MessagesInRange(ctx, ch.ID, from, to, patterns, store.MessageFilter{}, count)
		}
		if err != nil {
			return core.FromStore(err)
		}
		msgs, err := s.ToBusMessages(ctx, tx, rows)
		if err != nil {
			return err
		}
		msgs = rules.TrimToBytes(msgs, rules.EnvelopeBytes, s.Cfg.ResultDefaultKiB*1024)
		if newestFirst {
			slices.Reverse(msgs)
		}
		out = msgs
		return nil
	})
	if err != nil {
		return nil, core.FromStore(err)
	}
	return out, nil
}
