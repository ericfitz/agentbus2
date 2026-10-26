package delivery

import (
	"context"
	"slices"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/store"
)

// wake is what a peek found.
type wake struct {
	seq      int64
	count    int
	channels map[int64]bool
}

func (w *wake) add(ms []store.Message) {
	for _, m := range ms {
		w.seq = max(w.seq, m.Seq)
		w.count++
		if w.channels == nil {
			w.channels = map[int64]bool{}
		}
		w.channels[m.ChannelID] = true
	}
}

// peek is the read-only wake check (v1 internal/bus/wait.go peek): for every
// readable source, the live messages above max(cursor, pending end, after).
// A pending batch never wakes (v1 ADR 0012). It changes nothing.
func (s Service) peek(ctx context.Context, tx store.Tx, p access.Principal, sess store.Session, after int64, includeOwn bool) (wake, error) {
	v, err := s.load(ctx, tx, p, sess, false)
	if err != nil {
		return wake{}, err
	}
	f := ownFilter(sess.ID, includeOwn)
	var w wake
	for _, id := range v.direct {
		c := v.cursors[id]
		floor := max(c.CursorSeq, c.PendingEndSeq, after)
		ms, err := tx.MessagesAfter(ctx, id, floor, f, s.Cfg.ReceiveMaxCount)
		if err != nil {
			return wake{}, core.FromStore(err)
		}
		w.add(ms)
	}
	if v.tagsUsable() {
		tagFloor := max(v.tagCursor, v.tagPending, after)
		ms, err := tx.TagMatchesAfter(ctx, p.Name, tagFloor, 0, v.tagChannels, f, s.Cfg.ReceiveMaxCount)
		if err != nil {
			return wake{}, core.FromStore(err)
		}
		w.add(ms)
	}
	return w, nil
}

// channelNames resolves a wake's channel ids to sorted, unique names.
func channelNames(ctx context.Context, tx store.Tx, ids map[int64]bool) ([]string, error) {
	names := make([]string, 0, len(ids))
	for id := range ids {
		ch, err := tx.Channel(ctx, id)
		if err != nil {
			return nil, core.FromStore(err)
		}
		names = append(names, ch.Name)
	}
	slices.Sort(names)
	return names, nil
}
