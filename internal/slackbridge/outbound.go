package slackbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	// recordTimeout bounds the record step, recordBackoff is its pause
	// between attempts (times the attempt number).
	recordTimeout = 5 * time.Second
	recordBackoff = 50 * time.Millisecond
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
// process are serialized. Chat.postMessage has no idempotency key, so the map
// row is the only duplicate guard, and this process keeps two memories until a
// row is stored (both dropped when the bridge is archived or removed, and for
// every seq at or below the acked mark a drain reads, which another runner
// recorded; they are not dropped when the runner loses the lock, because
// record checks the map row before writing and another replica never sees
// them):
//
//   - posted: Slack answered with a ts. The next drain records that ts instead
//     of posting again.
//   - uncertain: the post failed in a way that may still have reached Slack
//     (a transport error, a timeout, a context end, an HTTP 5xx or 408, or a
//     server-side error code such as internal_error). The next drain first
//     searches Slack for a message of this app whose metadata seq is the
//     message's (History for a top-level post, Replies for a reply into a
//     thread, from 60 s before the attempt), records the ts it finds and posts
//     nothing; when it finds none it posts; when the search itself fails the
//     drain stops with that error and posts nothing blind. A failure that
//     proves Slack did not post (any other APIError, a 429 after the retries,
//     a channel gone) leaves no memory and posts again.
//
// What remains is a duplicate Slack post (never a bus message) when a post
// reached Slack and its record is lost with this process's memory (a crash, or
// a takeover by another replica) and CatchUp cannot repair it through the
// metadata, or when the uncertain search finds nothing because the post landed
// after the search or is older than its window (the bot reads its own metadata
// through History and Replies: verified on a real workspace, 2026-10-24).
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
	// Whatever the mark has passed another runner recorded: forget it.
	s.dropRecorded(b.ID, acked)
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
		s.clearPosted(postedKey{b.ID, m.Seq})
		s.clearUncertain(postedKey{b.ID, m.Seq})
		return false, s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			return tx.AdvanceAckedMark(ctx, store.SlackBridge, b.ChannelID, m.Seq)
		})
	}

	in := FormatPost(bus.Message{
		Seq: m.Seq, Channel: b.ChannelName, Sender: m.Sender, Type: m.Type, Subject: m.Subject, Content: m.Content, Tags: m.Tags,
	}, b.Scope, s.cfg.MaxMessageChars, quote)
	in.Channel, in.ThreadTS = b.SlackChannelID, thread
	key := postedKey{b.ID, m.Seq}
	ts, again := s.getPosted(key)
	if !again {
		if at, unsure := s.getUncertain(key); unsure {
			found, fts, err := s.findPost(ctx, b, m.Seq, thread, at)
			if err != nil {
				return true, fmt.Errorf("searching Slack for seq %d: %w", m.Seq, slackErr(err))
			}
			if found {
				s.log.Warn("slack bridge: an uncertain post had reached Slack; recording it", "channel", b.ChannelName, "slack_channel", b.SlackChannelID, "seq", m.Seq)
				ts, again = fts, true
			}
		}
	}
	if !again {
		attempt := s.core.Now()
		var err error
		ts, err = s.post(ctx, in)
		if errors.Is(err, slackapi.ErrChannelGone) {
			s.log.Warn("slack bridge: channel gone in Slack", "channel", b.ChannelName, "slack_channel", b.SlackChannelID)
			return true, s.ArchiveBridge(ctx, cur, reasonSlackGone)
		}
		if err != nil {
			if postUncertain(err) {
				s.setUncertain(key, attempt)
			}
			return true, fmt.Errorf("posting seq %d: %w", m.Seq, slackErr(err))
		}
	}
	root := thread
	if root == "" {
		root = ts
	}
	// The post happened: remember it until its map row is stored, so a failed
	// record (or a stop in the middle of it) is finished by the next drain
	// instead of posting the message again.
	s.setPosted(key, ts)
	s.clearUncertain(key)
	if err := s.record(ctx, b, m.Seq, ts, root); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.clearPosted(key)
			return true, nil // the bridge was removed after the post
		}
		return true, fmt.Errorf("recording seq %d: %w", m.Seq, err)
	}
	s.clearPosted(key)
	return false, nil
}

// postedKey names a bus message of a bridge.
type postedKey struct{ bridge, seq int64 }

func (s *Service) setPosted(k postedKey, ts string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retired[k.bridge] {
		return // archived or removed while the post was in flight
	}
	if s.posted == nil {
		s.posted = map[postedKey]string{}
	}
	s.posted[k] = ts
}

// getPosted reads the remembered Slack ts without removing it; the entry goes
// only through clearPosted (after the map row is stored), dropRecorded (the
// acked mark passed it) or clearBridgePosted (the bridge is archived or
// removed).
func (s *Service) getPosted(k postedKey) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, ok := s.posted[k]
	return ts, ok
}

// PendingPosts is how many posts this process remembers without a stored map
// row: posted ones waiting for their record and uncertain ones waiting for the
// search. Diagnostics and tests.
func (s *Service) PendingPosts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.posted) + len(s.uncertain)
}

func (s *Service) clearPosted(k postedKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.posted, k)
}

// setUncertain remembers that the post of k may have reached Slack, with the
// time of the attempt. An earlier attempt time is kept: the search window must
// cover every attempt.
func (s *Service) setUncertain(k postedKey, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retired[k.bridge] {
		return
	}
	if s.uncertain == nil {
		s.uncertain = map[postedKey]time.Time{}
	}
	if _, ok := s.uncertain[k]; !ok {
		s.uncertain[k] = at
	}
}

func (s *Service) getUncertain(k postedKey) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.uncertain[k]
	return at, ok
}

func (s *Service) clearUncertain(k postedKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.uncertain, k)
}

// clearBridgePosted drops every remembered post of one bridge (bridge 0 means
// all bridges): an archived or removed bridge posts nothing more, so its ID is
// also marked retired and no later setPosted or setUncertain remembers a post
// for it (bridge IDs are never reused). It is not
// called when the runner loses the lock: the memory is per process, another
// replica never sees it, and record checks the map row before writing.
func (s *Service) clearBridgePosted(bridge int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bridge == 0 {
		clear(s.posted)
		clear(s.uncertain)
		return
	}
	if s.retired == nil {
		s.retired = map[int64]bool{}
	}
	s.retired[bridge] = true // a drain still in flight must not remember a post afterwards
	for k := range s.posted {
		if k.bridge == bridge {
			delete(s.posted, k)
		}
	}
	for k := range s.uncertain {
		if k.bridge == bridge {
			delete(s.uncertain, k)
		}
	}
}

// dropRecorded forgets the remembered posts of a bridge whose seq the acked
// mark has reached: another runner recorded them (this process's own record
// clears its entry), so the memory only needs to cover seqs above the mark.
func (s *Service) dropRecorded(bridge, acked int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.posted {
		if k.bridge == bridge && k.seq <= acked {
			delete(s.posted, k)
		}
	}
	for k := range s.uncertain {
		if k.bridge == bridge && k.seq <= acked {
			delete(s.uncertain, k)
		}
	}
}

// uncertainWindow is how far before an uncertain attempt the search for its
// message starts, for clock skew between this host and Slack.
const uncertainWindow = 60 * time.Second

// postUncertain reports whether a failed post may still have reached Slack.
// Certain failures, where Slack answered that it did not post: an APIError
// (ok=false with a code) other than an HTTP 5xx or 408 (the adapter maps a bad
// status to http_<code>, and a gateway can return one after the post went
// through) and the server-side codes internal_error, fatal_error,
// service_unavailable and request_timeout; a RateLimitedError (a 429 is refused before processing); a
// channel gone. Everything else is uncertain: a transport error, a timeout, a
// canceled or expired context, an HTTP 5xx or 408.
func postUncertain(err error) bool {
	var rl *slackapi.RateLimitedError
	var ae *slackapi.APIError
	switch {
	case errors.Is(err, slackapi.ErrChannelGone), errors.As(err, &rl):
		return false
	case errors.As(err, &ae):
		switch ae.Code {
		case "http_408", "internal_error", "fatal_error", "service_unavailable", "request_timeout":
			return true
		}
		return strings.HasPrefix(ae.Code, "http_5")
	}
	return true
}

// findPost looks in Slack for a post of this app carrying seq in its metadata,
// from uncertainWindow before the attempt: the thread when the post was a
// reply, the channel history otherwise. A thread whose root is gone has no
// post to find.
func (s *Service) findPost(ctx context.Context, b store.Bridge, seq int64, thread string, attempt time.Time) (found bool, ts string, err error) {
	self, err := s.requireReady()
	if err != nil {
		return false, "", err
	}
	oldest := tsOf(attempt.Add(-uncertainWindow))
	var msgs []slackapi.Message
	if thread == "" {
		msgs, err = s.readAll(ctx, func(cursor string) (slackapi.Page, error) {
			return s.sc.History(ctx, b.SlackChannelID, oldest, cursor)
		})
	} else {
		msgs, err = s.readAll(ctx, func(cursor string) (slackapi.Page, error) {
			return s.sc.Replies(ctx, b.SlackChannelID, thread, oldest, cursor)
		})
		var ae *slackapi.APIError
		if errors.As(err, &ae) && ae.Code == "thread_not_found" {
			return false, "", nil
		}
	}
	if err != nil {
		return false, "", err
	}
	for _, m := range msgs {
		if m.MetadataSeq == seq && m.BotID != "" && m.BotID == self.BotID && slackapi.ValidTS(m.TS) {
			return true, m.TS, nil
		}
	}
	return false, "", nil
}

// record stores the map row of a posted message (unless an overlapping runner
// already did) and advances the acked mark, in one transaction. It runs on a
// context that outlives a stop or shutdown, bounded by recordTimeout, and
// backs off between attempts, because a post that is not recorded is posted
// again by the next drain of a runner that does not remember it.
func (s *Service) record(ctx context.Context, b store.Bridge, seq int64, ts, root string) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	var err error
	for attempt := 1; ; attempt++ {
		err = s.core.Store.Tx(rctx, func(ctx context.Context, tx store.Tx) error {
			_, err := tx.BridgeMessageBySeq(ctx, b.ID, seq)
			if errors.Is(err, store.ErrNotFound) {
				// An overlapping runner has not written it: write it. (Never
				// swallow a failed insert; on Postgres it ends the transaction.)
				if err := tx.PutBridgeMessage(ctx, store.BridgeMessage{BridgeID: b.ID, Seq: seq, SlackTS: ts, RootTS: root, CreatedAt: s.core.NowMs()}); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			return tx.AdvanceAckedMark(ctx, store.SlackBridge, b.ChannelID, seq)
		})
		if err == nil || errors.Is(err, store.ErrNotFound) || attempt >= writeAttempts {
			return err
		}
		if sleep(rctx, time.Duration(attempt)*recordBackoff) != nil {
			return err
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
