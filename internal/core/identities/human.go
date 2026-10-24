package identities

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/store"
)

// HoldsKeysText is the detail RegisterLocalHuman's conflict carries when the
// identity holds keys, so a caller can tell it from an exhausted rerun.
const HoldsKeysText = "holds keys, so it cannot be a human"

// CreateIdentityByEmail creates the human identity named by email (spec 4):
// the email is normalized, the identity's name is the normalized address, and
// the binding has no issuer and no provider subject until the human first
// signs in. created is false for a human already bound to that address;
// conflict for an agent of that name. It reruns the transaction on
// ErrConflict, up to maxAttempts.
//
// State (Definition of Done D): a second run changes nothing; two concurrent
// runs end with one row (the loser reruns and finds it); a failed run leaves
// nothing, since everything is one transaction.
func (s Service) CreateIdentityByEmail(ctx context.Context, email string) (identity string, created bool, err error) {
	e, err := NormalizeEmail(email)
	if err != nil {
		return "", false, err
	}
	if err := (channels.Service{Core: s.Core}).EnsureGeneral(ctx); err != nil {
		return "", false, err
	}
	for attempt := 1; ; attempt++ {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			identity, created, err = createByEmailTx(ctx, tx, e, s.NowMs())
			return err
		})
		if errors.Is(err, store.ErrConflict) && attempt < maxAttempts {
			continue
		}
		break
	}
	if err != nil {
		return "", false, core.FromStore(err)
	}
	return identity, created, nil
}

func createByEmailTx(ctx context.Context, tx store.Tx, email string, now int64) (string, bool, error) {
	b, err := tx.HumanBindingByValue(ctx, store.ClaimEmail, email)
	if err == nil {
		return b.Identity, false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", false, err
	}
	id, err := tx.Identity(ctx, email)
	if err == nil {
		if id.Kind == store.IdentityHuman {
			return "", false, core.E(bus.CodeConflict, "identity %q is a human not bound to that email", email)
		}
		return "", false, core.E(bus.CodeConflict, "identity %q is an agent", email)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", false, err
	}
	if err := createHumanWithGeneral(ctx, tx, store.HumanBinding{Identity: email, ClaimType: store.ClaimEmail, Value: email}, now); err != nil {
		return "", false, err
	}
	return email, true, nil
}

type createByEmailPayload struct {
	Email string `json:"email"`
}

type createByEmailResult struct {
	Identity string `json:"identity"`
	Created  bool   `json:"created"`
}

// CreateIdentityByEmailAs is CreateIdentityByEmail for an administrator over
// the API, idempotent under key (as ResetCredentialAs).
func (s Service) CreateIdentityByEmailAs(ctx context.Context, admin, email, key string) (string, bool, error) {
	e, err := NormalizeEmail(email)
	if err != nil {
		return "", false, err
	}
	if err := (channels.Service{Core: s.Core}).EnsureGeneral(ctx); err != nil {
		return "", false, err
	}
	var res createByEmailResult
	for attempt := 1; ; attempt++ {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			res = createByEmailResult{}
			if _, err := s.RequireAdmin(ctx, tx, admin); err != nil {
				return err
			}
			_, err := s.WithReceipt(ctx, tx, admin, key, createByEmailPayload{e}, &res, func() error {
				id, created, err := createByEmailTx(ctx, tx, e, s.NowMs())
				res = createByEmailResult{id, created}
				return err
			})
			return err
		})
		if errors.Is(err, store.ErrConflict) && attempt < maxAttempts {
			continue
		}
		break
	}
	if err != nil {
		return "", false, core.FromStore(err)
	}
	return res.Identity, res.Created, nil
}

// LookupHumanByEmail answers the identity bound to email (normalized first);
// not_found when none is.
func (s Service) LookupHumanByEmail(ctx context.Context, email string) (string, error) {
	e, err := NormalizeEmail(email)
	if err != nil {
		return "", err
	}
	var name string
	err = s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		b, err := tx.HumanBindingByValue(ctx, store.ClaimEmail, e)
		name = b.Identity
		return err
	})
	if err != nil {
		return "", core.FromStore(err)
	}
	return name, nil
}

// RegisterLocalHuman is RegisterLocal for the TUI's OS-login identity in a
// local profile: the row is created if missing and marked human, in one
// transaction; idempotent. An identity that holds keys is conflict.
func (s Service) RegisterLocalHuman(ctx context.Context, name string) error {
	if err := (channels.Service{Core: s.Core}).EnsureGeneral(ctx); err != nil {
		return err
	}
	var err error
	for attempt := 1; ; attempt++ {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			if _, err := tx.Identity(ctx, name); errors.Is(err, store.ErrNotFound) {
				if err := createWithGeneral(ctx, tx, name, s.NowMs()); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			ks, err := tx.PublicKeys(ctx, name)
			if err != nil {
				return err
			}
			if len(ks) > 0 {
				return core.E(bus.CodeConflict, "identity %q "+HoldsKeysText, name)
			}
			return tx.MarkHuman(ctx, name)
		})
		if errors.Is(err, store.ErrConflict) && attempt < maxAttempts {
			continue
		}
		break
	}
	return core.FromStore(err)
}
