package slackbridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/store"
)

const hourMs = int64(time.Hour / time.Millisecond)

// markConnected starts a new connection generation: the membership check of
// every done scope runs again, because Socket Mode does not replay a missed
// member_left_channel.
func (s *Service) markConnected() {
	s.mu.Lock()
	s.connGen++
	s.mu.Unlock()
}

// needsCheck reports whether the bridge's membership has not been checked
// since the last connect.
func (s *Service) needsCheck(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	gen, ok := s.checked[id]
	return !ok || gen != s.connGen
}

func (s *Service) generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connGen
}

// markChecked stamps the generation read before the Members call, so a
// reconnect during the check leaves the bridge due for another.
func (s *Service) markChecked(id int64, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checked == nil {
		s.checked = map[int64]uint64{}
	}
	s.checked[id] = gen
}

// Lifecycle applies the archive rules of spec 6.5 and 6.1b to every live
// bridge: a bus channel that is gone removes the bridge; a done scope past
// DoneAt + ArchiveAfterDone, an idle scope (no traffic for ScopeIdleHours)
// and a done scope with no human left in the Slack channel archive it. The
// membership check of a done scope runs once per connection generation.
// Scopes that are retired are left to maintenance.
//
// State (Definition of Done D): every rule is derived from stored state and
// ArchiveBridge is idempotent, so a second run, an overlapping run (another
// replica during a takeover, or maintenance) and the leftovers of a failed
// run are harmless; a failure on one bridge does not stop the others and the
// errors are joined.
func (s *Service) Lifecycle(ctx context.Context) error {
	if _, err := s.requireReady(); err != nil {
		return err
	}
	var bridges []store.Bridge
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		bridges, err = tx.ListBridges(ctx)
		return err
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range bridges {
		if b.State != store.BridgeLive || b.SlackChannelID == "" || ctx.Err() != nil {
			continue
		}
		if err := s.lifecycleOne(ctx, b); err != nil && ctx.Err() == nil {
			errs = append(errs, fmt.Errorf("lifecycle of channel %q: %w", b.ChannelName, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) lifecycleOne(ctx context.Context, b store.Bridge) error {
	var sc store.Scope
	chGone, scGone := false, false
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.Channel(ctx, b.ChannelID); errors.Is(err, store.ErrNotFound) {
			chGone = true
			return nil
		} else if err != nil {
			return err
		}
		var err error
		sc, err = tx.Scope(ctx, b.Scope)
		if errors.Is(err, store.ErrNotFound) {
			scGone = true
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	if chGone {
		if err := s.ArchiveBridge(ctx, b, scopes.ReasonChannelGone); err != nil {
			return err
		}
		err := s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error { return tx.DeleteBridge(ctx, b.ID) })
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return nil
	}
	if scGone || sc.State == store.ScopeRetired {
		return nil
	}
	gen := s.generation()
	now := s.core.NowMs()
	switch {
	case sc.State == store.ScopeDone && now >= sc.DoneAt+s.cfg.ArchiveAfterDone.Milliseconds():
		return s.ArchiveBridge(ctx, b, scopes.ReasonDone)
	case sc.LastActivity < now-int64(s.core.Cfg.ScopeIdleHours)*hourMs:
		return s.ArchiveBridge(ctx, b, scopes.ReasonIdle)
	case sc.State == store.ScopeDone && s.needsCheck(b.ID):
		return s.checkMembers(ctx, b, gen)
	}
	return nil
}

// checkMembers archives the bridge when no human remains in its Slack
// channel: the members minus the app and minus bots and deleted users. A
// failed check is not marked, so the next pass repeats it.
func (s *Service) checkMembers(ctx context.Context, b store.Bridge, gen uint64) error {
	self, err := s.requireReady()
	if err != nil {
		return err
	}
	ids, err := s.sc.Members(ctx, b.SlackChannelID)
	if errors.Is(err, slackapi.ErrChannelGone) {
		return s.ArchiveBridge(ctx, b, reasonSlackGone)
	}
	if err != nil {
		return slackErr(err)
	}
	for _, id := range ids {
		if id == self.UserID {
			continue
		}
		u, err := s.user(ctx, id)
		if errors.Is(err, slackapi.ErrUserNotFound) {
			continue
		}
		if err != nil {
			return slackErr(err)
		}
		if u.IsBot || u.Deleted {
			continue
		}
		s.markChecked(b.ID, gen)
		return nil
	}
	return s.ArchiveBridge(ctx, b, scopes.ReasonNoHumans)
}

// memberLeft handles a member_left_channel event: when the channel's bridge
// is live and its scope is done, the membership check runs at once.
func (s *Service) memberLeft(ctx context.Context, slackChannel string) error {
	if _, err := s.requireReady(); err != nil {
		return err
	}
	var b store.Bridge
	done := false
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if b, err = tx.BridgeBySlackChannel(ctx, slackChannel); err != nil {
			return err
		}
		sc, err := tx.Scope(ctx, b.Scope)
		if err != nil {
			return err
		}
		done = sc.State == store.ScopeDone
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.State != store.BridgeLive || !done {
		return nil
	}
	return s.checkMembers(ctx, b, s.generation())
}
