package slackbridge

import (
	"context"
	"errors"
	"slices"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/store"
)

// FinalLine is the last message posted in a Slack channel before it is
// archived (spec 6.5).
const FinalLine = "This channel is archived; the record stays in agentbus2 until its scope is retired."

// ArchiveBridge posts the final line, archives the Slack channel and marks
// the bridge archived (spec 6.5). reason is one of the scopes.Reason*
// constants and is only logged.
//
// State (Definition of Done D): a bridge already archived, or already
// deleted, is a no-op; a Slack channel already archived (by hand, or by an
// overlapping replica) is success, and so is one that is gone; a run that
// fails before the row is marked leaves the row as it was, so the next
// attempt posts the final line again at worst (a gone channel refuses it, and
// that refusal is ignored). A bridge still creating with no Slack channel has
// nothing to archive in Slack and is only marked. Before the Service has
// reached Slack it returns an error and changes nothing, so maintenance
// retries on its next tick.
func (s *Service) ArchiveBridge(ctx context.Context, b store.Bridge, reason string) error {
	if _, err := s.requireReady(); err != nil {
		return err
	}
	// b is the caller's snapshot; the row decides.
	var cur store.Bridge
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		cur, err = tx.Bridge(ctx, b.ID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return core.FromStore(err)
	}
	if cur.State == store.BridgeArchived {
		return nil
	}
	if cur.SlackChannelID != "" {
		if _, err := s.sc.Post(ctx, slackapi.PostInput{Channel: cur.SlackChannelID, Fallback: FinalLine, Section: FinalLine}); err != nil && !errors.Is(err, slackapi.ErrChannelGone) {
			return slackErr(err)
		}
		if err := s.sc.Archive(ctx, cur.SlackChannelID); err != nil &&
			!errors.Is(err, slackapi.ErrAlreadyArchived) && !errors.Is(err, slackapi.ErrChannelGone) {
			return slackErr(err)
		}
	}
	err = s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.SetBridgeArchived(ctx, cur.ID, s.core.NowMs()); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil // removed meanwhile
			}
			return err
		}
		return tx.Unsubscribe(ctx, store.SlackBridge, cur.ChannelID)
	})
	if err != nil {
		return core.FromStore(err)
	}
	s.clearBridgePosted(cur.ID)
	s.log.Info("slack bridge: archived", "channel", cur.ChannelName, "slack_channel", cur.SlackChannelID, "reason", reason)
	return nil
}

// Remove archives the Slack channel of the channel's bridge, then deletes the
// bridge row (with its map and drop rows), the slack-bridge subscription and
// the slack-bridge ACL entry (spec 6.1a). The scope and the bus channel stay.
//
// State (Definition of Done D): a second Remove finds no bridge and answers
// not_found; a Remove that fails after archiving (the delete transaction)
// leaves an archived row that the next Remove finishes without calling Slack;
// two overlapping Removes both archive idempotently and the loser of the
// delete answers not_found.
func (s *Service) Remove(ctx context.Context, admin string, in bus.RemoveBridgeInput) error {
	if _, err := s.guard(ctx, admin); err != nil {
		return err
	}
	var b store.Bridge
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		b, err = bridgeOf(ctx, tx, in.Channel)
		return err
	})
	if err != nil {
		return core.FromStore(err)
	}
	if err := s.ArchiveBridge(ctx, b, scopes.ReasonRemoved); err != nil {
		return err
	}
	err = s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.DeleteBridge(ctx, b.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return noBridge(in.Channel)
			}
			return err
		}
		ch, err := tx.Channel(ctx, b.ChannelID)
		if errors.Is(err, store.ErrNotFound) {
			return nil // the channel is gone; its ACL and subscriptions went with it
		}
		if err != nil {
			return err
		}
		if err := tx.Unsubscribe(ctx, store.SlackBridge, ch.ID); err != nil {
			return err
		}
		acl, err := tx.ACL(ctx, ch.ID)
		if err != nil {
			return err
		}
		kept := slices.DeleteFunc(slices.Clone(acl), func(e store.ACLEntry) bool { return e.Identity == store.SlackBridge })
		if len(kept) == len(acl) {
			return nil
		}
		return tx.SetACL(ctx, ch.ID, kept)
	})
	if err != nil {
		return core.FromStore(err)
	}
	s.clearBridgePosted(b.ID)
	s.log.Info("slack bridge: removed", "channel", b.ChannelName, "slack_channel", b.SlackChannelID)
	return nil
}
