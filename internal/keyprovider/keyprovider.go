// Package keyprovider is the port between the server's token signing key set
// and wherever a private key lives (signing-keys plan, issues #6 and #8;
// Eric 2026-10-27). The Store-row provider keeps the key in the database; an
// external provider (a KMS) keeps it out of the process and only signs.
// The package imports the standard library and internal/auth/keys only.
package keyprovider

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

// Key is one signing key a provider names: the public half, its thumbprint
// kid and where the private half lives.
type Key struct {
	KID       string
	Algorithm keys.Algorithm
	Public    crypto.PublicKey
	// Ref names the key inside an external provider (a key URI); "" for the
	// store provider.
	Ref string
	// PrivatePEM is the PKCS#8 PEM of the private key; the store provider
	// only.
	PrivatePEM []byte
}

// Signer signs a digest under a context. It satisfies token.ContextSigner
// structurally: like crypto.Signer it returns DER for ES256 and the raw
// signature for EdDSA.
type Signer interface {
	Public() crypto.PublicKey
	SignContext(ctx context.Context, digest []byte, opts crypto.SignerOpts) ([]byte, error)
}

// Provider produces and opens signing keys.
type Provider interface {
	// Name is the provider's store.SigningProviders value.
	Name() string
	// Next returns the key a rotation would install: a freshly generated one
	// (store) or the one the configured key URI resolves to (external). It
	// may call the network, so callers run it outside every Store
	// transaction.
	Next(ctx context.Context) (Key, error)
	// Signer opens k for signing. It makes no network call.
	Signer(ctx context.Context, k Key) (Signer, error)
}

// NameStore is the Store-row provider's name.
const NameStore = "store"

// StoreProvider keeps the private key in the signing_keys row.
type StoreProvider struct{}

// Name implements Provider.
func (StoreProvider) Name() string { return NameStore }

// Next generates an EdDSA key.
func (StoreProvider) Next(context.Context) (Key, error) {
	gen, err := keys.Generate(keys.EdDSA)
	if err != nil {
		return Key{}, err
	}
	pemBytes, err := keys.EncodePrivatePEM(gen)
	if err != nil {
		return Key{}, err
	}
	kid, err := keys.KID(gen.Public())
	if err != nil {
		return Key{}, err
	}
	return Key{KID: kid, Algorithm: keys.EdDSA, Public: gen.Public(), PrivatePEM: pemBytes}, nil
}

// Signer parses k.PrivatePEM. It refuses a PEM whose key is not k.KID or not
// k.Algorithm.
func (StoreProvider) Signer(_ context.Context, k Key) (Signer, error) {
	sg, alg, err := keys.ParsePrivatePEM(k.PrivatePEM)
	if err != nil {
		return nil, fmt.Errorf("keyprovider: key %q: %w", k.KID, err)
	}
	kid, err := keys.KID(sg.Public())
	if err != nil {
		return nil, fmt.Errorf("keyprovider: key %q: %w", k.KID, err)
	}
	if kid != k.KID {
		return nil, fmt.Errorf("keyprovider: the PEM holds key %q, not %q", kid, k.KID)
	}
	if alg != k.Algorithm {
		return nil, fmt.Errorf("keyprovider: key %q: the PEM is %s, not %s", k.KID, alg, k.Algorithm)
	}
	return storeSigner{sg}, nil
}

type storeSigner struct{ s crypto.Signer }

func (s storeSigner) Public() crypto.PublicKey { return s.s.Public() }

func (s storeSigner) SignContext(ctx context.Context, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.s.Sign(rand.Reader, digest, opts)
}

// Schemes are the key URI schemes of the external providers, in the order the
// error texts name them.
func Schemes() []string { return []string{"awskms", "gcpkms", "azurekms", "hashivault"} }

// SchemeOf returns the provider scheme of a key URI: "" for the empty URI
// (the store provider), else one of Schemes. A missing "://", an empty rest or
// a scheme not in Schemes (the match is case-sensitive) is an error.
func SchemeOf(uri string) (string, error) {
	if uri == "" {
		return "", nil
	}
	scheme, rest, ok := strings.Cut(uri, "://")
	if !ok {
		return "", fmt.Errorf("key URI %q has no scheme: use one of %s", uri, schemeList())
	}
	for _, s := range Schemes() {
		if scheme == s {
			if rest == "" {
				return "", fmt.Errorf("key URI %q names no key after %s://", uri, s)
			}
			return s, nil
		}
	}
	return "", fmt.Errorf("key URI %q has unknown scheme %q: use one of %s", uri, scheme, schemeList())
}

func schemeList() string {
	s := Schemes()
	return strings.Join(s[:len(s)-1], ", ") + " or " + s[len(s)-1]
}

// ExternalKey wraps the public key an external provider reports for ref. Only
// ECDSA P-256 (ES256) is supported, because KMS signing returns a DER
// signature the issuer converts for ES256 alone (decision 1); every other key,
// Ed25519 included, is an error.
func ExternalKey(pub crypto.PublicKey, ref string) (Key, error) {
	if ref == "" {
		return Key{}, errors.New("external signing key has no key URI")
	}
	alg, err := keys.AlgorithmOf(pub)
	if err != nil || alg != keys.ES256 {
		return Key{}, fmt.Errorf("external signing key is %s; only ECDSA P-256 (ES256) is supported", describe(pub))
	}
	kid, err := keys.KID(pub)
	if err != nil {
		return Key{}, err
	}
	return Key{KID: kid, Algorithm: keys.ES256, Public: pub, Ref: ref}, nil
}

func describe(pub crypto.PublicKey) string {
	switch k := pub.(type) {
	case ed25519.PublicKey:
		return "Ed25519"
	case *ecdsa.PublicKey:
		if k != nil && k.Curve != nil {
			return "ECDSA " + k.Curve.Params().Name
		}
		return "ECDSA"
	case *rsa.PublicKey:
		return "RSA"
	default:
		return fmt.Sprintf("a %T", pub)
	}
}
