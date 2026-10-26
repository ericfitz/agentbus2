package delivery

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// tagSource keys the tag source in the per-source maps; channel ids are
// positive, so it cannot collide.
const tagSource int64 = -1

// view is the delivery state of one session: its cursors, the readable
// subscribed channels (direct sources) and the tag source.
type view struct {
	// cursors holds one cursor per subscription that has one (or, when not
	// syncing, a synthesized one at the acked mark); unreadable included, so
	// an ack settles them.
	cursors    map[int64]*store.Cursor
	subscribed map[int64]bool
	// direct is the readable subscribed channel ids, ascending (Decision 80:
	// no read, no delivery).
	direct []int64
	// tagSets and tagChannels describe the tag source: sets of the identity
	// and the readable channels that are not directly subscribed (Decision
	// 95).
	tagSets     []store.TagSubscription
	tagChannels []int64
	// tagCursor and tagPending are the session's tags cursor and pending end.
	tagCursor, tagPending int64
}

func (v *view) tagsUsable() bool { return len(v.tagSets) > 0 && len(v.tagChannels) > 0 }

// load builds the view. With sync it also repairs the cursor rows: a
// subscription without a cursor gets one at its acked mark (readable ones
// only) and a cursor without a subscription is deleted. A source whose
// channel the identity cannot read is left untouched.
func (s Service) load(ctx context.Context, tx store.Tx, p access.Principal, sess store.Session, sync bool) (*view, error) {
	identity := p.Name
	subs, err := tx.Subscriptions(ctx, identity)
	if err != nil {
		return nil, core.FromStore(err)
	}
	cs, err := tx.Cursors(ctx, sess.ID)
	if err != nil {
		return nil, core.FromStore(err)
	}
	readableIDs, err := s.Access.Channels(ctx, tx, p, access.Deliver)
	if err != nil {
		return nil, err
	}
	readable := make(map[int64]bool, len(readableIDs))
	for _, id := range readableIDs {
		readable[id] = true
	}
	v := &view{cursors: map[int64]*store.Cursor{}, subscribed: make(map[int64]bool, len(subs)), tagCursor: sess.TagsCursorSeq, tagPending: sess.TagsPendingEndSeq}
	for _, sub := range subs {
		v.subscribed[sub.ChannelID] = true
	}
	have := map[int64]*store.Cursor{}
	for _, c := range cs {
		if !v.subscribed[c.ChannelID] {
			if sync {
				if err := tx.DeleteCursor(ctx, sess.ID, c.ChannelID); err != nil {
					return nil, core.FromStore(err)
				}
			}
			continue
		}
		c := c
		have[c.ChannelID] = &c
	}
	for _, sub := range subs {
		c := have[sub.ChannelID]
		if !readable[sub.ChannelID] {
			// Decision 80: no read, no delivery
			if c != nil {
				v.cursors[sub.ChannelID] = c
			}
			continue
		}
		if c == nil {
			c = &store.Cursor{SessionID: sess.ID, ChannelID: sub.ChannelID, CursorSeq: sub.AckedSeq}
			if sync {
				if err := tx.PutCursor(ctx, *c); err != nil {
					return nil, core.FromStore(err)
				}
			}
		}
		v.cursors[sub.ChannelID] = c
		v.direct = append(v.direct, sub.ChannelID)
	}
	if v.tagSets, err = tx.TagSubscriptions(ctx, identity); err != nil {
		return nil, core.FromStore(err)
	}
	if len(v.tagSets) > 0 {
		for _, id := range readableIDs {
			if !v.subscribed[id] {
				v.tagChannels = append(v.tagChannels, id)
			}
		}
	}
	return v, nil
}

// Receive delivers the next batch for a session (Decisions 16, 40, 75, 95),
// ported from v1 internal/bus/receive.go receiveOnce. The batch token is
// minted before the transaction and only written by it, so a failed receive
// minted nothing durable; two concurrent receives on one session serialize on
// the transaction and the second sees the first's pending token.
func (s Service) Receive(ctx context.Context, identity string, in bus.ReceiveInput) (bus.ReceiveResult, error) {
	count := in.Count
	if count < 0 {
		return bus.ReceiveResult{}, core.E(bus.CodeValidation, "count must not be negative")
	}
	if count == 0 {
		count = s.Cfg.ReceiveDefaultCount
	}
	count = min(count, s.Cfg.ReceiveMaxCount)
	token, err := makeBatchToken(in.IncludeOwn)
	if err != nil {
		return bus.ReceiveResult{}, err
	}
	var res bus.ReceiveResult
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		res = bus.ReceiveResult{Messages: []bus.Message{}}
		return s.receive(ctx, tx, identity, in, count, token, &res)
	})
	if err != nil {
		return bus.ReceiveResult{}, core.FromStore(err)
	}
	return res, nil
}

func (s Service) receive(ctx context.Context, tx store.Tx, identity string, in bus.ReceiveInput, count int, newToken string, res *bus.ReceiveResult) error {
	p, err := access.Resolve(ctx, tx, identity)
	if err != nil {
		return err
	}
	sess, err := s.Sessions.RequireAs(ctx, tx, p, in.Session)
	if err != nil {
		return err
	}
	inFilter := map[int64]bool{}
	for _, name := range in.Channels {
		ch, err := s.ChannelByName(ctx, tx, name)
		if err != nil {
			return err
		}
		// A filter names a channel the caller may not even see as it names
		// one that never existed: List is the scope-only gate here.
		if err := s.Access.Require(ctx, tx, p, access.Channel{ID: ch.ID}, access.List); err != nil {
			return err
		}
		inFilter[ch.ID] = true
	}
	filtered := len(in.Channels) > 0

	v, err := s.load(ctx, tx, p, sess, true)
	if err != nil {
		return err
	}

	// Ack. Only a token equal to the pending one acks; a plain receive with
	// no Ack and no pending token must not take this branch, which would
	// clear stale pending ends and blur AckIgnored. Every cursor with a
	// pending end settles, unreadable ones included: the batch was delivered
	// while they were readable.
	matched := in.Ack != "" && in.Ack == sess.PendingToken
	if matched {
		ids := make([]int64, 0, len(v.cursors))
		for id := range v.cursors {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			c := v.cursors[id]
			if c.PendingEndSeq == 0 {
				continue
			}
			c.CursorSeq, c.PendingEndSeq = c.PendingEndSeq, 0
			if err := tx.PutCursor(ctx, *c); err != nil {
				return core.FromStore(err)
			}
			if err := tx.AdvanceAckedMark(ctx, identity, id, c.CursorSeq); err != nil {
				return core.FromStore(err)
			}
		}
		if v.tagPending > 0 {
			v.tagCursor, v.tagPending = v.tagPending, 0
			if err := tx.SetTagsCursor(ctx, sess.ID, v.tagCursor, 0); err != nil {
				return core.FromStore(err)
			}
			if err := tx.AdvanceTagAckedMark(ctx, identity, v.tagCursor); err != nil {
				return core.FromStore(err)
			}
		}
		if err := tx.SetPendingToken(ctx, sess.ID, ""); err != nil {
			return core.FromStore(err)
		}
		sess.PendingToken = ""
	}
	res.AckIgnored = in.Ack != "" && !matched

	// inScope: a source takes part in this call when no filter is given, the
	// filter names it, or it holds a pending batch (redelivered
	// unconditionally).
	inScope := func(id int64) bool { return !filtered || inFilter[id] || v.cursors[id].PendingEndSeq > 0 }

	// Gaps: a cursor below the retention boundary resumes at the oldest
	// survivor, and a pending end the move passed is dropped.
	gapsDeferred := false
	for _, id := range v.direct {
		if !inScope(id) {
			continue
		}
		ch, err := tx.Channel(ctx, id)
		if err != nil {
			return core.FromStore(err)
		}
		c := v.cursors[id]
		if c.CursorSeq >= ch.EvictedBeforeSeq-1 {
			continue
		}
		if len(res.Gaps) >= maxNoticesPerReceive {
			// The remaining gaps surface on a later call; no delivery may
			// happen until they are reported, or a later ack could advance
			// past a gap unreported.
			gapsDeferred = true
			continue
		}
		res.Gaps = append(res.Gaps, bus.Gap{Channel: ch.Name, From: c.CursorSeq + 1, To: ch.EvictedBeforeSeq - 1})
		c.CursorSeq = ch.EvictedBeforeSeq - 1
		if c.PendingEndSeq != 0 && c.PendingEndSeq <= c.CursorSeq {
			c.PendingEndSeq = 0
		}
		if err := tx.PutCursor(ctx, *c); err != nil {
			return core.FromStore(err)
		}
	}

	if n, ok, err := tx.Notice(ctx, "capacity"); err != nil {
		return core.FromStore(err)
	} else if ok {
		res.Notice = n.Message
	}

	// A token with nothing readable pending behind it is stale (a gap passed
	// its ends, the tag sets went away): clear it and start fresh.
	pendingDirect := make([]int64, 0, len(v.direct))
	for _, id := range v.direct {
		if v.cursors[id].PendingEndSeq > 0 {
			pendingDirect = append(pendingDirect, id)
		}
	}
	tagsPending := v.tagPending > 0 && v.tagsUsable()
	if sess.PendingToken != "" && len(pendingDirect) == 0 && !tagsPending {
		if err := tx.SetPendingToken(ctx, sess.ID, ""); err != nil {
			return core.FromStore(err)
		}
		if v.tagPending > 0 {
			v.tagPending = 0
			if err := tx.SetTagsCursor(ctx, sess.ID, v.tagCursor, 0); err != nil {
				return core.FromStore(err)
			}
		}
		sess.PendingToken = ""
	}

	sets := tagSets(v.tagSets)
	// attribute converts messages and sets MatchedTags on the ones that
	// arrived through the tag source (a directly subscribed channel's
	// message is never attributed to it).
	attribute := func(ms []store.Message) ([]bus.Message, error) {
		out, err := s.ToBusMessages(ctx, tx, ms)
		if err != nil {
			return nil, err
		}
		for i, m := range ms {
			if !v.subscribed[m.ChannelID] {
				out[i].MatchedTags = rules.MatchedTags(sets, m.Seq, m.Tags)
			}
		}
		return out, nil
	}
	srcOf := func(m store.Message) int64 {
		if v.subscribed[m.ChannelID] {
			return m.ChannelID
		}
		return tagSource
	}
	// reserve is the exact serialized size of the result with the messages
	// cleared, so selection trims against limit minus it.
	reserve := func(batch, instruction string, redelivered bool) int {
		r := *res
		r.Messages, r.Batch, r.Instruction, r.Redelivered = nil, batch, instruction, redelivered
		return rules.MarshalLen(r)
	}
	// putEnds records the pending end per source and the batch token.
	putEnds := func(ends map[int64]int64) error {
		for src, end := range ends {
			if src == tagSource {
				if err := tx.SetTagsCursor(ctx, sess.ID, v.tagCursor, end); err != nil {
					return core.FromStore(err)
				}
				continue
			}
			c := v.cursors[src]
			c.PendingEndSeq = end
			if err := tx.PutCursor(ctx, *c); err != nil {
				return core.FromStore(err)
			}
		}
		return nil
	}

	switch {
	case gapsDeferred:
		res.Instruction = "Some gap notices exceeded this call's limit; call receive again to see the rest before message delivery resumes."
	case sess.PendingToken != "":
		batch := sess.PendingToken
		instruction := fmt.Sprintf("This batch was delivered before and not acknowledged. Pass ack=%q on your next receive to advance past it.", batch)
		limit := redeliveryHardCeilingBytes - reserve(batch, instruction, true)
		if limit <= 0 {
			// The reserve alone exceeds the ceiling: metadata only, the
			// pending batch untouched for a later call.
			break
		}
		// Replay the include_own the batch was created with, not this
		// call's: the caller cannot change what a batch already means.
		f := ownFilter(sess.ID, tokenIncludesOwn(batch))
		var ms []store.Message
		for _, id := range pendingDirect {
			c := v.cursors[id]
			got, err := tx.MessagesInRange(ctx, id, c.CursorSeq+1, c.PendingEndSeq, nil, f, s.Cfg.ReceiveMaxCount)
			if err != nil {
				return core.FromStore(err)
			}
			ms = append(ms, got...)
		}
		if tagsPending {
			got, err := tx.TagMatchesAfter(ctx, identity, v.tagCursor, v.tagPending, v.tagChannels, f, s.Cfg.ReceiveMaxCount)
			if err != nil {
				return core.FromStore(err)
			}
			ms = append(ms, got...)
		}
		slices.SortFunc(ms, func(a, b store.Message) int { return int(a.Seq - b.Seq) })
		bm, err := attribute(ms)
		if err != nil {
			return err
		}
		bm = rules.TrimToBytes(bm, rules.EnvelopeBytes, limit)
		res.Messages, res.Batch, res.Redelivered, res.Instruction = bm, batch, true, instruction
		// Never let an ack of this token skip anything not shown here:
		// re-stamp each source's pending end to the highest seq actually
		// redelivered (its cursor when none), whether the byte ceiling cut
		// the tail or a message was tombstoned since.
		shown := map[int64]int64{}
		for _, m := range ms[:len(bm)] {
			shown[srcOf(m)] = m.Seq
		}
		ends := map[int64]int64{}
		for _, id := range pendingDirect {
			end := shown[id]
			if end == 0 {
				end = v.cursors[id].CursorSeq
			}
			if end != v.cursors[id].PendingEndSeq {
				ends[id] = end
			}
		}
		if tagsPending {
			end := shown[tagSource]
			if end == 0 {
				end = v.tagCursor
			}
			if end != v.tagPending {
				ends[tagSource] = end
			}
		}
		if err := putEnds(ends); err != nil {
			return err
		}
	default:
		placeholder := strings.Repeat("x", maxBatchTokenLen)
		instruction := fmt.Sprintf("Pass ack=%q on your next receive to acknowledge this batch.", placeholder)
		limit := min(s.Cfg.ResultDefaultKiB*1024, rules.TrimHardCeilingBytes) - reserve(placeholder, instruction, false)
		if limit <= 0 {
			break
		}
		f := ownFilter(sess.ID, in.IncludeOwn)
		var ms []store.Message
		for _, id := range v.direct {
			if filtered && !inFilter[id] {
				continue
			}
			got, err := tx.MessagesAfter(ctx, id, v.cursors[id].CursorSeq, f, count)
			if err != nil {
				return core.FromStore(err)
			}
			ms = append(ms, got...)
		}
		// The tag cursor is shared by every tag-matched channel, so the tag
		// source takes part only in an unfiltered receive.
		if !filtered && v.tagsUsable() {
			got, err := tx.TagMatchesAfter(ctx, identity, v.tagCursor, 0, v.tagChannels, f, count)
			if err != nil {
				return core.FromStore(err)
			}
			ms = append(ms, got...)
		}
		slices.SortFunc(ms, func(a, b store.Message) int { return int(a.Seq - b.Seq) })
		if len(ms) > count {
			ms = ms[:count]
		}
		bm, err := attribute(ms)
		if err != nil {
			return err
		}
		bm = rules.TrimToBytes(bm, rules.EnvelopeBytes, limit)
		if len(bm) == 0 {
			break
		}
		ms = ms[:len(bm)]
		ends := map[int64]int64{}
		for _, m := range ms {
			ends[srcOf(m)] = m.Seq
		}
		if err := putEnds(ends); err != nil {
			return err
		}
		if err := tx.SetPendingToken(ctx, sess.ID, newToken); err != nil {
			return core.FromStore(err)
		}
		res.Messages, res.Batch = bm, newToken
		res.Instruction = fmt.Sprintf("Pass ack=%q on your next receive to acknowledge this batch.", newToken)
	}

	// By construction this cannot fire: every branch trims against the
	// ceiling minus the exact reserve. Fail loudly if a future change breaks
	// that.
	if n := rules.MarshalLen(*res); n > rules.TrimHardCeilingBytes {
		return bus.Internal(fmt.Errorf("serialized receive result of %d bytes exceeds the %d byte hard ceiling", n, rules.TrimHardCeilingBytes))
	}
	return nil
}
