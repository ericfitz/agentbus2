package channels

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Ported from v1 internal/bus/channels.go (CreateChannel, ListChannels,
// deleteChannel without the subscriber refusal, RenameChannel on the id),
// under the channel ACL (Decisions 80, 89, 93, 94).

// List returns every channel by name with its kind and nothing else; no
// permission is needed (Decision 94).
func (s Service) List(ctx context.Context, identity string) ([]bus.Channel, error) {
	out := []bus.Channel{}
	err := s.read(ctx, identity, func(ctx context.Context, tx store.Tx) error {
		out = out[:0]
		chs, err := tx.ListChannels(ctx)
		if err != nil {
			return err
		}
		for _, c := range chs {
			out = append(out, core.ChannelToBus(c))
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
	err = s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx) error {
		creator, err := s.RequireIdentity(ctx, tx, identity)
		if err != nil {
			return err
		}
		if s.Cfg.ChannelCreation == core.ChannelCreationAdmins {
			if _, err := s.RequireAdmin(ctx, tx, identity); err != nil {
				return err
			}
		}
		// The scope (Slack bridge spec 4.2): only an administrator names
		// one; otherwise the channel takes the creator's. Either must be
		// open.
		scope := creator.Scope
		if in.Scope != "" {
			if _, err := s.RequireAdmin(ctx, tx, identity); err != nil {
				return core.E(bus.CodeForbidden, "only an administrator may name a scope")
			}
			scope = in.Scope
		}
		if scope != "" {
			if _, err := scopes.RequireOpen(ctx, s.Core, tx, scope); err != nil {
				return err
			}
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

// adminChannel resolves a channel and requires admin on it.
func (s Service) adminChannel(ctx context.Context, tx store.Tx, identity, name string) (store.Channel, error) {
	ch, err := s.ChannelByName(ctx, tx, name)
	if err != nil {
		return store.Channel{}, err
	}
	if err := s.Authorize(ctx, tx, identity, ch.ID, store.PermAdmin); err != nil {
		return store.Channel{}, err
	}
	return ch, nil
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
	return s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx) error {
		ch, err := s.adminChannel(ctx, tx, identity, in.Name)
		if err != nil {
			return err
		}
		if err := protected(ch, "deleted"); err != nil {
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
	return s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx) error {
		ch, err := s.adminChannel(ctx, tx, identity, in.Name)
		if err != nil {
			return err
		}
		if err := protected(ch, "renamed"); err != nil {
			return err
		}
		return tx.RenameChannel(ctx, ch.ID, in.NewName)
	})
}

// ACL returns the channel's entries in insertion order; it needs admin on the
// channel.
func (s Service) ACL(ctx context.Context, identity, channel string) ([]bus.ACLEntry, error) {
	out := []bus.ACLEntry{}
	err := s.read(ctx, identity, func(ctx context.Context, tx store.Tx) error {
		ch, err := s.adminChannel(ctx, tx, identity, channel)
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
// 80, 96); nil Entries clears the ACL. It needs admin on the channel and
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
	err = s.mutate(ctx, identity, in.IdempotencyKey, payload, &res, func(ctx context.Context, tx store.Tx) error {
		channelID = 0
		ch, err := s.adminChannel(ctx, tx, identity, in.Channel)
		if err != nil {
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
