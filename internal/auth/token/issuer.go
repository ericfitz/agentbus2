// Package token is the server side of authentication: the issuer that mints and
// verifies the server's own bearer JWTs (Decisions 57, 70, 84), external
// issuers with JWKS, and the verifier that routes a bearer by its issuer. The
// client assertion jti record is the Store's assertion_jti table. It imports
// only the standard library, go-jose and internal/auth/keys, and is never
// imported by client code.
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
	// ErrUnknownIssuer marks a bearer whose iss is not the own issuer.
	ErrUnknownIssuer = errors.New("token: unknown issuer")
	// ErrNoEmail marks a provider token without a usable email claim; it is
	// wrapped alongside ErrInvalid.
	ErrNoEmail = errors.New("token: no email claim")
	// ErrJWKSUnavailable marks a provider key set that could not be fetched
	// and left no usable key (the provider is down, not the token bad).
	ErrJWKSUnavailable = errors.New("token: issuer key set unavailable")
)

var serverAlgorithms = []jose.SignatureAlgorithm{jose.EdDSA, jose.ES256}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// invalidCause is invalid for a failure with an underlying error. It wraps the
// cause as well as ErrInvalid, so a caller classifies the failure with
// errors.Is (jwt.ErrExpired, jwt.ErrInvalidAudience, jose.ErrCryptoFailure, ...)
// and not by matching go-jose's message text. An empty what leaves the
// description out.
func invalidCause(what string, cause error) error {
	if what == "" {
		return fmt.Errorf("%w: %w", ErrInvalid, cause)
	}
	return fmt.Errorf("%w: %s: %w", ErrInvalid, what, cause)
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
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", kid))
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

// TokenEndpoint is the token endpoint URL, which a client assertion may still
// name as its audience (RFC 7523bis prefers the issuer URL).
func (i *Issuer) TokenEndpoint() string { return i.url + "/v1/auth/token" }

// MetadataURI is where the RFC 8414 authorization server metadata is served.
func (i *Issuer) MetadataURI() string { return i.url + "/.well-known/oauth-authorization-server" }

// JWKSURI is where the public key is served.
func (i *Issuer) JWKSURI() string { return i.url + "/v1/auth/jwks" }

// Lifetime is the bearer lifetime.
func (i *Issuer) Lifetime() time.Duration { return i.lifetime }

// Token kinds carried in the KindClaim claim.
const (
	KindAgent = "agent"
	KindHuman = "human"
)

// KindClaim is the private claim naming the kind of the bearer's identity.
const KindClaim = "agentbus2_kind"

// MintInput is what Mint needs. Lifetime 0 means the issuer's lifetime;
// ClientID "" means Identity.
type MintInput struct {
	Identity, Kind, ClientID string
	Lifetime                 time.Duration
}

// Mint issues an RFC 9068 access token: typ at+jwt, iss = aud = URL,
// sub = identity, client_id, jti, iat, exp and the agentbus2_kind claim.
func (i *Issuer) Mint(in MintInput) (string, time.Time, error) {
	if in.Identity == "" {
		return "", time.Time{}, errors.New("token: identity is required")
	}
	if in.Kind != KindAgent && in.Kind != KindHuman {
		return "", time.Time{}, fmt.Errorf("token: unknown kind %q", in.Kind)
	}
	if in.Lifetime < 0 {
		return "", time.Time{}, errors.New("token: lifetime must not be negative")
	}
	life := in.Lifetime
	if life == 0 {
		life = i.lifetime
	}
	clientID := in.ClientID
	if clientID == "" {
		clientID = in.Identity
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("token: jti: %w", err)
	}
	now := i.now()
	exp := now.Add(life)
	c := jwt.Claims{
		Issuer:   i.url,
		Subject:  in.Identity,
		Audience: jwt.Audience{i.url},
		ID:       base64.RawURLEncoding.EncodeToString(rnd[:]),
		IssuedAt: jwt.NewNumericDate(now),
		Expiry:   jwt.NewNumericDate(exp),
	}
	extra := struct {
		ClientID string `json:"client_id"`
		Kind     string `json:"agentbus2_kind"`
	}{clientID, in.Kind}
	raw, err := jwt.Signed(i.signer).Claims(c).Claims(extra).Serialize()
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
		return "", invalidCause("", err)
	}
	if len(tok.Headers) != 1 {
		return "", invalid("expected one signature")
	}
	if tok.Headers[0].KeyID != i.kid {
		return "", ErrUnknownKey
	}
	if !isAccessTokenType(tok.Headers[0]) {
		return "", invalid("typ must be at+jwt")
	}
	var c jwt.Claims
	if err := tok.Claims(i.key.Public(), &c); err != nil {
		return "", invalidCause("signature", err)
	}
	if err := c.ValidateWithLeeway(jwt.Expected{Issuer: i.url, AnyAudience: jwt.Audience{i.url}, Time: now}, Leeway); err != nil {
		return "", invalidCause("", err)
	}
	if c.Expiry == nil {
		return "", invalid("exp is required")
	}
	if c.Subject == "" {
		return "", invalid("sub is required")
	}
	return c.Subject, nil
}

// isAccessTokenType reports whether the header's typ is at+jwt or
// application/at+jwt, compared case-insensitively (RFC 9068 section 2.1).
func isAccessTokenType(h jose.Header) bool {
	typ, _ := h.ExtraHeaders[jose.HeaderType].(string)
	typ = strings.ToLower(typ)
	return typ == "at+jwt" || typ == "application/at+jwt"
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
