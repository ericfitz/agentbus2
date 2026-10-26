package channels

import (
	"context"
	"errors"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Ported from v1 internal/bus/channels.go (CreateChannel, ListChannels,
// deleteChannel without the subscriber refusal, RenameChannel on the id),
// under the channel ACL (Decisions 80, 89, 93, 94).

// List returns the channels the identity may list, by name with their kind
// and nothing else. Until the channel-visibility rebuild the List action
// admits every channel of the principal's scope (Decision 94 for an unscoped
// principal: every channel; accesscheck spec 3.2).
func (s Service) List(ctx context.Context, identity string) ([]bus.Channel, error) {
	out := []bus.Channel{}
	err := s.read(ctx, identity, func(ctx context.Context, tx store.Tx, p access.Principal) error {
		out = out[:0]
		ids, err := s.Access.Channels(ctx, tx, p, access.List)
		if err != nil {
			return err
		}
		visible := make(map[int64]bool, len(ids))
		for _, id := range ids {
			visible[id] = true
		}
		chs, err := tx.ListChannels(ctx)
		if err != nil {
			return err
		}
		for _, c := range chs {
			if visible[c.ID] {
				out = append(out, core.ChannelToBus(c))
			}
		}
		return nil
	})
	return out, err
}

// Create makes a channel (Decision 93: any registered identity when
// ChannelCreation is "open", an administrator when "admins"). A nil ACL
// means the configured default, an empty one no entries.
func (s Service) Create(ctx context.Context, identity string, in bus.CreateChannelInput) (bus.ChannelInfo, error) {
	kind, err := core.KindFromBus(in.Kind)
	if err != nil {
		return bus.ChannelInfo{}, err
	}
	acl, err := core.ACLFromBus(in.ACL)
	if err != nil {
		return bus.ChannelInfo{}, err
	}
	if acl == nil && s.Cfg.DefaultACL != nil {
		acl = s.Cfg.DefaultACL(identity)
	}
	payload := struct {
		Op    string
		Name  string
		Kind  store.Kind
		ACL   []store.ACLEntry
		Scope string
	}{"create_channel", in.Name, kind, acl, in.Scope}
	var res bus.ChannelInfo
	err = s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx, p access.Principal) error {
		if err := s.Access.Require(ctx, tx, p, access.Server{}, access.CreateChannel); err != nil {
			return err
		}
		// The scope (Slack bridge spec 4.2): only an administrator names
		// one; otherwise the channel takes the creator's. Either must be
		// open.
		scope := p.Scope
		if in.Scope != "" {
			if err := s.Access.Require(ctx, tx, p, access.Server{}, access.Administer,
				access.WithText("only an administrator may name a scope")); err != nil {
				return err
			}
			scope = in.Scope
		}
		if err := s.visibleACL(ctx, tx, p, acl); err != nil {
			return err
		}
		if scope != "" {
			if _, err := scopes.RequireOpen(ctx, tx, scope); err != nil {
				return err
			}
		}
		if err := namePrefixRule(ctx, tx, p, in.Name, scope); err != nil {
			return err
		}
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		ch, err := tx.CreateChannel(ctx, store.ChannelCreate{
			Name: in.Name, Kind: kind, Creator: identity, Scope: scope, ACL: acl, CreatedSeq: seq, CreatedAt: s.NowMs(),
		})
		if err != nil {
			return err
		}
		res = core.ChannelInfoToBus(ch)
		return nil
	})
	return res, err
}

// namePrefixRule is the scope-prefixed naming rule (accesscheck spec 4.2,
// Eric's decision 2026-10-25): a scoped principal's channel is named
// <scope>-<rest>, and a name starting with <s>- for an existing scope s (any
// state) belongs to s. So a create or rename collision can only name a
// channel of the caller's own scope. It validates the name's shape, as
// CheckBridgeACL validates an ACL's; scope is the scope the channel will
// carry. Names that violate it already stored are not converted
// (pre-release).
func namePrefixRule(ctx context.Context, tx store.Tx, p access.Principal, name, scope string) error {
	if p.Scope != "" && !strings.HasPrefix(name, p.Scope+"-") {
		return core.E(bus.CodeValidation, "a channel of scope %q is named %s-<name>", p.Scope, p.Scope)
	}
	all, err := tx.ListScopes(ctx)
	if err != nil {
		return err
	}
	for _, sc := range all {
		if strings.HasPrefix(name, sc.Name+"-") && scope != sc.Name {
			return core.E(bus.CodeValidation, "names starting with %q- belong to scope %q", sc.Name, sc.Name)
		}
	}
	return nil
}

// adminChannel resolves a channel and requires admin on it.
func (s Service) adminChannel(ctx context.Context, tx store.Tx, p access.Principal, name string) (store.Channel, error) {
	ch, err := s.ChannelByName(ctx, tx, name)
	if err != nil {
		return store.Channel{}, err
	}
	if err := s.Access.Require(ctx, tx, p, access.Channel{ID: ch.ID}, access.Admin); err != nil {
		return store.Channel{}, err
	}
	return ch, nil
}

// visibleACL refuses, with the Decision 96 text, an ACL entry naming an
// identity the granter may not see (an unregistered one, or one outside a
// scoped granter's scope: the granter learns nothing, accesscheck spec 4.1
// (b) and (d)). The reserved names and malformed names are the Store's to
// validate.
func (s Service) visibleACL(ctx context.Context, tx store.Tx, p access.Principal, entries []store.ACLEntry) error {
	for _, e := range entries {
		if store.ReservedIdentity(e.Identity) || store.NameRule(e.Identity) != nil {
			continue // the Store's own refusal (and its text) applies
		}
		d, err := s.Access.Check(ctx, tx, p, access.Identity{Name: e.Identity}, access.See)
		if err != nil {
			return err
		}
		if !d.Allowed {
			return core.E(bus.CodeValidation, "ACL identity %q is not a registered identity", e.Identity)
		}
	}
	return nil
}

// liveBridge reports whether the channel has a bridge in state creating or
// live (Decision 12, spec 6.1b): such a bridge pins the channel's ACL, name
// and existence. An archived bridge or none leaves the ordinary rules. The
// guard is a rule here, not in the Store, so the bridge's own transactions
// are unaffected.
func liveBridge(ctx context.Context, tx store.Tx, channelID int64) (bool, error) {
	b, err := tx.BridgeByChannel(ctx, channelID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return b.State == store.BridgeCreating || b.State == store.BridgeLive, nil
}

// refuseBridged is the Delete and Rename guard.
func refuseBridged(ctx context.Context, tx store.Tx, ch store.Channel) error {
	pinned, err := liveBridge(ctx, tx, ch.ID)
	if err != nil {
		return err
	}
	if pinned {
		return core.E(bus.CodeConflict, "channel %q has a live Slack bridge; run bridges remove first", ch.Name)
	}
	return nil
}

func protected(ch store.Channel, what string) error {
	if ch.Name == GeneralChannel {
		return core.E(bus.CodeValidation, "the %s channel cannot be %s", GeneralChannel, what)
	}
	return nil
}

// Delete removes a channel; its ACL, subscriptions, cursors and messages go
// with it (Decision 89; no live-subscriber refusal, pending item 7).
// general is refused for everyone (Decision (Eric, 2026-10-21): Milestone 3 item 6).
func (s Service) Delete(ctx context.Context, identity string, in bus.DeleteChannelInput) error {
	payload := struct{ Op, Name string }{"delete_channel", in.Name}
	var res struct{}
	return s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx, p access.Principal) error {
		ch, err := s.adminChannel(ctx, tx, p, in.Name)
		if err != nil {
			return err
		}
		if err := protected(ch, "deleted"); err != nil {
			return err
		}
		if err := refuseBridged(ctx, tx, ch); err != nil {
			return err
		}
		return tx.DeleteChannel(ctx, ch.ID)
	})
}

// Rename changes a channel's name; the id, and so subscriptions and cursors,
// stay (Decision 89). general is refused for everyone.
func (s Service) Rename(ctx context.Context, identity string, in bus.RenameChannelInput) error {
	payload := struct{ Op, Name, NewName string }{"rename_channel", in.Name, in.NewName}
	var res struct{}
	return s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx, p access.Principal) error {
		ch, err := s.adminChannel(ctx, tx, p, in.Name)
		if err != nil {
			return err
		}
		if err := protected(ch, "renamed"); err != nil {
			return err
		}
		if err := refuseBridged(ctx, tx, ch); err != nil {
			return err
		}
		if err := namePrefixRule(ctx, tx, p, in.NewName, ch.Scope); err != nil {
			return err
		}
		return tx.RenameChannel(ctx, ch.ID, in.NewName)
	})
}

// ACL returns the channel's entries in insertion order; it needs admin on the
// channel.
func (s Service) ACL(ctx context.Context, identity, channel string) ([]bus.ACLEntry, error) {
	out := []bus.ACLEntry{}
	err := s.read(ctx, identity, func(ctx context.Context, tx store.Tx, p access.Principal) error {
		ch, err := s.adminChannel(ctx, tx, p, channel)
		if err != nil {
			return err
		}
		entries, err := tx.ACL(ctx, ch.ID)
		if err != nil {
			return err
		}
		out = core.ACLToBus(entries)
		if out == nil {
			out = []bus.ACLEntry{}
		}
		return nil
	})
	return out, err
}

// SetACL replaces the channel's entries (the Store validates them, Decisions
// 80, 96); nil Entries clears the ACL. It needs admin on the channel, and an
// agent cannot remove a human's or an administrator's access (spec 4.6). It
// publishes a zero-seq hint after commit so an armed watch re-checks
// readability.
func (s Service) SetACL(ctx context.Context, identity string, in bus.SetACLInput) error {
	entries, err := core.ACLFromBus(in.Entries)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []store.ACLEntry{}
	}
	payload := struct {
		Op      string
		Channel string
		Entries []store.ACLEntry
	}{"set_acl", in.Channel, entries}
	var res struct{}
	var channelID int64
	err = s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx, p access.Principal) error {
		channelID = 0
		ch, err := s.ChannelByName(ctx, tx, in.Channel)
		if err != nil {
			return err
		}
		if err := s.Access.Require(ctx, tx, p, access.ACLChange{ID: ch.ID, Entries: entries}, access.ReplaceACL); err != nil {
			return err
		}
		pinned, err := liveBridge(ctx, tx, ch.ID)
		if err != nil {
			return err
		}
		if pinned {
			if err := s.Access.Require(ctx, tx, p, access.Server{}, access.Administer,
				access.WithText("only a server administrator may change the ACL of a bridged channel")); err != nil {
				return err
			}
			if err := scopes.CheckBridgeACL(entries); err != nil {
				return err
			}
		}
		if err := s.visibleACL(ctx, tx, p, entries); err != nil {
			return err
		}
		if err := tx.SetACL(ctx, ch.ID, entries); err != nil {
			return err
		}
		channelID = ch.ID
		return nil
	})
	if err == nil && channelID != 0 {
		s.Publish(notify.Hint{ChannelID: channelID})
	}
	return err
}
