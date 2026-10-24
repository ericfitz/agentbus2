package slackbridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/store"
)

const (
	// catchUpRootWindow and catchUpMaxRoots bound the thread reads of one
	// pass: roots with a bridged message in the last week, at most 200 of
	// them, newest first (plan choice). A skipped root is logged.
	catchUpRootWindow = 7 * 24 * time.Hour
	catchUpMaxRoots   = 200
)

// CatchUp reads every live bridge's Slack history (and thread replies) since
// its cursor and handles each unseen message as if it were an event (spec
// 5.3). The cursor moves only here, when a bridge's pass completes, to the
// newest Slack ts it read, or to just below the oldest retry outcome; events
// never move it, so a message posted during an outage before a later bridge
// post, or delivered out of order, is not skipped.
//
// State (Definition of Done D): safe to call repeatedly and from overlapping
// callers. The ts map and the drop set make every message idempotent, so a
// second pass over the same window finds only duplicates and ignores, and a
// pass that failed halfway leaves the cursor where it was for the next pass.
// A failure on one bridge does not stop the others; the errors are joined.
func (s *Service) CatchUp(ctx context.Context) error {
	if _, err := s.requireReady(); err != nil {
		return err
	}
	var bridges []store.Bridge
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		all, err := tx.ListBridges(ctx)
		bridges = all
		return err
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range bridges {
		if b.State != store.BridgeLive || b.SlackChannelID == "" {
			continue
		}
		if err := s.catchUpBridge(ctx, b); err != nil {
			errs = append(errs, fmt.Errorf("catch-up of channel %q: %w", b.ChannelName, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) catchUpBridge(ctx context.Context, b store.Bridge) error {
	hist, err := s.readAll(ctx, func(cursor string) (slackapi.Page, error) {
		return s.sc.History(ctx, b.SlackChannelID, b.CursorTS, cursor)
	})
	if err != nil {
		return err
	}
	// Roots to read: those with replies in the history, then those the bridge
	// mapped this week; at most catchUpMaxRoots.
	var roots []string
	for _, m := range hist {
		if m.ReplyCount > 0 && slackapi.ValidTS(m.TS) && !slices.Contains(roots, m.TS) {
			roots = append(roots, m.TS)
		}
	}
	var mapped []string
	err = s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		mapped, err = tx.ThreadRoots(ctx, b.ID, tsOf(s.core.Now().Add(-catchUpRootWindow)), catchUpMaxRoots+1)
		return err
	})
	if err != nil {
		return err
	}
	for _, r := range mapped {
		if slackapi.ValidTS(r) && !slices.Contains(roots, r) {
			roots = append(roots, r)
		}
	}
	if len(roots) > catchUpMaxRoots {
		s.log.Warn("slack catch-up skipped thread roots", "slack_channel", b.SlackChannelID, "skipped", len(roots)-catchUpMaxRoots)
		roots = roots[:catchUpMaxRoots]
	}
	all := hist
	for _, root := range roots {
		replies, err := s.readAll(ctx, func(cursor string) (slackapi.Page, error) {
			return s.sc.Replies(ctx, b.SlackChannelID, root, b.CursorTS, cursor)
		})
		var ae *slackapi.APIError
		if errors.As(err, &ae) && ae.Code == "thread_not_found" {
			continue // the root was deleted in Slack
		}
		if err != nil {
			return err
		}
		all = append(all, replies...)
	}
	// Merge, dedupe by ts, ascending; skip what the cursor already passed
	// (a thread read returns its root whatever oldest says).
	slices.SortStableFunc(all, func(a, b slackapi.Message) int { return compareTS(a.TS, b.TS) })
	all = slices.CompactFunc(all, func(a, b slackapi.Message) bool { return a.TS == b.TS })
	// The cursor never passes the newest top-level ts the history read
	// returned: a top-level message posted after that read, before a later
	// thread read, must not be skipped (thread replies read later are
	// re-read next pass and are duplicates by then).
	var newest, oldestRetry string
	for _, m := range hist {
		if slackapi.ValidTS(m.TS) && m.TS > newest {
			newest = m.TS
		}
	}
	for _, m := range all {
		if !slackapi.ValidTS(m.TS) || m.TS <= b.CursorTS {
			continue
		}
		out, err := s.handleMessage(ctx, b.SlackChannelID, m, true)
		if out == OutcomeRetry {
			if oldestRetry == "" {
				oldestRetry = m.TS
			}
			s.log.Warn("slack catch-up: message will be retried", "slack_user", m.User, "slack_channel", b.SlackChannelID, "code", bus.CodeOf(err))
		}
	}
	target := newest
	if oldestRetry != "" {
		target = min(newest, prevTS(oldestRetry))
	}
	if target == "" || target <= b.CursorTS {
		return nil
	}
	err = s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AdvanceBridgeCursor(ctx, b.ID, target)
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil // removed meanwhile
	}
	return err
}

// readAll reads every page of a listing.
func (s *Service) readAll(ctx context.Context, page func(cursor string) (slackapi.Page, error)) ([]slackapi.Message, error) {
	var out []slackapi.Message
	cursor := ""
	for {
		p, err := page(cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Messages...)
		if p.NextCursor == "" {
			return out, nil
		}
		if p.NextCursor == cursor {
			return nil, errors.New("slack returned the same page cursor twice")
		}
		cursor = p.NextCursor
	}
}

// compareTS orders Slack ts values; an invalid one sorts first.
func compareTS(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// tsOf is the Slack ts of a time.
func tsOf(t time.Time) string {
	return fmt.Sprintf("%010d.%06d", t.Unix(), t.Nanosecond()/1000)
}

// prevTS is the ts one microsecond before a valid ts.
func prevTS(ts string) string {
	sec, _ := strconv.ParseInt(ts[:10], 10, 64)
	micro, _ := strconv.ParseInt(ts[11:], 10, 64)
	if micro == 0 {
		sec, micro = sec-1, 999999
	} else {
		micro--
	}
	return fmt.Sprintf("%010d.%06d", sec, micro)
}
