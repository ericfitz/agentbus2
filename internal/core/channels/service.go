// Package channels holds the rules of channels, their ACLs, subscriptions,
// tag subscriptions and the administrator's status (spec sections 3.2, 4.6).
// Every operation is one Store transaction; authorization is
// Core.Authorize inside it (Decision 80).
package channels

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

// GeneralChannel is the channel the server creates at start (Decision 92).
const GeneralChannel = "general"

// Service is the channel aggregate over a Core.
type Service struct{ *core.Core }

// EnsureGeneral creates the general channel with Creator "" and the
// configured default ACL (Decision 92) unless it exists. No creator entry is
// written: administrators act through the implicit admins entry. It
// subscribes nobody; that happens at registration (Milestone 5).
//
// State (Definition of Done D): a second run finds the channel and writes
// nothing; two overlapping runs race on the unique name and the loser's
// ErrConflict is success; a failed run's leftovers are nothing, since the
// channel and its ACL are written in one transaction.
func (s Service) EnsureGeneral(ctx context.Context) error {
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.ChannelByName(ctx, GeneralChannel); err == nil {
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		var acl []store.ACLEntry // nil: the Store writes store.DefaultACL("")
		if s.Cfg.DefaultACL != nil {
			acl = s.Cfg.DefaultACL("")
		}
		_, err = tx.CreateChannel(ctx, store.ChannelCreate{
			Name: GeneralChannel, Kind: store.KindOrdinary, ACL: acl, CreatedSeq: seq, CreatedAt: s.NowMs(),
		})
		return err
	})
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	return core.FromStore(err)
}

// mutate runs fn in one transaction as a registered identity, wrapped in the
// idempotency receipt. payload is what the key's fingerprint covers (not the
// key itself); result is stored and replayed. fn has no side effects
// (Decision 26): it may run more than once.
func (s Service) mutate(ctx context.Context, identity, key string, payload, result any, fn func(ctx context.Context, tx store.Tx) error) error {
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := s.RequireIdentity(ctx, tx, identity); err != nil {
			return err
		}
		_, err := s.WithReceipt(ctx, tx, identity, key, payload, result, func() error { return fn(ctx, tx) })
		return err
	})
	return core.FromStore(err)
}

// read runs fn in a read-only transaction as a registered identity.
func (s Service) read(ctx context.Context, identity string, fn func(ctx context.Context, tx store.Tx) error) error {
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := s.RequireIdentity(ctx, tx, identity); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
	return core.FromStore(err)
}
