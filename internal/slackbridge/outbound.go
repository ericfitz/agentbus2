package slackbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/store"
)

const (
	// drainBatch is how many messages one read of a bridged channel takes.
	drainBatch = 100
	// rateLimitAttempts bounds the 429 answers one message waits through; the
	// bridge then stops until the next drain. rateLimitWaitMax caps one wait.
	rateLimitAttempts = 10
	rateLimitWaitMax  = time.Minute
	// writeAttempts is how often the map-and-mark transaction after a
	// successful post is tried before the bridge stops until the next drain.
	writeAttempts = 3
	// reasonSlackGone is the logged reason of a bridge archived by hand.
	reasonSlackGone = "archived in Slack"
)

// pacer spaces the posts to one Slack channel by Config.PostInterval (wall
// clock; the Core clock is the tests' to steer).
type pacer struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// wait blocks until a post to channel is allowed, or ctx ends, and records
// the post time.
func (p *pacer) wait(ctx context.Context, channel string, every time.Duration) error {
	p.mu.Lock()
	var d time.Duration
	if t, ok := p.last[channel]; ok {
		d = every - time.Since(t)
	}
	if p.last == nil {
		p.last = map[string]time.Time{}
	}
	p.last[channel] = time.Now().Add(max(d, 0)) // reserved: a concurrent caller waits behind it
	p.mu.Unlock()
	return sleep(ctx, d)
}

// sleep waits d (not at all when d <= 0) or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Drain posts the bus messages of every live bridge that the slack-bridge
// subscription has not acked, in seq order (spec 6.2, 5.3).
//
// A message sent by slack-bridge, a memory-kind message and one with a map
// row (the bridge sent it for a human, Decision 2) only advance the mark.
// Any other message is posted (a reply into the thread of its parent's root
// when the parent is mapped, otherwise top-level with the parent quoted when
// it still exists), then one transaction writes the map row (unless an
// overlapping runner already did) and advances the mark. A 429 waits
// Retry-After and retries the same message; a channel gone in Slack marks the
// bridge archived; any other error stops that bridge until the next drain
// without advancing its mark.
//
// State (Definition of Done D): the mark moves only after a successful post,
// so a second Drain posts nothing already acked; overlapping Drains in one
// process are serialized; after a failed run the next one resumes from the
// mark and posts the unacked message again at worst (a crash between the
// post and its map row is repaired by CatchUp through the message metadata,
// which the runner runs before the first drain).
func (s *Service) Drain(ctx context.Context) error {
	if _, err := s.requireReady(); err != nil {
		return err
	}
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	var bridges []store.Bridge
	marks := map[int64]int64{}
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if bridges, err = tx.ListBridges(ctx); err != nil {
			return err
		}
		subs, err := tx.Subscriptions(ctx, store.SlackBridge)
		if err != nil {
			return err
		}
		clear(marks)
		for _, sub := range subs {
			marks[sub.ChannelID] = sub.AckedSeq
		}
		return nil
	})
	if err != nil {
		return err
	}
	bridged := map[int64]bool{}
	var errs []error
	for _, b := range bridges {
		if b.State != store.BridgeLive || b.SlackChannelID == "" {
			continue
		}
		bridged[b.ChannelID] = true
		acked, ok := marks[b.ChannelID]
		if !ok || ctx.Err() != nil {
			continue
		}
		if err := s.drainBridge(ctx, b, acked); err != nil && ctx.Err() == nil {
			errs = append(errs, fmt.Errorf("drain of channel %q: %w", b.ChannelName, err))
		}
	}
	s.setBridged(bridged)
	return errors.Join(errs...)
}

func (s *Service) drainBridge(ctx context.Context, b store.Bridge, acked int64) error {
	for {
		var msgs []store.Message
		var ch store.Channel
		err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			if ch, err = tx.Channel(ctx, b.ChannelID); err != nil {
				return err
			}
			msgs, err = tx.MessagesAfter(ctx, b.ChannelID, acked, store.MessageFilter{}, drainBatch)
			return err
		})
		if errors.Is(err, store.ErrNotFound) {
			return nil // the bus channel is gone; the lifecycle removes the bridge
		}
		if err != nil {
			return err
		}
		for _, m := range msgs {
			stop, err := s.drainOne(ctx, b, ch, m)
			if err != nil || stop {
				return err
			}
			acked = m.Seq
		}
		if len(msgs) < drainBatch {
			return nil
		}
	}
}

// drainOne handles one message. stop is true when the bridge is no longer
// live (the rest of its batch is moot).
func (s *Service) drainOne(ctx context.Context, b store.Bridge, ch store.Channel, m store.Message) (stop bool, err error) {
	skip := m.Sender == store.SlackBridge || ch.Kind != store.KindOrdinary || m.MemoryID != nil
	var thread, quote string
	var cur store.Bridge
	err = s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if cur, err = tx.Bridge(ctx, b.ID); err != nil {
			return err
		}
		if skip {
			return nil
		}
		if _, err := tx.BridgeMessageBySeq(ctx, b.ID, m.Seq); err == nil {
			skip = true
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if m.ReplyTo == nil {
			return nil
		}
		row, err := tx.BridgeMessageBySeq(ctx, b.ID, *m.ReplyTo)
		if err == nil {
			thread = row.RootTS
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		p, err := tx.Message(ctx, *m.ReplyTo)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if p.ChannelID == b.ChannelID && !p.Tombstone {
			quote = p.Content
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) || (err == nil && cur.State != store.BridgeLive) {
		return true, nil // removed or archived meanwhile
	}
	if err != nil {
		return true, err
	}
	if skip {
		return false, s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			return tx.AdvanceAckedMark(ctx, store.SlackBridge, b.ChannelID, m.Seq)
		})
	}

	in := FormatPost(bus.Message{
		Seq: m.Seq, Channel: b.ChannelName, Sender: m.Sender, Type: m.Type, Subject: m.Subject, Content: m.Content, Tags: m.Tags,
	}, b.Scope, s.cfg.MaxMessageChars, quote)
	in.Channel, in.ThreadTS = b.SlackChannelID, thread
	ts, err := s.post(ctx, in)
	if errors.Is(err, slackapi.ErrChannelGone) {
		s.log.Warn("slack bridge: channel gone in Slack", "channel", b.ChannelName, "slack_channel", b.SlackChannelID)
		return true, s.ArchiveBridge(ctx, cur, reasonSlackGone)
	}
	if err != nil {
		return true, fmt.Errorf("posting seq %d: %w", m.Seq, slackErr(err))
	}
	root := thread
	if root == "" {
		root = ts
	}
	for attempt := 1; ; attempt++ {
		err = s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			_, err := tx.BridgeMessageBySeq(ctx, b.ID, m.Seq)
			if errors.Is(err, store.ErrNotFound) {
				// An overlapping runner has not written it: write it. (Never
				// swallow a failed insert; on Postgres it ends the transaction.)
				if err := tx.PutBridgeMessage(ctx, store.BridgeMessage{BridgeID: b.ID, Seq: m.Seq, SlackTS: ts, RootTS: root, CreatedAt: s.core.NowMs()}); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			return tx.AdvanceAckedMark(ctx, store.SlackBridge, b.ChannelID, m.Seq)
		})
		if err == nil {
			return false, nil
		}
		if errors.Is(err, store.ErrNotFound) {
			return true, nil // the bridge was removed after the post
		}
		if attempt >= writeAttempts || ctx.Err() != nil {
			return true, fmt.Errorf("recording seq %d: %w", m.Seq, err)
		}
	}
}

// post sends in at the pace of Config.PostInterval, waiting out a 429 and
// retrying the same message.
func (s *Service) post(ctx context.Context, in slackapi.PostInput) (string, error) {
	for attempt := 1; ; attempt++ {
		if err := s.pace.wait(ctx, in.Channel, s.cfg.PostInterval); err != nil {
			return "", err
		}
		ts, err := s.sc.Post(ctx, in)
		var rl *slackapi.RateLimitedError
		if !errors.As(err, &rl) || attempt >= rateLimitAttempts {
			return ts, err
		}
		if err := sleep(ctx, min(rl.RetryAfter, rateLimitWaitMax)); err != nil {
			return "", err
		}
	}
}

// setBridged records the bus channels with a live bridge, for the drain wake.
func (s *Service) setBridged(m map[int64]bool) {
	s.mu.Lock()
	s.bridged = m
	s.mu.Unlock()
}

// isBridged reports whether a hint for channelID concerns a bridge. Hints
// with no channel (a polling notifier's tick) concern every bridge.
func (s *Service) isBridged(channelID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return channelID == 0 || s.bridged[channelID]
}
