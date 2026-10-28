// Package token is the server side of authentication: the issuer that mints and
// verifies the server's own bearer JWTs (Decisions 57, 70, 84), external
// issuers with JWKS, and the verifier that routes a bearer by its issuer. The
// client assertion jti record is the Store's assertion_jti table. It imports
// only the standard library, go-jose and internal/auth/keys, and is never
// imported by client code.
package token

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
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

// keyLoad is one key-set load that other callers may wait on.
type keyLoad struct {
	done chan struct{}
	err  error
}

// Issuer mints and verifies the server's own bearers over a key set that a
// KeySource supplies: it signs with the active key and verifies by kid.
type Issuer struct {
	url      string
	src      KeySource
	lifetime time.Duration
	now      func() time.Time

	mu         sync.Mutex
	set        KeySet
	byKID      map[string]Key
	loadedAt   time.Time
	stale      bool      // Invalidate was called: the next access reloads
	lastForced time.Time // the last reload forced by an unknown kid
	inflight   *keyLoad  // the load in progress, if any
}

// NewIssuer builds an issuer and loads its key set once. issuerURL is absolute
// http(s) with no trailing slash, path, query or fragment; lifetime is
// positive. It fails when the load fails or the set is invalid.
func NewIssuer(ctx context.Context, issuerURL string, src KeySource, lifetime time.Duration, now func() time.Time) (*Issuer, error) {
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
	if src == nil {
		return nil, errors.New("token: key source is required")
	}
	if lifetime <= 0 {
		return nil, errors.New("token: lifetime must be positive")
	}
	if now == nil {
		return nil, errors.New("token: clock is required")
	}
	i := &Issuer{url: issuerURL, src: src, lifetime: lifetime, now: now}
	set, err := i.fetch(ctx)
	if err != nil {
		return nil, err
	}
	i.install(set)
	return i, nil
}

// fetch loads and validates a key set.
func (i *Issuer) fetch(ctx context.Context) (KeySet, error) {
	set, err := i.src.LoadKeys(ctx)
	if err != nil {
		return KeySet{}, fmt.Errorf("token: load keys: %w", err)
	}
	if err := set.validate(); err != nil {
		return KeySet{}, err
	}
	return set, nil
}

// install caches set. The caller holds mu, or owns i exclusively.
func (i *Issuer) install(set KeySet) {
	i.set = set
	i.byKID = make(map[string]Key, len(set.Keys))
	for _, k := range set.Keys {
		i.byKID[k.KID] = k
	}
	i.loadedAt = i.now()
	i.stale = false
}

// startLoad registers a load; the caller holds mu and has seen no inflight.
func (i *Issuer) startLoad() *keyLoad {
	f := &keyLoad{done: make(chan struct{})}
	i.inflight = f
	return f
}

// runLoad loads under a context that outlives the caller's cancellation
// (other callers wait on the result), then publishes the outcome. On failure
// the cached set stays and the next periodic reload is a full interval away.
func (i *Issuer) runLoad(ctx context.Context, f *keyLoad) {
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SignTimeout)
	defer cancel()
	set, err := i.fetch(lctx)
	i.mu.Lock()
	if err == nil {
		i.install(set)
	} else {
		i.loadedAt = i.now()
		i.stale = false
	}
	i.inflight = nil
	i.mu.Unlock()
	f.err = err
	close(f.done)
}

// current returns the cached set, first reloading it when it is stale or older
// than ReloadInterval. One caller reloads; the others keep using the cached
// set meanwhile. A failed reload keeps the cached set.
func (i *Issuer) current(ctx context.Context) (KeySet, map[string]Key) {
	i.mu.Lock()
	if i.inflight == nil && (i.stale || i.now().Sub(i.loadedAt) >= ReloadInterval) {
		f := i.startLoad()
		i.mu.Unlock()
		i.runLoad(ctx, f)
		i.mu.Lock()
	}
	set, by := i.set, i.byKID
	i.mu.Unlock()
	return set, by
}

// lookup finds a key by kid. A kid the cached set lacks forces a reload, shared
// by every caller that asks while it runs and allowed at most once per
// UnknownKeyReloadInterval.
func (i *Issuer) lookup(ctx context.Context, kid string) (Key, bool) {
	_, by := i.current(ctx)
	if k, ok := by[kid]; ok {
		return k, true
	}
	i.mu.Lock()
	f := i.inflight
	switch {
	case f != nil:
		i.mu.Unlock()
	case !i.lastForced.IsZero() && i.now().Sub(i.lastForced) < UnknownKeyReloadInterval:
		i.mu.Unlock()
		return Key{}, false
	default:
		i.lastForced = i.now()
		f = i.startLoad()
		i.mu.Unlock()
		i.runLoad(ctx, f)
	}
	select {
	case <-f.done:
	case <-ctx.Done():
		return Key{}, false
	}
	i.mu.Lock()
	k, ok := i.byKID[kid]
	i.mu.Unlock()
	return k, ok
}

// Invalidate makes the next access reload the key set.
func (i *Issuer) Invalidate() {
	i.mu.Lock()
	i.stale = true
	i.mu.Unlock()
}

// URL is the issuer identifier (the iss claim).
func (i *Issuer) URL() string { return i.url }

// KID is the thumbprint of the active key of the cached set.
func (i *Issuer) KID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.set.Active.KID
}

// Algorithm is the algorithm of the active key of the cached set.
func (i *Issuer) Algorithm() keys.Algorithm {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.set.Active.Algorithm
}

// TokenEndpoint is the token endpoint URL, which a client assertion may still
// name as its audience (RFC 7523bis prefers the issuer URL).
func (i *Issuer) TokenEndpoint() string { return i.url + "/v1/auth/token" }

// MetadataURI is where the RFC 8414 authorization server metadata is served.
func (i *Issuer) MetadataURI() string { return i.url + "/.well-known/oauth-authorization-server" }

// JWKSURI is where the public keys are served.
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
func (i *Issuer) Mint(ctx context.Context, in MintInput) (string, time.Time, error) {
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
	set, _ := i.current(ctx)
	if set.Signer == nil {
		return "", time.Time{}, fmt.Errorf("%w: no usable key", ErrSignerUnavailable)
	}
	ja := jose.EdDSA
	if set.Active.Algorithm == keys.ES256 {
		ja = jose.ES256
	}
	sctx, cancel := context.WithTimeout(ctx, SignTimeout)
	defer cancel()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: ja, Key: cryptosigner.Opaque(ctxSigner{sctx, set.Signer})},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", set.Active.KID))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: %w", ErrSignerUnavailable, err)
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
	raw, err := jwt.Signed(signer).Claims(c).Claims(extra).Serialize()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: sign: %w", ErrSignerUnavailable, err)
	}
	return raw, exp, nil
}

// Verify checks a bearer minted by this issuer and returns its identity.
func (i *Issuer) Verify(ctx context.Context, raw string, now time.Time) (string, error) {
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
	key, ok := i.lookup(ctx, tok.Headers[0].KeyID)
	if !ok {
		return "", ErrUnknownKey
	}
	if !isAccessTokenType(tok.Headers[0]) {
		return "", invalid("typ must be at+jwt")
	}
	var c jwt.Claims
	if err := tok.Claims(key.Public, &c); err != nil {
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

// JWKS is the public key set served at JWKSURI: every key of the set, in the
// set's order.
func (i *Issuer) JWKS(ctx context.Context) jose.JSONWebKeySet {
	set, _ := i.current(ctx)
	out := jose.JSONWebKeySet{Keys: make([]jose.JSONWebKey, 0, len(set.Keys))}
	for _, k := range set.Keys {
		j, err := keys.ToJWK(k.Public, k.KID)
		if err != nil {
			// validate admitted the key, so this cannot happen.
			continue
		}
		out.Keys = append(out.Keys, j)
	}
	return out
}
