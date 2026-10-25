package slackbridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/store"
)

const (
	reasonNotFound = "not found in Slack"
	reasonGuest    = "Slack guest or external user"
)

// reserveAttempts bounds how often step 1 reruns after losing the insert to
// an overlapping Create.
const reserveAttempts = 3

// createLease bounds the SQLite lease on a create; the Slack calls under it
// are far shorter.
const createLease = 2 * time.Minute

// human is an identity with explicit write on the channel and its email
// binding.
type human struct{ identity, email string }

// reservation is what step 1 hands the rest of Create.
type reservation struct {
	bridge  store.Bridge
	channel store.Channel
	resumed bool
	// inserted: this call inserted the row (only then may it delete it).
	inserted bool
	humans   []human
}

func nameTaken(name string) error {
	return core.E(bus.CodeConflict, "Slack channel name %q is taken", name)
}

func alreadyBridged(channel string) error {
	return core.E(bus.CodeConflict, "channel %q is already bridged", channel)
}

// withBridgeRead adds the {slack-bridge, [read]} entry when the ACL lacks a
// read grant for it.
func withBridgeRead(acl []store.ACLEntry) []store.ACLEntry {
	out := slices.Clone(acl)
	for i, e := range out {
		if e.Identity == store.SlackBridge {
			if !slices.Contains(e.Permissions, store.PermRead) {
				out[i].Permissions = append(slices.Clone(e.Permissions), store.PermRead)
			}
			return out
		}
	}
	return append(out, store.ACLEntry{Identity: store.SlackBridge, Permissions: []store.Permission{store.PermRead}})
}

// Create bridges a channel to a new private Slack channel (spec 6.1).
//
// Step 1 stores a bridge row in state creating with the intended Slack name,
// one transaction that also checks the channel (ordinary, scoped, scope open),
// its ACL (Decision 12) and that it has no bridge. Step 2 creates the private
// Slack channel and stores its ID. Step 3 invites every human with explicit
// write who is a full member of this workspace. Step 4 makes the bridge live,
// adds {slack-bridge, [read]} to the ACL and subscribes slack-bridge at the
// channel's current end. Step 5 reports the humans who could not be invited
// in a notice in both channels.
//
// Any failure, a Slack rate limit (*slackapi.RateLimitedError) included,
// returns an error and leaves the row creating; the next Create resumes it.
// Users invited before the failure stay invited, and inviting them again is
// harmless because already_in_channel counts as success.
//
// State (Definition of Done D): a second Create on a live or archived channel
// is conflict "already bridged". A run killed after step 1 leaves a creating
// row, and the next Create resumes it under the stored name (a different
// slack name is conflict naming the stored one). Killed after step 2, the row
// has the Slack ID, so the next run creates nothing and invites again. Two
// overlapping Creates share the unique row; the loser of the Slack name race
// that resumed the row adopts the winner's channel through
// FindPrivateChannel, and both end live with one Slack channel. A name taken
// by a channel that is not ours, or archived, is conflict and the creating
// row without a Slack channel is deleted. A killed step 5 loses the notice
// only; the bridge is live and the notice is best effort.
func (s *Service) Create(ctx context.Context, admin string, in bus.CreateBridgeInput) (bus.BridgeCreated, error) {
	self, err := s.guard(ctx, admin)
	if err != nil {
		return bus.BridgeCreated{}, err
	}
	b, r, err := s.reserveAndOpen(ctx, in)
	if err != nil {
		return bus.BridgeCreated{}, err
	}
	notInvited, err := s.invite(ctx, self, b.SlackChannelID, r.humans)
	if err != nil {
		return bus.BridgeCreated{}, err
	}
	live, wasLive, err := s.goLive(ctx, b)
	if err != nil {
		return bus.BridgeCreated{}, err
	}
	if wasLive {
		// An overlapping Create finished first and reported; this one only
		// returns the same bridge.
		return bus.BridgeCreated{Bridge: toBus(live), NotInvited: notInvited}, nil
	}
	s.log.Info("slack bridge: created", "channel", live.ChannelName, "slack_channel", live.SlackChannelID, "not_invited", len(notInvited))
	if len(notInvited) > 0 {
		s.notice(ctx, live, notInvited)
	}
	return bus.BridgeCreated{Bridge: toBus(live), NotInvited: notInvited}, nil
}

// reserveAndOpen is steps 1 and 2, serialized within this process.
//
// Across replicas the two steps hold the Store lock "bridge-create:<channel
// id>" (a lease of createLease on SQLite, the session on Postgres), so a
// resumed row and a fresh one never race for the Slack name; a held lock is a
// retryable unavailable.
func (s *Service) reserveAndOpen(ctx context.Context, in bus.CreateBridgeInput) (store.Bridge, reservation, error) {
	var ch store.Channel
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		ch, err = s.core.ChannelByName(ctx, tx, in.Channel)
		return err
	})
	if err != nil {
		return store.Bridge{}, reservation{}, core.FromStore(err)
	}
	defer s.createLocks.lock(ch.ID)()
	owner, err := core.RandomHex(8)
	if err != nil {
		return store.Bridge{}, reservation{}, core.FromStore(err)
	}
	lock := fmt.Sprintf("bridge-create:%d", ch.ID)
	now := s.core.Now()
	held, err := s.core.Store.TryLock(ctx, lock, owner, now.UnixMilli(), now.Add(createLease).UnixMilli())
	if err != nil {
		return store.Bridge{}, reservation{}, core.FromStore(err)
	}
	if !held {
		return store.Bridge{}, reservation{}, core.E(bus.CodeUnavailable, "another create of this bridge is in progress; retry")
	}
	defer func() {
		if err := s.core.Store.Unlock(context.WithoutCancel(ctx), lock, owner); err != nil {
			s.log.Warn("slack bridge: create lock release failed", "lock", lock, "err", err)
		}
	}()
	r, err := s.reserve(ctx, in)
	if err != nil {
		return store.Bridge{}, r, err
	}
	b := r.bridge
	if b.SlackChannelID == "" {
		if b, err = s.openSlackChannel(ctx, r); err != nil {
			return store.Bridge{}, r, err
		}
	}
	return b, r, nil
}

// reserve is step 1.
func (s *Service) reserve(ctx context.Context, in bus.CreateBridgeInput) (reservation, error) {
	if in.SlackName != "" && !slackapi.ValidChannelName(in.SlackName) {
		return reservation{}, core.E(bus.CodeValidation, "slack name %q is not valid: use 1 to 80 characters of a-z, 0-9, - and _", in.SlackName)
	}
	var r reservation
	var err error
	for range reserveAttempts {
		err = s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			r = reservation{}
			return s.reserveTx(ctx, tx, in, &r)
		})
		if !errors.Is(err, store.ErrConflict) {
			break
		}
	}
	if err != nil {
		return reservation{}, core.FromStore(err)
	}
	return r, nil
}

func (s *Service) reserveTx(ctx context.Context, tx store.Tx, in bus.CreateBridgeInput, r *reservation) error {
	ch, err := s.core.ChannelByName(ctx, tx, in.Channel)
	if err != nil {
		return err
	}
	if ch.Kind != store.KindOrdinary {
		return core.E(bus.CodeValidation, "a bridge needs an ordinary channel")
	}
	if ch.Scope == "" {
		return core.E(bus.CodeValidation, "channel %q has no scope; a bridge requires a scoped channel", ch.Name)
	}
	r.channel = ch
	existing, err := tx.BridgeByChannel(ctx, ch.ID)
	switch {
	case err == nil:
		if existing.State != store.BridgeCreating {
			return alreadyBridged(ch.Name)
		}
		if in.SlackName != "" && in.SlackName != existing.SlackName {
			return core.E(bus.CodeConflict, "channel %q has a bridge being created under the Slack name %q; use that name or run bridges remove", ch.Name, existing.SlackName)
		}
		r.bridge, r.resumed = existing, true
	case errors.Is(err, store.ErrNotFound):
	default:
		return err
	}
	if _, err := scopes.RequireOpen(ctx, tx, ch.Scope); err != nil {
		return err
	}
	acl, err := tx.ACL(ctx, ch.ID)
	if err != nil {
		return err
	}
	if err := scopes.CheckBridgeACL(withBridgeRead(acl)); err != nil {
		return err
	}
	if r.humans, err = writeHumans(ctx, tx, acl); err != nil {
		return err
	}
	if r.resumed {
		return nil
	}
	name := in.SlackName
	if name == "" {
		if name, err = SlackChannelName(ch.Name); err != nil {
			return core.E(bus.CodeValidation, "%s", err.Error())
		}
	}
	r.inserted = true
	r.bridge, err = tx.CreateBridge(ctx, store.Bridge{
		ChannelID: ch.ID, ChannelName: ch.Name, Scope: ch.Scope, SlackName: name, CreatedAt: s.core.NowMs(),
	})
	return err
}

// writeHumans lists the humans holding an explicit write entry, in ACL order,
// each with its email binding ("" when it has none).
func writeHumans(ctx context.Context, tx store.Tx, acl []store.ACLEntry) ([]human, error) {
	var out []human
	for _, e := range acl {
		if !slices.Contains(e.Permissions, store.PermWrite) || store.ReservedIdentity(e.Identity) {
			continue
		}
		id, err := tx.Identity(ctx, e.Identity)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if id.Kind != store.IdentityHuman {
			continue
		}
		h := human{identity: e.Identity}
		switch hb, err := tx.HumanBinding(ctx, e.Identity); {
		case err == nil:
			if hb.ClaimType == store.ClaimEmail {
				h.email = hb.Value
			}
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// openSlackChannel is step 2: it creates the private Slack channel, or adopts
// the one a crashed or overlapping run created, and records its ID.
func (s *Service) openSlackChannel(ctx context.Context, r reservation) (store.Bridge, error) {
	b := r.bridge
	id, err := s.sc.CreatePrivateChannel(ctx, b.SlackName)
	if errors.Is(err, slackapi.ErrNameTaken) {
		id, err = s.adopt(ctx, r)
	}
	if err != nil {
		return store.Bridge{}, slackErr(err)
	}
	var out store.Bridge
	err = s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		cur, err := tx.Bridge(ctx, b.ID)
		if err != nil {
			return err
		}
		switch {
		case cur.State == store.BridgeArchived:
			return alreadyBridged(b.ChannelName)
		case cur.SlackChannelID != "":
			// An overlapping Create recorded its channel first; the unique
			// name makes it the same one.
			out = cur
			return nil
		}
		if err := tx.SetBridgeSlackChannel(ctx, b.ID, id); err != nil {
			return err
		}
		out, err = tx.Bridge(ctx, b.ID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return store.Bridge{}, core.E(bus.CodeNotFound, "the bridge of channel %q was removed meanwhile; retry", b.ChannelName)
	}
	if err != nil {
		return store.Bridge{}, core.FromStore(err)
	}
	return out, nil
}

// adopt answers the ID of the Slack channel that holds the bridge's name. Only
// a resumed row adopts, and only an unarchived channel the app can see;
// anything else is conflict. A row this call inserted (no Slack channel yet)
// is deleted so the channel is not left pinned; a resumed row is left for
// bridges remove, since another replica may be using it.
func (s *Service) adopt(ctx context.Context, r reservation) (string, error) {
	b := r.bridge
	if r.resumed {
		info, err := s.sc.FindPrivateChannel(ctx, b.SlackName)
		switch {
		case err == nil && !info.Archived:
			return info.ID, nil
		case err != nil && !errors.Is(err, slackapi.ErrChannelNotFound):
			return "", err
		}
	}
	if !r.inserted {
		return "", core.E(bus.CodeConflict, "Slack channel name %q is taken; run bridges remove for channel %q, then create it again with another -slack-name", b.SlackName, b.ChannelName)
	}
	if err := s.dropCreating(ctx, b); err != nil {
		return "", err
	}
	return "", nameTaken(b.SlackName)
}

// dropCreating deletes a bridge row that is still creating without a Slack
// channel; a row an overlapping Create has since given one is left alone.
func (s *Service) dropCreating(ctx context.Context, b store.Bridge) error {
	return core.FromStore(s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		cur, err := tx.Bridge(ctx, b.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if cur.State != store.BridgeCreating || cur.SlackChannelID != "" {
			return nil
		}
		return tx.DeleteBridge(ctx, b.ID)
	}))
}

// invite is step 3.
func (s *Service) invite(ctx context.Context, self slackapi.Self, slackChannel string, humans []human) ([]bus.BridgeNotInvited, error) {
	var out []bus.BridgeNotInvited
	var ids []string
	owner := map[string]string{} // Slack user ID -> identity
	for _, h := range humans {
		if h.email == "" {
			out = append(out, bus.BridgeNotInvited{Identity: h.identity, Reason: reasonNotFound})
			continue
		}
		u, err := s.sc.LookupByEmail(ctx, h.email)
		if errors.Is(err, slackapi.ErrUserNotFound) {
			out = append(out, bus.BridgeNotInvited{Identity: h.identity, Reason: reasonNotFound})
			continue
		}
		if err != nil {
			return nil, slackErr(err)
		}
		switch {
		case u.Deleted || u.IsBot:
			out = append(out, bus.BridgeNotInvited{Identity: h.identity, Reason: reasonNotFound})
		case u.Restricted || u.UltraRestricted || u.Stranger || otherTeam(u, self):
			// Decision 18: guests and external users are never invited.
			out = append(out, bus.BridgeNotInvited{Identity: h.identity, Reason: reasonGuest})
		default:
			if _, dup := owner[u.ID]; !dup {
				ids = append(ids, u.ID)
				owner[u.ID] = h.identity
			}
		}
	}
	if len(ids) > 0 {
		fails, err := s.sc.Invite(ctx, slackChannel, ids)
		if err != nil {
			return nil, slackErr(err)
		}
		for _, f := range fails {
			out = append(out, bus.BridgeNotInvited{Identity: owner[f.User], Reason: "invite failed: " + f.Code})
		}
	}
	return out, nil
}

// goLive is step 4.
func (s *Service) goLive(ctx context.Context, b store.Bridge) (out store.Bridge, wasLive bool, err error) {
	err = s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		wasLive = false
		cur, err := tx.Bridge(ctx, b.ID)
		if errors.Is(err, store.ErrNotFound) {
			return core.E(bus.CodeNotFound, "the bridge of channel %q was removed meanwhile; retry", b.ChannelName)
		}
		if err != nil {
			return err
		}
		if cur.State == store.BridgeArchived {
			return alreadyBridged(b.ChannelName)
		}
		acl, err := tx.ACL(ctx, b.ChannelID)
		if err != nil {
			return err
		}
		want := withBridgeRead(acl)
		if err := scopes.CheckBridgeACL(want); err != nil {
			return err
		}
		wasLive = cur.State == store.BridgeLive
		if err := tx.SetBridgeLive(ctx, b.ID); err != nil {
			return err
		}
		if !slices.EqualFunc(acl, want, func(a, b store.ACLEntry) bool {
			return a.Identity == b.Identity && slices.Equal(a.Permissions, b.Permissions)
		}) {
			if err := tx.SetACL(ctx, b.ChannelID, want); err != nil {
				return err
			}
		}
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		if err := tx.Subscribe(ctx, store.SlackBridge, b.ChannelID, seq, s.core.NowMs()); err != nil {
			return err
		}
		out, err = tx.Bridge(ctx, b.ID)
		return err
	})
	if err != nil {
		return store.Bridge{}, false, core.FromStore(err)
	}
	return out, wasLive, nil
}

// notice is step 5: a Slack post and a bus message from slack-bridge naming
// the identities that were not invited. The bridge is live by now, so a
// failure here is logged and not returned.
func (s *Service) notice(ctx context.Context, b store.Bridge, ni []bus.BridgeNotInvited) {
	var sb strings.Builder
	sb.WriteString("Not invited to the Slack channel:")
	for i, n := range ni {
		if i > 0 {
			sb.WriteByte(';')
		}
		sb.WriteString(" " + n.Identity + " (" + n.Reason + ")")
	}
	text := sb.String()
	esc := escapeSlack(text)
	if _, err := s.sc.Post(ctx, slackapi.PostInput{Channel: b.SlackChannelID, Fallback: esc, Section: esc}); err != nil {
		s.log.Warn("slack bridge: posting the not-invited notice failed", "channel", b.ChannelName, "err", err)
	}
	// slack-bridge is not an actor RequireIdentity accepts, so the message
	// goes through core.Append directly.
	var seq int64
	err := s.core.Store.Append(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		seq, _, err = s.core.Append(ctx, tx, core.AppendInput{
			ChannelID: b.ChannelID, Channel: b.ChannelName, Kind: store.KindOrdinary,
			Sender: store.SlackBridge, SenderKind: store.IdentityAgent, Type: "notice", Content: text,
		})
		return err
	})
	if err != nil {
		s.log.Warn("slack bridge: appending the not-invited notice failed", "channel", b.ChannelName, "err", err)
		return
	}
	s.core.Publish(notify.Hint{ChannelID: b.ChannelID, Seq: seq})
}
