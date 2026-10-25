package identities

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/store"
)

// ErrHoldsKeys is what RegisterLocalHuman's holds-keys conflict matches under
// errors.Is, so a caller can tell it from a conflict left by exhausted reruns
// without reading message text. The error is still a conflict *bus.Error.
var ErrHoldsKeys = errors.New("identity holds keys, so it cannot be a human")

// holdsKeysError is the conflict *bus.Error that also matches ErrHoldsKeys.
type holdsKeysError struct{ be *bus.Error }

func (e holdsKeysError) Error() string        { return e.be.Error() }
func (e holdsKeysError) Unwrap() error        { return e.be }
func (e holdsKeysError) Is(target error) bool { return target == ErrHoldsKeys }

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
				return holdsKeysError{core.E(bus.CodeConflict, "identity %q holds keys, so it cannot be a human", name)}
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

// HumanSignIn is the outcome of SignInHuman: the identity the claims resolve
// to, its freshly evaluated admin flag, and whether this call created it.
type HumanSignIn struct {
	Identity       string
	Admin, Created bool
}

// SignInHuman resolves the verified claims of a corporate sign-in to a human
// identity (spec 4.2, 6.2; Decision 13): the users gate, the binding checks,
// creation or subject recording, and the admins evaluation that writes the
// admin flag. The rule order is the plan's. It reruns the transaction on
// ErrConflict, up to maxAttempts.
//
// State (Definition of Done D): a second sign-in with the same claims
// changes nothing (the admin flag is re-evaluated and written only when it
// changes); two overlapping first sign-ins end with one identity and one
// binding (the loser reruns and finds it); a failed run writes nothing, since
// everything is one transaction.
func (s Service) SignInHuman(ctx context.Context, c HumanClaims) (HumanSignIn, error) {
	email, err := NormalizeEmail(c.Email)
	if err != nil {
		return HumanSignIn{}, core.E(bus.CodeUnauthenticated, "email claim is not a valid identity name")
	}
	c.Email = email
	if c.Subject == "" || c.Issuer == "" {
		return HumanSignIn{}, core.E(bus.CodeUnauthenticated, "subject and issuer claims are required")
	}
	if err := (channels.Service{Core: s.Core}).EnsureGeneral(ctx); err != nil {
		return HumanSignIn{}, err
	}
	var res HumanSignIn
	for attempt := 1; ; attempt++ {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			res = HumanSignIn{}
			return signInHumanTx(ctx, tx, c, s.NowMs(), &res)
		})
		if errors.Is(err, store.ErrConflict) && attempt < maxAttempts {
			continue
		}
		break
	}
	if err != nil {
		return HumanSignIn{}, core.FromStore(err)
	}
	return res, nil
}

func signInHumanTx(ctx context.Context, tx store.Tx, c HumanClaims, now int64, res *HumanSignIn) error {
	// (2) the bindings the claims name.
	byEmail, err := tx.HumanBindingByValue(ctx, store.ClaimEmail, c.Email)
	hasEmail := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	bySub, err := tx.HumanBindingBySubject(ctx, c.Issuer, c.Subject)
	hasSub := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	identity := c.Email
	if hasEmail {
		identity = byEmail.Identity
	}
	// (3) the users gate; a miss writes nothing.
	users, err := tx.ListReferences(ctx, store.ListUsers)
	if err != nil {
		return err
	}
	if !MatchReferences(users, identity, c) {
		return core.E(bus.CodeForbidden, "access denied: no users entry matches")
	}
	// (4) the subject and email must agree with what is recorded.
	if hasSub && (!hasEmail || bySub.Identity != byEmail.Identity) {
		return core.E(bus.CodeUnauthenticated, "subject bound to identity %q", bySub.Identity)
	}
	if hasEmail && byEmail.ProviderSubject != "" && (byEmail.ProviderSubject != c.Subject || byEmail.Issuer != c.Issuer) {
		return core.E(bus.CodeUnauthenticated, "identity bound to a different subject")
	}
	switch {
	case !hasEmail:
		// (5) the name is taken by an identity without a binding.
		if _, err := tx.Identity(ctx, identity); err == nil {
			return core.E(bus.CodeConflict, "identity %q is an agent", identity)
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		// (6)
		b := store.HumanBinding{Identity: identity, Issuer: c.Issuer, ClaimType: store.ClaimEmail, Value: c.Email, ProviderSubject: c.Subject}
		if err := createHumanWithGeneral(ctx, tx, b, now); err != nil {
			return err
		}
		res.Created = true
	case byEmail.ProviderSubject == "":
		if err := tx.SetHumanSubject(ctx, identity, c.Issuer, c.Subject); err != nil {
			return err
		}
	}
	// (7)
	admins, err := tx.ListReferences(ctx, store.ListAdmins)
	if err != nil {
		return err
	}
	res.Identity = identity
	res.Admin = MatchReferences(admins, identity, c)
	// Write the flag only when it changes, so a repeated sign-in writes nothing.
	cur, err := tx.Identity(ctx, identity)
	if err != nil {
		return err
	}
	if cur.Admin == res.Admin {
		return nil
	}
	return tx.SetAdmin(ctx, identity, res.Admin)
}
