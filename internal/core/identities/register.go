package identities

import (
	"context"
	"crypto"
	"errors"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/store"
)

// RegisterInput is what a client presents to register.
type RegisterInput struct {
	Identity        string
	Key             crypto.PublicKey
	EnrollmentToken string
}

// RegisterResult is the outcome of Register.
type RegisterResult struct {
	Identity  string
	KID       string
	Algorithm keys.Algorithm
	// Created is true when this call created the identity row.
	Created bool
}

const errInvalidToken = "enrollment token is invalid"

// Register binds a public key to an identity under the listener's mode
// (trust on first use, enrollment tokens; Decisions 20, 58, 72, 77, 92).
// The rule order is the plan's choice 8.
//
// State (Definition of Done D): the same key again changes nothing and
// consumes nothing (a client whose response was lost retries safely); two
// concurrent registers of one name end with one key, the loser answering
// conflict or, for the same key, success; a failed run leaves nothing, since
// everything is one transaction.
func (s Service) Register(ctx context.Context, mode Mode, in RegisterInput) (RegisterResult, error) {
	switch mode {
	case ModeOpen, ModeEnrollment:
	case ModeClosed:
		return RegisterResult{}, core.E(bus.CodeForbidden, "registration is closed")
	default:
		panic("identities: Register with invalid mode " + string(mode))
	}
	if in.Key == nil {
		return RegisterResult{}, core.E(bus.CodeValidation, "public key is required")
	}
	alg, err := keys.AlgorithmOf(in.Key)
	if err != nil {
		return RegisterResult{}, core.E(bus.CodeValidation, "%s", err.Error())
	}
	kid, err := keys.KID(in.Key)
	if err != nil {
		return RegisterResult{}, core.E(bus.CodeValidation, "%s", err.Error())
	}
	der, err := keys.MarshalPublic(in.Key)
	if err != nil {
		return RegisterResult{}, core.E(bus.CodeValidation, "%s", err.Error())
	}
	if err := (channels.Service{Core: s.Core}).EnsureGeneral(ctx); err != nil {
		return RegisterResult{}, err
	}
	var res RegisterResult
	for attempt := 1; ; attempt++ {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			res = RegisterResult{Identity: in.Identity, KID: kid, Algorithm: alg}
			return s.registerTx(ctx, tx, mode, in, kid, alg, der, &res)
		})
		if errors.Is(err, store.ErrConflict) && attempt < maxAttempts {
			continue
		}
		break
	}
	if err != nil {
		return RegisterResult{}, core.FromStore(err)
	}
	return res, nil
}

func (s Service) registerTx(ctx context.Context, tx store.Tx, mode Mode, in RegisterInput, kid string, alg keys.Algorithm, der []byte, res *RegisterResult) error {
	now := s.NowMs()
	id, err := tx.Identity(ctx, in.Identity)
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	var existing []store.PublicKey
	if exists {
		if existing, err = tx.PublicKeys(ctx, in.Identity); err != nil {
			return err
		}
		// (a) the idempotent short-circuit: consumes nothing.
		for _, k := range existing {
			if k.KID == kid {
				return nil
			}
		}
		// (a2) a keyless administrator row needs an enrollment token.
		if id.Admin && len(existing) == 0 && in.EnrollmentToken == "" {
			return core.E(bus.CodeForbidden, "identity %q requires an enrollment token", in.Identity)
		}
	}
	// (b)
	if mode == ModeEnrollment && in.EnrollmentToken == "" {
		return core.E(bus.CodeForbidden, "registration requires an enrollment token")
	}
	// (c)
	var tok store.EnrollmentToken
	if in.EnrollmentToken != "" {
		tok, err = tx.EnrollmentTokenByHash(ctx, HashToken(in.EnrollmentToken))
		if errors.Is(err, store.ErrNotFound) {
			return core.E(bus.CodeForbidden, "%s", errInvalidToken)
		}
		if err != nil {
			return err
		}
		if tok.ExpiresAt <= now || tok.UsesLeft <= 0 || (tok.Identity != "" && tok.Identity != in.Identity) {
			return core.E(bus.CodeForbidden, "%s", errInvalidToken)
		}
		// (c2) an unbound token claims free names only: a keyless
		// administrator row (for example after reset-credential) needs a
		// token bound to it (post-build decision 1). Nothing is consumed.
		if exists && id.Admin && len(existing) == 0 && tok.Identity != in.Identity {
			return core.E(bus.CodeForbidden, "identity %q requires an enrollment token bound to it", in.Identity)
		}
	}
	// (d)
	if !exists {
		if err := createWithGeneral(ctx, tx, in.Identity, now); err != nil {
			return err
		}
		res.Created = true
	}
	// (e)
	if len(existing) > 0 {
		return core.E(bus.CodeConflict, "identity %q already has a credential", in.Identity)
	}
	if err := tx.PutPublicKey(ctx, store.PublicKey{Identity: in.Identity, KID: kid, Algorithm: string(alg), Key: der, CreatedAt: now}); err != nil {
		return err
	}
	// (f)
	if in.EnrollmentToken != "" {
		ok, err := tx.ConsumeEnrollmentToken(ctx, tok.ID, now)
		if err != nil {
			return err
		}
		if !ok {
			return core.E(bus.CodeForbidden, "%s", errInvalidToken)
		}
	}
	return nil
}

// createWithGeneral creates the identity row and subscribes it to general at
// the current MaxSeq (Decisions 59, 77, 92). The Store refuses a bad name
// with ErrInvalid.
func createWithGeneral(ctx context.Context, tx store.Tx, name string, now int64) error {
	if err := tx.CreateIdentity(ctx, name, now); err != nil {
		return err
	}
	gen, err := tx.ChannelByName(ctx, channels.GeneralChannel)
	if err != nil {
		return err
	}
	seq, err := tx.MaxSeq(ctx)
	if err != nil {
		return err
	}
	return tx.Subscribe(ctx, name, gen.ID, seq, now)
}

// RegisterLocal creates a keyless identity row with its general
// subscription (Decision 36); idempotent.
func (s Service) RegisterLocal(ctx context.Context, name string) error {
	_, err := s.CreateIdentity(ctx, name)
	return err
}

// CreateIdentity is RegisterLocal's rule returning whether the row was
// created. Nothing here sets the administrator flag.
func (s Service) CreateIdentity(ctx context.Context, name string) (bool, error) {
	if err := (channels.Service{Core: s.Core}).EnsureGeneral(ctx); err != nil {
		return false, err
	}
	var created bool
	var err error
	for attempt := 1; ; attempt++ {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			created = false
			if _, err := tx.Identity(ctx, name); err == nil {
				return nil
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err := createWithGeneral(ctx, tx, name, s.NowMs()); err != nil {
				return err
			}
			created = true
			return nil
		})
		if errors.Is(err, store.ErrConflict) && attempt < maxAttempts {
			continue
		}
		break
	}
	if err != nil {
		return false, core.FromStore(err)
	}
	return created, nil
}

// EnsureIdentity is CreateIdentity behind a read: a name that has a row costs
// a read snapshot and no write transaction, so Bearer.AutoCreate on every
// externally authenticated request in open mode does not queue behind the
// writer lock. There is no cache, so a row deleted later (reset, registry
// reset) is created again on the next call. Overlapping callers that all
// miss the read fall through to CreateIdentity, which is idempotent and
// reruns on a conflict.
func (s Service) EnsureIdentity(ctx context.Context, name string) error {
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.Identity(ctx, name)
		return err
	})
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, store.ErrNotFound):
		return core.FromStore(err)
	}
	_, err = s.CreateIdentity(ctx, name)
	return err
}
