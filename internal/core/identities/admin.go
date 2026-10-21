package identities

import (
	"context"
	"crypto"
	"errors"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/store"
)

// requireRegistered is not_registered with a hint for the host commands.
func requireRegistered(ctx context.Context, tx store.Tx, name string) error {
	if name != "" && name != store.Everyone && name != store.Admins {
		_, err := tx.Identity(ctx, name)
		if err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return core.E(bus.CodeNotRegistered, "identity %q is not registered; register it first", name)
}

// MakeAdmin sets or clears the administrator flag; idempotent.
func (s Service) MakeAdmin(ctx context.Context, name string, admin bool) error {
	return core.FromStore(s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := requireRegistered(ctx, tx, name); err != nil {
			return err
		}
		return tx.SetAdmin(ctx, name, admin)
	}))
}

// ResetCredential removes every registered key of the identity and returns
// how many (0 is success; Decision 58).
func (s Service) ResetCredential(ctx context.Context, name string) (int, error) {
	var n int
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		n, err = resetCredential(ctx, tx, name)
		return err
	})
	return n, core.FromStore(err)
}

func resetCredential(ctx context.Context, tx store.Tx, name string) (int, error) {
	if err := requireRegistered(ctx, tx, name); err != nil {
		return 0, err
	}
	return tx.DeletePublicKeys(ctx, name)
}

type resetCredentialPayload struct {
	Name string `json:"name"`
}

type resetCredentialResult struct {
	Removed int `json:"removed"`
}

// ResetCredentialAs is ResetCredential for an administrator over the API
// (Decision 73), idempotent under key.
func (s Service) ResetCredentialAs(ctx context.Context, admin, name string, key string) (int, error) {
	var res resetCredentialResult
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		res = resetCredentialResult{}
		if _, err := s.RequireAdmin(ctx, tx, admin); err != nil {
			return err
		}
		_, err := s.WithReceipt(ctx, tx, admin, key, resetCredentialPayload{name}, &res, func() error {
			n, err := resetCredential(ctx, tx, name)
			res = resetCredentialResult{n}
			return err
		})
		return err
	})
	if err != nil {
		return 0, core.FromStore(err)
	}
	return res.Removed, nil
}

// PublicKey returns the identity's key with the given kid, not_found when
// there is none.
func (s Service) PublicKey(ctx context.Context, identity, kid string) (crypto.PublicKey, keys.Algorithm, error) {
	var der []byte
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		ks, err := tx.PublicKeys(ctx, identity)
		if err != nil {
			return err
		}
		for _, k := range ks {
			if k.KID == kid {
				der = k.Key
				return nil
			}
		}
		return core.E(bus.CodeNotFound, "no such key")
	})
	if err != nil {
		return nil, "", core.FromStore(err)
	}
	pub, alg, err := keys.ParsePublic(der)
	if err != nil {
		return nil, "", bus.Internal(err)
	}
	return pub, alg, nil
}

type resetPayload struct {
	Scope bus.ResetScope `json:"scope"`
}

// Reset wipes the bus data (Eric, 2026-10-21, Milestone 5 item 1; ported
// from v1 internal/bus/admin.go Reset). Scope data (or "") keeps identities,
// keys, tokens and the signing key; everything also removes the other
// identities with their keys and every token, keeping the caller and the
// signing key. general is recreated and every remaining identity subscribed.
//
// State (Definition of Done D): a second run gives the same empty state; the
// receipt is stored after the wipe, so the same key replays.
func (s Service) Reset(ctx context.Context, admin string, key string, scope bus.ResetScope) error {
	switch scope {
	case "":
		scope = bus.ResetData
	case bus.ResetData, bus.ResetEverything:
	default:
		return core.E(bus.CodeValidation, "unknown reset scope %q", string(scope))
	}
	var done struct{}
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := s.RequireAdmin(ctx, tx, admin); err != nil {
			return err
		}
		_, err := s.WithReceipt(ctx, tx, admin, key, resetPayload{scope}, &done, func() error {
			if err := tx.ResetData(ctx); err != nil {
				return err
			}
			if scope == bus.ResetEverything {
				if err := tx.ResetRegistry(ctx, admin); err != nil {
					return err
				}
			}
			seq, err := tx.MaxSeq(ctx)
			if err != nil {
				return err
			}
			var acl []store.ACLEntry
			if s.Cfg.DefaultACL != nil {
				acl = s.Cfg.DefaultACL("")
			}
			now := s.NowMs()
			gen, err := tx.CreateChannel(ctx, store.ChannelCreate{
				Name: channels.GeneralChannel, Kind: store.KindOrdinary, ACL: acl, CreatedSeq: seq, CreatedAt: now,
			})
			if err != nil {
				return err
			}
			ids, err := tx.ListIdentities(ctx)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := tx.Subscribe(ctx, id.Name, gen.ID, seq, now); err != nil {
					return err
				}
			}
			return nil
		})
		return err
	})
	if err != nil {
		return core.FromStore(err)
	}
	s.Publish(notify.Hint{})
	return nil
}
