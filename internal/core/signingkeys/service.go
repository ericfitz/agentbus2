// Package signingkeys holds the rules of the server's token signing key set
// (issues #6 and #8; Eric 2026-10-27): the first-start key, rotation with its
// cooldown, the removal times of retiring keys and the Keyring the issuer
// loads its set from. The host command, the scheduler and the admin API all
// call it.
package signingkeys

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/store"
)

// MaxBearerLifetime is the longest bearer lifetime the server admits; a
// retiring key stays published this long after the last bearer it can have
// signed (the token_lifetime_seconds ceiling).
const MaxBearerLifetime = 86400 * time.Second

// RemoveAfter is when a key retired at retiredAt (Unix milliseconds) may be
// removed: the issuer's reload interval, plus the longest bearer, plus the
// verification leeway after it stopped signing.
func RemoveAfter(retiredAt int64) int64 {
	return retiredAt + (token.ReloadInterval + MaxBearerLifetime + token.Leeway).Milliseconds()
}

// Settings are the rotation settings.
type Settings struct {
	// Interval is the age at which the scheduler rotates the active key; 0
	// turns scheduled rotation off.
	Interval time.Duration
	// Cooldown is the least age of the active key before a rotation is
	// allowed (a revoking rotation is exempt); 0 turns the cooldown off.
	Cooldown time.Duration
}

// DefaultSettings are 720 hours between scheduled rotations and a one hour
// cooldown.
func DefaultSettings() Settings {
	return Settings{Interval: 720 * time.Hour, Cooldown: 3600 * time.Second}
}

// The two reasons a rotation answers conflict besides a lost race. Both are
// bus.Error values with code conflict; errors.Is tells them apart.
var (
	// ErrCooldown: the active key is younger than the cooldown.
	ErrCooldown = errors.New("signing key rotation is inside the cooldown")
	// ErrRotateNoChange: the provider's next key is the active key.
	ErrRotateNoChange = errors.New("the provider's next signing key is the active key")
)

// conflict is a conflict bus.Error that also matches a sentinel.
type conflict struct {
	be   *bus.Error
	kind error
}

func (c *conflict) Error() string   { return c.be.Error() }
func (c *conflict) Unwrap() []error { return []error{c.be, c.kind} }

// Service is the signing key rules over a Core. Provider is the configured
// key provider.
type Service struct {
	*core.Core
	Provider keyprovider.Provider
	Settings Settings
	// OnChange runs after a transaction that changed the set commits (the
	// issuer's Invalidate); nil is fine.
	OnChange func()
}

func (s Service) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

func corrupt(kid string) error {
	return core.E(bus.CodeInternal, "signing key row %s is corrupt", kid)
}

// checkActive admits the active row the configured provider holds, or says
// why it is not usable. It compares the row with what it names: for a store
// row the PEM's key against the kid, algorithm and public key columns; for an
// external row the public key column against the kid and ES256.
func (s Service) checkActive(row store.SigningKey) error {
	if row.Provider != s.Provider.Name() {
		return core.E(bus.CodeConflict,
			"active signing key %s is provider %s, configured provider is %s: run agentbus2 server admin signing-key rotate with the new configuration",
			row.KID, row.Provider, s.Provider.Name())
	}
	return validateRow(row)
}

// validateRow checks a row's columns against each other.
func validateRow(row store.SigningKey) error {
	pub, alg, err := keys.ParsePublic(row.PublicKey)
	if err != nil {
		return corrupt(row.KID)
	}
	if kid, kerr := keys.KID(pub); kerr != nil || kid != row.KID || alg != keys.Algorithm(row.Algorithm) {
		return corrupt(row.KID)
	}
	if row.Provider == store.SigningProviderStore {
		sg, a, perr := keys.ParsePrivatePEM([]byte(row.PrivateKeyPEM))
		if perr != nil || a != alg {
			return corrupt(row.KID)
		}
		der, merr := keys.MarshalPublic(sg.Public())
		if merr != nil || !bytes.Equal(der, row.PublicKey) {
			return corrupt(row.KID)
		}
		return nil
	}
	if alg != keys.ES256 {
		return corrupt(row.KID)
	}
	return nil
}

// Prepared is a key a provider produced for a rotation or a first start,
// ready to insert. Only Prepare and PrepareRotation make one.
type Prepared struct {
	provider string
	key      keyprovider.Key
}

// KID is the prepared key's thumbprint.
func (p Prepared) KID() string { return p.key.KID }

// PrepareRotation asks the provider for the next key. It runs outside every
// transaction (Decision 26) because it may call the network or generate a key;
// nothing it returns is stored until ApplyRotation or RotatePrepared inserts
// it, so a failure or a lost race leaves the set untouched.
func (s Service) PrepareRotation(ctx context.Context) (Prepared, error) {
	k, err := s.Provider.Next(ctx)
	if err != nil {
		return Prepared{}, core.E(bus.CodeInternal, "signing key provider %s: %v", s.Provider.Name(), err)
	}
	kid, err := keys.KID(k.Public)
	if err != nil || kid != k.KID {
		return Prepared{}, core.E(bus.CodeInternal, "signing key provider %s returned key %q that is not the thumbprint of its public key", s.Provider.Name(), k.KID)
	}
	if alg, err := keys.AlgorithmOf(k.Public); err != nil || alg != k.Algorithm {
		return Prepared{}, core.E(bus.CodeInternal, "signing key provider %s returned key %q whose algorithm is not %s", s.Provider.Name(), k.KID, k.Algorithm)
	}
	return Prepared{provider: s.Provider.Name(), key: k}, nil
}

func (p Prepared) row(now int64) (store.SigningKey, error) {
	pub, err := keys.MarshalPublic(p.key.Public)
	if err != nil {
		return store.SigningKey{}, core.E(bus.CodeInternal, "signing key %s: %v", p.key.KID, err)
	}
	return store.SigningKey{
		KID: p.key.KID, State: store.SigningKeyActive, Provider: p.provider, ProviderRef: p.key.Ref,
		Algorithm: string(p.key.Algorithm), PublicKey: pub, PrivateKeyPEM: string(p.key.PrivatePEM), CreatedAt: now,
	}, nil
}

// rerun reports whether err is a lost race the transaction may rerun after.
func rerun(err error, attempt int) bool {
	return attempt < 2 && (errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound))
}

// EnsureActive loads the active key, creating it when the set has none. It
// reports whether this call created it. The active row's provider must be the
// configured one, and a store row must be intact (PEM, kid, algorithm and
// public key agree); a row that is not is refused and never overwritten.
//
// State (Definition of Done D): a second call reads the row and calls no
// Provider.Next; two callers at once end with one active row (the partial
// unique index; the loser reruns once and loads the winner's key, created
// false); a call that fails after Next left nothing, and a crash before the
// insert leaves nothing.
func (s Service) EnsureActive(ctx context.Context) (store.SigningKey, bool, error) {
	var active *store.SigningKey
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		active = nil
		rows, err := tx.ListSigningKeys(ctx)
		if err != nil {
			return err
		}
		if len(rows) > 0 && rows[0].State == store.SigningKeyActive {
			active = &rows[0]
		}
		return nil
	})
	if err != nil {
		return store.SigningKey{}, false, core.FromStore(err)
	}
	if active != nil {
		if err := s.checkActive(*active); err != nil {
			return store.SigningKey{}, false, err
		}
		return *active, false, nil
	}
	p, err := s.PrepareRotation(ctx)
	if err != nil {
		return store.SigningKey{}, false, err
	}
	var (
		out     store.SigningKey
		created bool
	)
	for attempt := 1; ; attempt++ {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			out, created = store.SigningKey{}, false
			rows, err := tx.ListSigningKeys(ctx)
			if err != nil {
				return err
			}
			if len(rows) > 0 && rows[0].State == store.SigningKeyActive {
				if err := s.checkActive(rows[0]); err != nil {
					return err
				}
				out = rows[0]
				return nil
			}
			if err := refuseKnownKID(rows, p.key.KID); err != nil {
				return err
			}
			row, err := p.row(s.NowMs())
			if err != nil {
				return err
			}
			if err := tx.PutSigningKey(ctx, row); err != nil {
				return err
			}
			out, created = row, true
			return nil
		})
		if errors.Is(err, store.ErrConflict) && attempt < 2 {
			continue
		}
		break
	}
	if err != nil {
		return store.SigningKey{}, false, core.FromStore(err)
	}
	if created {
		s.changed()
	}
	return out, created, nil
}

// refuseKnownKID refuses a key the set already holds.
func refuseKnownKID(rows []store.SigningKey, kid string) error {
	for _, r := range rows {
		if r.KID == kid {
			return core.E(bus.CodeConflict, "the key %s is already in the signing key set (%s): point the provider at a new key", kid, r.State)
		}
	}
	return nil
}

// RotateInput selects the kind of rotation.
type RotateInput struct {
	// RevokePrevious deletes the previous active key instead of retiring it,
	// so bearers it signed stop verifying at once, and skips the cooldown.
	RevokePrevious bool
}

// RotateResult reports a rotation.
type RotateResult struct {
	// Active is the new active row. Previous is the row that was active (as
	// it was before the rotation: for a revoke, the deleted row); the zero
	// value when there was none. Revoked is true when Previous was deleted.
	Active, Previous store.SigningKey
	Revoked          bool
}

// ApplyRotation is the transaction body of a rotation: it lists the set,
// refuses a next key that is already in it or a rotation inside the
// cooldown, retires (or, for RevokePrevious, deletes) the active row and
// inserts the prepared key as the new active row. It has no side effects
// beyond tx, so a caller may run it again after a lost race; it returns the
// Store's ErrConflict or ErrNotFound unchanged for that (see RotatePrepared).
// It does not call OnChange: the caller does after the transaction commits.
// It does not check who may rotate: the host command and the scheduler are
// trusted, the admin API checks first.
func (s Service) ApplyRotation(ctx context.Context, tx store.Tx, p Prepared, in RotateInput) (RotateResult, error) {
	rows, err := tx.ListSigningKeys(ctx)
	if err != nil {
		return RotateResult{}, err
	}
	now := s.NowMs()
	var res RotateResult
	if len(rows) > 0 && rows[0].State == store.SigningKeyActive {
		act := rows[0]
		if act.KID == p.key.KID {
			return RotateResult{}, &conflict{core.E(bus.CodeConflict,
				"the key URI resolves to the active signing key %s: point it at a new key, then rotate", act.KID), ErrRotateNoChange}
		}
		if !in.RevokePrevious && act.CreatedAt+s.Settings.Cooldown.Milliseconds() > now {
			next := act.CreatedAt + s.Settings.Cooldown.Milliseconds()
			return RotateResult{}, &conflict{core.E(bus.CodeConflict,
				"the active signing key %s was created at %s; the next rotation is allowed at %s (signing_key_rotation_cooldown_seconds %d; -revoke-previous is exempt)",
				act.KID, rfc3339(act.CreatedAt), rfc3339(next), int64(s.Settings.Cooldown/time.Second)), ErrCooldown}
		}
		if err := refuseKnownKID(rows, p.key.KID); err != nil {
			return RotateResult{}, err
		}
		res.Previous = act
		if in.RevokePrevious {
			if err := tx.DeleteSigningKey(ctx, act.KID); err != nil {
				return RotateResult{}, err
			}
			res.Revoked = true
		} else if err := tx.RetireSigningKey(ctx, act.KID, now, RemoveAfter(now)); err != nil {
			return RotateResult{}, err
		}
	} else if err := refuseKnownKID(rows, p.key.KID); err != nil {
		return RotateResult{}, err
	}
	row, err := p.row(now)
	if err != nil {
		return RotateResult{}, err
	}
	if err := tx.PutSigningKey(ctx, row); err != nil {
		return RotateResult{}, err
	}
	res.Active = row
	return res, nil
}

func rfc3339(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339) }

// RotatePrepared runs ApplyRotation in one transaction, rerunning it once
// after a lost race (a concurrent rotation or removal; Postgres retries its
// own serialization failures first), then calls OnChange.
func (s Service) RotatePrepared(ctx context.Context, p Prepared, in RotateInput) (RotateResult, error) {
	var (
		res RotateResult
		err error
	)
	for attempt := 1; ; attempt++ {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			var e error
			res, e = s.ApplyRotation(ctx, tx, p, in)
			return e
		})
		if rerun(err, attempt) {
			continue
		}
		break
	}
	if err != nil {
		return RotateResult{}, core.FromStore(err)
	}
	s.changed()
	return res, nil
}

// Rotate prepares the provider's next key and applies the rotation.
//
// State (Definition of Done D): the previous active key is retiring, with
// RemoveAfter(now), after one successful rotation. A second Rotate inside the
// cooldown answers conflict (ErrCooldown) and changes nothing; with a provider
// whose Next returns the same key it answers ErrRotateNoChange. Two at once
// after the cooldown: one wins, the other reruns and answers the cooldown
// conflict (two RevokePrevious both succeed, the second deleting the first's
// key). A failure after Next, before the commit, leaves the rows as they were;
// the unused key is discarded (a store key was never persisted).
func (s Service) Rotate(ctx context.Context, in RotateInput) (RotateResult, error) {
	p, err := s.PrepareRotation(ctx)
	if err != nil {
		return RotateResult{}, err
	}
	return s.RotatePrepared(ctx, p, in)
}

// List returns the key set, the active row first. The private key is never
// part of it.
func (s Service) List(ctx context.Context) ([]store.SigningKey, error) {
	var rows []store.SigningKey
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var e error
		rows, e = tx.ListSigningKeys(ctx)
		return e
	})
	if err != nil {
		return nil, core.FromStore(err)
	}
	for i := range rows {
		rows[i].PrivateKeyPEM = ""
	}
	return rows, nil
}

var _ token.KeySource = (*Keyring)(nil)
