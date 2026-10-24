// Package assertion builds and checks RFC 7523 client assertions: a JWT signed
// by the identity's own key, with iss = sub = the identity and aud = the issuer
// URL (Decisions 19 and 70; draft-ietf-oauth-rfc7523bis). It ships in the client binaries and imports
// only the standard library, go-jose and internal/auth/keys.
package assertion

import (
	"crypto"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/cryptosigner"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

// Type is the client_assertion_type of RFC 7523 section 2.2.
const Type = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// JWTType is the typ header an assertion carries (draft-ietf-oauth-rfc7523bis).
const JWTType = "client-authentication+jwt"

// MaxLifetime bounds an assertion's lifetime (Milestone 5 choice 4).
const MaxLifetime = 5 * time.Minute

var (
	// ErrMalformed marks an assertion that is not shaped as required.
	ErrMalformed = errors.New("assertion: malformed")
	// ErrInvalid marks a well-formed assertion that fails verification.
	ErrInvalid = errors.New("assertion: invalid")
)

var allowedAlgorithms = []jose.SignatureAlgorithm{jose.EdDSA, jose.ES256}

// Input is what Sign needs.
type Input struct {
	Identity, Audience string
	Key                crypto.Signer
	KID                string
	Now                time.Time
	Lifetime           time.Duration // 0: MaxLifetime; above it: error
}

func joseAlg(a keys.Algorithm) jose.SignatureAlgorithm {
	if a == keys.EdDSA {
		return jose.EdDSA
	}
	return jose.ES256
}

// Sign builds a compact assertion.
func Sign(in Input) (string, error) {
	if in.Identity == "" || in.Audience == "" || in.KID == "" {
		return "", errors.New("assertion: identity, audience and kid are required")
	}
	if in.Key == nil {
		return "", errors.New("assertion: key is required")
	}
	life := in.Lifetime
	if life == 0 {
		life = MaxLifetime
	}
	if life < 0 || life > MaxLifetime {
		return "", fmt.Errorf("assertion: lifetime %s outside (0, %s]", life, MaxLifetime)
	}
	alg, err := keys.AlgorithmOf(in.Key.Public())
	if err != nil {
		return "", err
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("assertion: jti: %w", err)
	}
	opts := (&jose.SignerOptions{}).WithType(JWTType).WithHeader("kid", in.KID)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: joseAlg(alg), Key: cryptosigner.Opaque(in.Key)}, opts)
	if err != nil {
		return "", fmt.Errorf("assertion: signer: %w", err)
	}
	claims := jwt.Claims{
		Issuer:   in.Identity,
		Subject:  in.Identity,
		Audience: jwt.Audience{in.Audience},
		ID:       base64.RawURLEncoding.EncodeToString(rnd[:]),
		IssuedAt: jwt.NewNumericDate(in.Now),
		Expiry:   jwt.NewNumericDate(in.Now.Add(life)),
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return "", fmt.Errorf("assertion: sign: %w", err)
	}
	return raw, nil
}

// Parsed is an assertion whose shape has been checked and whose signature has not.
type Parsed struct {
	Identity, KID, JTI string
	Alg                keys.Algorithm
	Expiry             time.Time
	tok                *jwt.JSONWebToken
}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

// malformedCause is malformed for a failure with an underlying error. It wraps
// the cause as well as ErrMalformed, so errors.Is and errors.As still reach go-jose's
// own errors (for example *jose.ErrUnexpectedSignatureAlgorithm) and a caller
// need not match message text. An empty what leaves the description out.
func malformedCause(what string, cause error) error {
	if what == "" {
		return fmt.Errorf("%w: %w", ErrMalformed, cause)
	}
	return fmt.Errorf("%w: %s: %w", ErrMalformed, what, cause)
}

// MaxJTIBytes bounds the jti claim; store.MaxJTIBytes is the same bound on
// the replay row.
const MaxJTIBytes = 256

// Parse checks the algorithm allowlist and the claim shape; it does not verify
// the signature. Use Verify with the key stored under KID.
func Parse(raw string) (Parsed, error) {
	if strings.Count(raw, ".") != 2 {
		return Parsed{}, malformed("not a compact JWS")
	}
	tok, err := jwt.ParseSigned(raw, allowedAlgorithms)
	if err != nil {
		return Parsed{}, malformedCause("", err)
	}
	if len(tok.Headers) != 1 {
		return Parsed{}, malformed("expected one signature")
	}
	h := tok.Headers[0]
	var alg keys.Algorithm
	switch jose.SignatureAlgorithm(h.Algorithm) {
	case jose.EdDSA:
		alg = keys.EdDSA
	case jose.ES256:
		alg = keys.ES256
	default:
		return Parsed{}, malformed("algorithm %q", h.Algorithm)
	}
	switch typ, _ := h.ExtraHeaders[jose.HeaderType].(string); strings.ToLower(typ) {
	case "", "jwt", strings.ToLower(JWTType):
	default:
		return Parsed{}, malformed("typ %q", typ)
	}
	var c jwt.Claims
	if err := tok.UnsafeClaimsWithoutVerification(&c); err != nil {
		return Parsed{}, malformedCause("claims", err)
	}
	switch {
	case c.Issuer == "" || c.Subject == "" || c.Issuer != c.Subject:
		return Parsed{}, malformed("iss and sub must be non-empty and equal")
	case h.KeyID == "":
		return Parsed{}, malformed("kid header is required")
	case c.ID == "":
		return Parsed{}, malformed("jti is required")
	case len(c.ID) > MaxJTIBytes:
		return Parsed{}, malformed("jti is %d bytes, at most %d", len(c.ID), MaxJTIBytes)
	case c.Expiry == nil:
		return Parsed{}, malformed("exp is required")
	}
	return Parsed{Identity: c.Subject, KID: h.KeyID, JTI: c.ID, Alg: alg, Expiry: c.Expiry.Time(), tok: tok}, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// invalidCause is invalid for a failure with an underlying error; see
// malformedCause. errors.Is(err, jwt.ErrExpired), jwt.ErrInvalidAudience and
// jose.ErrCryptoFailure hold alongside ErrInvalid.
func invalidCause(what string, cause error) error {
	if what == "" {
		return fmt.Errorf("%w: %w", ErrInvalid, cause)
	}
	return fmt.Errorf("%w: %s: %w", ErrInvalid, what, cause)
}

// Verify checks the signature against pub and the claims against the
// accepted audiences (any one matches) and the clock, with the given leeway.
func (p Parsed) Verify(pub crypto.PublicKey, audiences []string, now time.Time, leeway time.Duration) error {
	if p.tok == nil {
		return invalid("not parsed")
	}
	if len(audiences) == 0 {
		return invalid("an accepted audience is required")
	}
	for _, a := range audiences {
		if a == "" {
			return invalid("an accepted audience is empty")
		}
	}
	alg, err := keys.AlgorithmOf(pub)
	if err != nil {
		return invalidCause("", err)
	}
	if alg != p.Alg {
		return invalid("key algorithm %s does not match the assertion's %s", alg, p.Alg)
	}
	var c jwt.Claims
	if err := p.tok.Claims(pub, &c); err != nil {
		return invalidCause("signature", err)
	}
	exp := jwt.Expected{Issuer: p.Identity, Subject: p.Identity, AnyAudience: jwt.Audience(audiences), Time: now}
	if err := c.ValidateWithLeeway(exp, leeway); err != nil {
		return invalidCause("", err)
	}
	if c.Expiry == nil {
		return invalid("exp is required")
	}
	if c.Expiry.Time().Sub(now) > MaxLifetime+leeway {
		return invalid("exp too far ahead")
	}
	return nil
}
