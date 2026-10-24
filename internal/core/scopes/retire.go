package scopes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

const hourMs = 3_600_000

// Why a bridge is archived by retirement (spec 6.5), passed to
// Archiver.ArchiveBridge as the reason of its final Slack line.
const (
	ReasonDone        = "done"
	ReasonNoHumans    = "no humans"
	ReasonIdle        = "idle"
	ReasonRemoved     = "removed"
	ReasonChannelGone = "channel gone"
)

// Archiver archives the Slack channel of a bridge: it posts the final line,
// archives the channel in Slack and marks the bridge row archived. It is
// idempotent: a bridge already archived is a no-op.
type Archiver interface {
	ArchiveBridge(ctx context.Context, b store.Bridge, reason string) error
}

// dueScope is a scope RetireDue found due, with the bridges still to archive
// and why.
type dueScope struct {
	name    string
	reason  string
	bridges []store.Bridge // creating or live
}

// RetireDue retires every open or done scope that is due at now (spec 4.3):
// (1) done with_bridge and none of its bridges is creating or live; (2) done
// normal and without a channel; (3) no message on any of its channels for
// ScopeIdleHours, whatever its state. For a due scope each bridge still
// creating or live goes to a first (reason idle for rule 3, channel gone for
// rule 2); an error leaves the scope for the next tick. With a == nil such a
// scope is held (nothing retired, no row deleted) and a warning names the
// scope and the Slack channel on every tick, so a Slack channel with humans
// in it is never orphaned. It returns how many scopes it retired.
//
// State (Definition of Done D): the due test reads only state that a
// partial run leaves unchanged (the idle clock, the absent live bridges, the
// absent channels), so a scope a failed run left is due again next tick and
// Retire finishes it; a bridge archived by a run that then failed is skipped
// by the next one, so Slack is called once; overlapping runs are serialized
// by the maintenance lock and Retire re-reads inside its transactions.
func (s Service) RetireDue(ctx context.Context, now int64, a Archiver) (int, error) {
	var due []dueScope
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		due = nil
		all, err := tx.ListScopes(ctx)
		if err != nil {
			return err
		}
		idleBefore := now - int64(s.Cfg.ScopeIdleHours)*hourMs
		for _, sc := range all {
			if sc.State == store.ScopeRetired {
				continue
			}
			bridges, err := tx.ScopeBridges(ctx, sc.Name)
			if err != nil {
				return err
			}
			var active []store.Bridge
			for _, b := range bridges {
				if b.State == store.BridgeCreating || b.State == store.BridgeLive {
					active = append(active, b)
				}
			}
			reason := ""
			switch {
			case sc.State == store.ScopeDone && sc.Retire == store.RetireWithBridge && len(active) == 0:
				reason = ReasonDone
			case sc.State == store.ScopeDone && sc.Retire == store.RetireNormal:
				chs, _, err := tx.ScopeMembers(ctx, sc.Name)
				if err != nil {
					return err
				}
				if len(chs) == 0 {
					reason = ReasonChannelGone
				}
			}
			if reason == "" && sc.LastActivity < idleBefore {
				reason = ReasonIdle
			}
			if reason != "" {
				due = append(due, dueScope{sc.Name, reason, active})
			}
		}
		return nil
	})
	if err != nil {
		return 0, core.FromStore(err)
	}

	retired := 0
	var errs []error
scopes:
	for _, d := range due {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if a == nil && len(d.bridges) > 0 {
			ids := make([]string, len(d.bridges))
			for i, b := range d.bridges {
				ids[i] = b.SlackChannelID
			}
			s.Log.Warn("scope retirement held: the Slack bridge is not configured and a bridge is still active",
				"scope", d.name, "bridges", len(ids), "slack_channels", strings.Join(ids, ","))
			continue
		}
		for _, b := range d.bridges {
			if err := a.ArchiveBridge(ctx, b, d.reason); err != nil {
				errs = append(errs, fmt.Errorf("scope %q: archive bridge of %q: %w", d.name, b.ChannelName, err))
				continue scopes
			}
		}
		ok, err := s.Retire(ctx, d.name, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("scope %q: %w", d.name, err))
			continue
		}
		if ok {
			retired++
		}
	}
	return retired, errors.Join(errs...)
}

// live reports whether the scope still exists and is not retired.
func live(ctx context.Context, tx store.Tx, name string) (bool, error) {
	sc, err := tx.Scope(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sc.State != store.ScopeRetired, nil
}

// Retire retires one scope (spec 4.3, Decision 19) as a sequence of short
// transactions, each re-reading the scope first: one deleting its bridges
// (map and drop rows cascade), one per channel of every kind (messages,
// subscriptions and ACLs go with it), one per scoped identity (RetireIdentity:
// keys, sessions, subscriptions, its ACL entries on every channel and its
// bound enrollment tokens, its name reserved for good; its messages and its
// task claims' rows stay, the claims are reclaimed by the abandoned-tasks job
// of the same tick), and last the retired mark. It reports false, nil when
// the scope is already retired or gone. scopes works on tx only: it imports
// neither core/channels nor core/identities.
//
// State (Definition of Done D): every step is idempotent and a step that did
// not run is found again by the next one, because the members are listed
// afresh; a run that stopped part way leaves the scope open or done with
// fewer members, and the next call finishes it. If a channel or identity
// joined while the run was in progress, the final step refuses (the scope
// stays) and the next call removes it too.
func (s Service) Retire(ctx context.Context, name string, now int64) (bool, error) {
	nBridges, nChannels, nIdentities := 0, 0, 0

	gone := false
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		gone, nBridges = false, 0
		ok, err := live(ctx, tx, name)
		if err != nil || !ok {
			gone = !ok
			return err
		}
		bridges, err := tx.ScopeBridges(ctx, name)
		if err != nil {
			return err
		}
		for _, b := range bridges {
			// RetireDue archived the active ones; one that went creating or
			// live since would lose its row while its Slack channel stays
			// open with humans in it. The scope stays; the next tick
			// archives it.
			if b.State == store.BridgeCreating || b.State == store.BridgeLive {
				return core.E(bus.CodeConflict, "scope %q has an active bridge on %q; the next run archives it", name, b.ChannelName)
			}
		}
		for _, b := range bridges {
			if err := tx.DeleteBridge(ctx, b.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		nBridges = len(bridges)
		return nil
	})
	if err != nil {
		return false, core.FromStore(err)
	}
	if gone {
		return false, nil
	}

	var chs []store.Channel
	var ids []store.Identity
	err = s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		chs, ids, err = tx.ScopeMembers(ctx, name)
		return err
	})
	if err != nil {
		return false, core.FromStore(err)
	}

	for _, ch := range chs {
		deleted := false
		err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			deleted, gone = false, false
			ok, err := live(ctx, tx, name)
			if err != nil || !ok {
				gone = !ok
				return err
			}
			err = tx.DeleteChannel(ctx, ch.ID)
			if errors.Is(err, store.ErrNotFound) {
				return nil // removed meanwhile
			}
			deleted = err == nil
			return err
		})
		if err != nil {
			return false, core.FromStore(err)
		}
		if gone {
			return false, nil
		}
		if deleted {
			nChannels++
		}
	}

	for _, id := range ids {
		retired := false
		err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			retired, gone = false, false
			ok, err := live(ctx, tx, name)
			if err != nil || !ok {
				gone = !ok
				return err
			}
			retired, err = tx.RetireIdentity(ctx, id.Name, now)
			return err
		})
		if err != nil {
			return false, core.FromStore(err)
		}
		if gone {
			return false, nil
		}
		if retired {
			nIdentities++
		}
	}

	done := false
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		done, gone = false, false
		ok, err := live(ctx, tx, name)
		if err != nil || !ok {
			gone = !ok
			return err
		}
		left, leftIDs, err := tx.ScopeMembers(ctx, name)
		if err != nil {
			return err
		}
		if len(left) != 0 || len(leftIDs) != 0 {
			return core.E(bus.CodeConflict, "scope %q gained members during retirement; the next run removes them", name)
		}
		if err := tx.MarkScopeRetired(ctx, name, now); err != nil {
			return err
		}
		done = true
		return nil
	})
	if err != nil {
		return false, core.FromStore(err)
	}
	if gone || !done {
		return false, nil
	}
	s.Log.Info("scope retired", "scope", name, "bridges", nBridges, "channels", nChannels, "identities", nIdentities)
	return true, nil
}

// PurgeRetired deletes the retired scopes that no channel or identity
// references any more and whose retirement is older than SessionRetentionHours
// (the retired identities' names stay reserved in retired_identities, so a
// purged scope name can be reused, its members' names cannot). Idempotent.
func (s Service) PurgeRetired(ctx context.Context, now int64) (int, error) {
	n := 0
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		n, err = tx.DeleteRetiredScopes(ctx, now-int64(s.Cfg.SessionRetentionHours)*hourMs)
		return err
	})
	return n, core.FromStore(err)
}
