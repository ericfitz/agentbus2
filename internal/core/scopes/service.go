// Package scopes holds the rules of scopes: a named group of channels and
// agent identities that an administrator creates, marks done and that is
// retired as one unit (Slack bridge spec 4, Decisions 7 to 9). Every
// operation is administrator only and one Store transaction.
package scopes

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Service is the scopes aggregate over a Core.
type Service struct{ *core.Core }

// RequireOpen returns the scope when it exists and is open. Anything else is
// refused: an unknown scope is not_found, a retired one is not_found with the
// retired text (spec 4.1) and a done one is conflict, since nothing new may
// join it (spec 4.2). A store error other than ErrNotFound is returned as it
// came, so the Store's retry loop still sees a retryable engine error.
func RequireOpen(ctx context.Context, _ *core.Core, tx store.Tx, name string) (store.Scope, error) {
	sc, err := load(ctx, tx, name)
	if err != nil {
		return store.Scope{}, err
	}
	if sc.State == store.ScopeDone {
		return store.Scope{}, core.E(bus.CodeConflict, "scope %q is done; nothing new may join it", name)
	}
	return sc, nil
}

// load reads a scope that is not retired: unknown and retired are not_found.
func load(ctx context.Context, tx store.Tx, name string) (store.Scope, error) {
	sc, err := tx.Scope(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Scope{}, core.E(bus.CodeNotFound, "scope %q does not exist", name)
	}
	if err != nil {
		return store.Scope{}, err
	}
	if sc.State == store.ScopeRetired {
		return store.Scope{}, core.E(bus.CodeNotFound, "scope %q is retired", name)
	}
	return sc, nil
}

// Create makes an open scope, idempotent under the key. A name that exists in
// any state, retired included, is conflict.
func (s Service) Create(ctx context.Context, admin string, in bus.CreateScopeInput) (bus.Scope, error) {
	payload := struct{ Op, Name string }{"create_scope", in.Name}
	var res bus.Scope
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		res = bus.Scope{}
		if _, err := s.RequireAdmin(ctx, tx, admin); err != nil {
			return err
		}
		_, err := s.WithReceipt(ctx, tx, admin, in.IdempotencyKey, payload, &res, func() error {
			if err := tx.CreateScope(ctx, in.Name, s.NowMs()); err != nil {
				return err
			}
			sc, err := tx.Scope(ctx, in.Name)
			if err != nil {
				return err
			}
			res = core.ScopeToBus(sc)
			return nil
		})
		return err
	})
	return res, core.FromStore(err)
}

// List returns the open and done scopes by name, without their members.
func (s Service) List(ctx context.Context, admin string) ([]bus.Scope, error) {
	out := []bus.Scope{}
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out = out[:0]
		if _, err := s.RequireAdmin(ctx, tx, admin); err != nil {
			return err
		}
		all, err := tx.ListScopes(ctx)
		if err != nil {
			return err
		}
		for _, sc := range all {
			if sc.State != store.ScopeRetired {
				out = append(out, core.ScopeToBus(sc))
			}
		}
		return nil
	})
	return out, core.FromStore(err)
}

// Get returns one scope with its channel and identity names. A retired scope
// is not_found with the retired text.
func (s Service) Get(ctx context.Context, admin, name string) (bus.Scope, error) {
	var res bus.Scope
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		res = bus.Scope{}
		if _, err := s.RequireAdmin(ctx, tx, admin); err != nil {
			return err
		}
		sc, err := load(ctx, tx, name)
		if err != nil {
			return err
		}
		chs, ids, err := tx.ScopeMembers(ctx, name)
		if err != nil {
			return err
		}
		res = core.ScopeToBus(sc)
		res.Channels = make([]string, 0, len(chs))
		for _, c := range chs {
			res.Channels = append(res.Channels, c.Name)
		}
		res.Identities = make([]string, 0, len(ids))
		for _, id := range ids {
			res.Identities = append(res.Identities, id.Name)
		}
		return nil
	})
	return res, core.FromStore(err)
}

// Done marks an open scope done with its retirement mode (Decision 7).
// with_bridge needs a live bridge of the scope (conflict otherwise); a done
// scope cannot be marked again; a retired one is not_found.
//
// State (Definition of Done D): a second call with the same key replays the
// receipt; without a key it answers conflict and changes nothing; the mark
// and the receipt are one transaction, so a failed run leaves nothing.
func (s Service) Done(ctx context.Context, admin string, in bus.DoneScopeInput) (bus.Scope, error) {
	mode := store.RetireMode(in.Retire)
	if !store.ValidRetireMode(mode) {
		return bus.Scope{}, core.E(bus.CodeValidation, "retire must be %q or %q, got %q", bus.RetireWithBridge, bus.RetireNormal, string(in.Retire))
	}
	payload := struct {
		Op, Name string
		Retire   store.RetireMode
	}{"done_scope", in.Name, mode}
	var res bus.Scope
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		res = bus.Scope{}
		if _, err := s.RequireAdmin(ctx, tx, admin); err != nil {
			return err
		}
		_, err := s.WithReceipt(ctx, tx, admin, in.IdempotencyKey, payload, &res, func() error {
			sc, err := load(ctx, tx, in.Name)
			if err != nil {
				return err
			}
			if sc.State == store.ScopeDone {
				return core.E(bus.CodeConflict, "scope %q is already done", in.Name)
			}
			if mode == store.RetireWithBridge {
				bridges, err := tx.ScopeBridges(ctx, in.Name)
				if err != nil {
					return err
				}
				live := false
				for _, b := range bridges {
					live = live || b.State == store.BridgeLive
				}
				if !live {
					return core.E(bus.CodeConflict, "scope %q has no live bridge", in.Name)
				}
			}
			if err := tx.MarkScopeDone(ctx, in.Name, mode, s.NowMs()); err != nil {
				return err
			}
			sc, err = tx.Scope(ctx, in.Name)
			if err != nil {
				return err
			}
			res = core.ScopeToBus(sc)
			return nil
		})
		return err
	})
	return res, core.FromStore(err)
}
