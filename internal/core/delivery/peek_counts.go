package delivery

import (
	"cmp"
	"context"
	"slices"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Peek is the read-only pending count (Decision (Eric, 2026-10-21),
// Milestone 6 item 2): per subscribed channel and tag source, the live
// messages above the watch floor max(CursorSeq, PendingEndSeq), so a batch
// the session already holds is not counted. It writes nothing: no lease
// renewal, no cursor, no watch row, no receipt (Decision 26 is trivially
// met). A message reachable both directly and by tag counts once, on its
// channel. Each source is capped at ReceiveMaxCount, as the watch's is.
func (s Service) Peek(ctx context.Context, identity string, in bus.PeekInput) (bus.PeekResult, error) {
	var out bus.PeekResult
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out = bus.PeekResult{Channels: []bus.PeekChannel{}}
		p, err := access.Resolve(ctx, tx, identity)
		if err != nil {
			return err
		}
		// not_found for another identity's session, as for Receive and Watch,
		// so it is not revealed to exist.
		sess, err := s.Sessions.Owned(ctx, tx, p, in.Session)
		if err != nil {
			return err
		}
		switch {
		case sess.ClosedAt != 0:
			return core.E(bus.CodeSessionClosed, "session %q is closed", in.Session)
		case !s.IsLive(sess, s.NowMs()):
			return core.E(bus.CodeSessionLapsed, "session %q lapsed; open with its resume token to resume it", in.Session)
		}
		byChannel, err := s.peekCounts(ctx, tx, p, sess, in.IncludeOwn)
		if err != nil {
			return err
		}
		for id, n := range byChannel {
			ch, err := tx.Channel(ctx, id)
			if err != nil {
				return core.FromStore(err)
			}
			out.Channels = append(out.Channels, bus.PeekChannel{Channel: ch.Name, Count: n})
			out.Count += n
		}
		slices.SortFunc(out.Channels, func(a, b bus.PeekChannel) int { return cmp.Compare(a.Channel, b.Channel) })
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return bus.PeekResult{}, ctx.Err()
		}
		return bus.PeekResult{}, core.FromStore(err)
	}
	return out, nil
}

// peekCounts counts like peek (the same view, floors and own filter) but per
// channel id, once per message even when two sources reach it.
func (s Service) peekCounts(ctx context.Context, tx store.Tx, p access.Principal, sess store.Session, includeOwn bool) (map[int64]int, error) {
	v, err := s.load(ctx, tx, p, sess, false)
	if err != nil {
		return nil, err
	}
	f := ownFilter(sess.ID, includeOwn)
	out := map[int64]int{}
	seen := map[int64]bool{}
	add := func(ms []store.Message) {
		for _, m := range ms {
			if !seen[m.Seq] {
				seen[m.Seq] = true
				out[m.ChannelID]++
			}
		}
	}
	for _, id := range v.direct {
		c := v.cursors[id]
		ms, err := tx.MessagesAfter(ctx, id, max(c.CursorSeq, c.PendingEndSeq), f, s.Cfg.ReceiveMaxCount)
		if err != nil {
			return nil, core.FromStore(err)
		}
		add(ms)
	}
	if v.tagsUsable() {
		ms, err := tx.TagMatchesAfter(ctx, p.Name, max(v.tagCursor, v.tagPending), 0, v.tagChannels, f, s.Cfg.ReceiveMaxCount)
		if err != nil {
			return nil, core.FromStore(err)
		}
		add(ms)
	}
	return out, nil
}
