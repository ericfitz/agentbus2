package token

import (
	"context"
	"crypto"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

// The issuer's timing constants (signing-keys plan, Task 2).
const (
	// ReloadInterval is how long the cached key set serves before the next
	// access reloads it.
	ReloadInterval = 30 * time.Second
	// UnknownKeyReloadInterval is the least time between two reloads forced by
	// a bearer whose kid is not in the cached set.
	UnknownKeyReloadInterval = 5 * time.Second
	// SignTimeout bounds one signature.
	SignTimeout = 10 * time.Second
)

// ErrSignerUnavailable marks a Mint that could not sign: the key set has no
// usable signer, or the signer failed or timed out. Verification is not
// affected.
var ErrSignerUnavailable = errors.New("token: signing key unavailable")

// Key is one published public key of the issuer.
type Key struct {
	// KID is the RFC 7638 thumbprint of Public (keys.KID).
	KID       string
	Algorithm keys.Algorithm
	Public    crypto.PublicKey
}

// ContextSigner signs a digest under a context. Like crypto.Signer, it returns
// DER for ES256 and the raw signature for EdDSA.
type ContextSigner interface {
	Public() crypto.PublicKey
	SignContext(ctx context.Context, digest []byte, opts crypto.SignerOpts) ([]byte, error)
}

// KeySet is what a KeySource reports.
type KeySet struct {
	// Active is the key Signer signs with (a keyring may report the last
	// usable retiring key here when no active key is usable).
	Active Key
	// Signer is nil when no key is usable: Mint then fails with
	// ErrSignerUnavailable while Verify and JWKS keep working.
	Signer ContextSigner
	// Keys is every published key, Active included, in the order JWKS lists them.
	Keys []Key
}

// KeySource loads the issuer's key set.
type KeySource interface {
	LoadKeys(ctx context.Context) (KeySet, error)
}

type staticKeys struct{ s crypto.Signer }

// StaticKeys is a KeySource of one active key that never changes.
func StaticKeys(s crypto.Signer) KeySource { return staticKeys{s} }

func (k staticKeys) LoadKeys(context.Context) (KeySet, error) {
	if k.s == nil {
		return KeySet{}, errors.New("token: signing key is required")
	}
	key, err := keyOf(k.s.Public())
	if err != nil {
		return KeySet{}, err
	}
	return KeySet{Active: key, Signer: cryptoSigner{k.s}, Keys: []Key{key}}, nil
}

type cryptoSigner struct{ crypto.Signer }

func (c cryptoSigner) SignContext(_ context.Context, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return c.Sign(rand.Reader, digest, opts)
}

func keyOf(pub crypto.PublicKey) (Key, error) {
	alg, err := keys.AlgorithmOf(pub)
	if err != nil {
		return Key{}, err
	}
	kid, err := keys.KID(pub)
	if err != nil {
		return Key{}, err
	}
	return Key{KID: kid, Algorithm: alg, Public: pub}, nil
}

// validate admits a key set: every key's kid is the thumbprint of its public
// key and its algorithm the key's, kids are distinct, Active is one of Keys,
// and a non-nil Signer holds Active's key.
func (s KeySet) validate() error {
	if len(s.Keys) == 0 {
		return errors.New("token: key set has no keys")
	}
	seen := map[string]bool{}
	for _, k := range s.Keys {
		if err := checkKey(k); err != nil {
			return err
		}
		if seen[k.KID] {
			return fmt.Errorf("token: key set lists kid %q twice", k.KID)
		}
		seen[k.KID] = true
	}
	if err := checkKey(s.Active); err != nil {
		return fmt.Errorf("active key: %w", err)
	}
	if !seen[s.Active.KID] {
		return fmt.Errorf("token: active kid %q is not in the key set", s.Active.KID)
	}
	if s.Signer != nil {
		kid, err := keys.KID(s.Signer.Public())
		if err != nil {
			return fmt.Errorf("token: signer key: %w", err)
		}
		if kid != s.Active.KID {
			return fmt.Errorf("token: signer holds key %q, not the active %q", kid, s.Active.KID)
		}
	}
	return nil
}

func checkKey(k Key) error {
	alg, err := keys.AlgorithmOf(k.Public)
	if err != nil {
		return fmt.Errorf("token: key %q: %w", k.KID, err)
	}
	if alg != k.Algorithm {
		return fmt.Errorf("token: key %q: algorithm %q, but the key is %q", k.KID, k.Algorithm, alg)
	}
	kid, err := keys.KID(k.Public)
	if err != nil {
		return fmt.Errorf("token: key %q: %w", k.KID, err)
	}
	if kid != k.KID {
		return fmt.Errorf("token: key %q is not its thumbprint %q", k.KID, kid)
	}
	return nil
}

// ctxSigner adapts a ContextSigner to the crypto.Signer go-jose's opaque
// signer needs, signing under ctx bounded by SignTimeout.
type ctxSigner struct {
	ctx context.Context //nolint:containedctx // one signature's context, built per Mint
	s   ContextSigner
}

func (c ctxSigner) Public() crypto.PublicKey { return c.s.Public() }

func (c ctxSigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return c.s.SignContext(c.ctx, digest, opts)
}
