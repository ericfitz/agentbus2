// Package token is the server side of authentication: the issuer that mints and
// verifies the server's own bearer JWTs (Decisions 57, 70, 84), the jti replay
// set for client assertions, external issuers with JWKS, and the verifier that
// routes a bearer by its issuer. It imports only the standard library, go-jose
// and internal/auth/keys, and is never imported by client code.
package token

import (
	"crypto"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/cryptosigner"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

// Leeway is the clock skew tolerated on every claim check (Milestone 5 choice 5).
const Leeway = 60 * time.Second

var (
	// ErrInvalid marks a bearer that fails verification.
	ErrInvalid = errors.New("token: invalid")
	// ErrUnknownKey marks a bearer whose kid names no key of its issuer.
	ErrUnknownKey = errors.New("token: unknown key")
	// ErrUnknownIssuer marks a bearer whose iss is neither the own issuer nor a configured one.
	ErrUnknownIssuer = errors.New("token: unknown issuer")
)

var serverAlgorithms = []jose.SignatureAlgorithm{jose.EdDSA, jose.ES256}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Issuer mints and verifies the server's own bearers with one signing key.
type Issuer struct {
	url      string
	key      crypto.Signer
	kid      string
	alg      keys.Algorithm
	lifetime time.Duration
	now      func() time.Time
	signer   jose.Signer
}

// NewIssuer builds an issuer. issuerURL is absolute http(s) with no trailing
// slash, path, query or fragment; lifetime is positive.
func NewIssuer(issuerURL string, key crypto.Signer, lifetime time.Duration, now func() time.Time) (*Issuer, error) {
	u, err := url.Parse(issuerURL)
	if err != nil {
		return nil, fmt.Errorf("token: issuer URL: %w", err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("token: issuer URL %q must be http or https", issuerURL)
	case u.Host == "" || u.User != nil:
		return nil, fmt.Errorf("token: issuer URL %q needs a host and no user info", issuerURL)
	case u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery ||
		strings.ContainsAny(issuerURL, "?#") || strings.HasSuffix(issuerURL, "/"):
		return nil, fmt.Errorf("token: issuer URL %q must have no path, query or fragment", issuerURL)
	}
	if key == nil {
		return nil, errors.New("token: signing key is required")
	}
	if lifetime <= 0 {
		return nil, errors.New("token: lifetime must be positive")
	}
	if now == nil {
		return nil, errors.New("token: clock is required")
	}
	alg, err := keys.AlgorithmOf(key.Public())
	if err != nil {
		return nil, err
	}
	kid, err := keys.KID(key.Public())
	if err != nil {
		return nil, err
	}
	ja := jose.EdDSA
	if alg == keys.ES256 {
		ja = jose.ES256
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: ja, Key: cryptosigner.Opaque(key)},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		return nil, fmt.Errorf("token: signer: %w", err)
	}
	return &Issuer{url: issuerURL, key: key, kid: kid, alg: alg, lifetime: lifetime, now: now, signer: signer}, nil
}

// URL is the issuer identifier (the iss claim).
func (i *Issuer) URL() string { return i.url }

// KID is the thumbprint of the signing key.
func (i *Issuer) KID() string { return i.kid }

// Algorithm is the signing algorithm.
func (i *Issuer) Algorithm() keys.Algorithm { return i.alg }

// TokenEndpoint is the audience of client assertions.
func (i *Issuer) TokenEndpoint() string { return i.url + "/v1/auth/token" }

// JWKSURI is where the public key is served.
func (i *Issuer) JWKSURI() string { return i.url + "/v1/auth/jwks" }

// Lifetime is the bearer lifetime.
func (i *Issuer) Lifetime() time.Duration { return i.lifetime }

// Mint issues a bearer for identity: iss = aud = URL, sub = identity.
func (i *Issuer) Mint(identity string) (string, time.Time, error) {
	if identity == "" {
		return "", time.Time{}, errors.New("token: identity is required")
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("token: jti: %w", err)
	}
	now := i.now()
	exp := now.Add(i.lifetime)
	c := jwt.Claims{
		Issuer:   i.url,
		Subject:  identity,
		Audience: jwt.Audience{i.url},
		ID:       base64.RawURLEncoding.EncodeToString(rnd[:]),
		IssuedAt: jwt.NewNumericDate(now),
		Expiry:   jwt.NewNumericDate(exp),
	}
	raw, err := jwt.Signed(i.signer).Claims(c).Serialize()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token: sign: %w", err)
	}
	return raw, exp, nil
}

// Verify checks a bearer minted by this issuer and returns its identity.
func (i *Issuer) Verify(raw string, now time.Time) (string, error) {
	if strings.Count(raw, ".") != 2 {
		return "", invalid("not a compact JWS")
	}
	tok, err := jwt.ParseSigned(raw, serverAlgorithms)
	if err != nil {
		return "", invalid("%v", err)
	}
	if len(tok.Headers) != 1 {
		return "", invalid("expected one signature")
	}
	if tok.Headers[0].KeyID != i.kid {
		return "", ErrUnknownKey
	}
	var c jwt.Claims
	if err := tok.Claims(i.key.Public(), &c); err != nil {
		return "", invalid("signature: %v", err)
	}
	if err := c.ValidateWithLeeway(jwt.Expected{Issuer: i.url, AnyAudience: jwt.Audience{i.url}, Time: now}, Leeway); err != nil {
		return "", invalid("%v", err)
	}
	if c.Expiry == nil {
		return "", invalid("exp is required")
	}
	if c.Subject == "" {
		return "", invalid("sub is required")
	}
	return c.Subject, nil
}

// JWKS is the public key set served at JWKSURI.
func (i *Issuer) JWKS() jose.JSONWebKeySet {
	j, err := keys.ToJWK(i.key.Public(), i.kid)
	if err != nil {
		// NewIssuer admitted the key, so this cannot happen.
		return jose.JSONWebKeySet{}
	}
	return jose.JSONWebKeySet{Keys: []jose.JSONWebKey{j}}
}
