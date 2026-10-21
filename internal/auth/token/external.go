package token

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	defaultTTL      = 300 * time.Second
	refetchInterval = time.Minute
	maxJWKSBytes    = 1 << 20
)

var defaultExternalAlgorithms = []jose.SignatureAlgorithm{jose.EdDSA, jose.ES256, jose.RS256}

// External is a configured external issuer (Decision 57, Milestone 5 choice 11):
// its tokens are verified against its JWKS and their sub is mapped to an
// identity. The zero value of the unexported state is ready to use; share one
// External by pointer.
type External struct {
	Issuer, JWKSURI, Audience string
	Algorithms                []jose.SignatureAlgorithm // nil: EdDSA, ES256, RS256
	Subjects                  map[string]string         // external sub -> identity
	Client                    *http.Client              // nil: a 10 s client
	TTL                       time.Duration             // 0: 300 s
	Now                       func() time.Time          // nil: time.Now

	mu          sync.Mutex
	set         jose.JSONWebKeySet
	fetched     time.Time // last successful fetch; zero: none cached
	lastAttempt time.Time // last fetch attempt, successful or not
}

func (e *External) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *External) ttl() time.Duration {
	if e.TTL > 0 {
		return e.TTL
	}
	return defaultTTL
}

func (e *External) fetch(ctx context.Context, now time.Time) error {
	e.lastAttempt = now
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.JWKSURI, nil)
	if err != nil {
		return fmt.Errorf("token: jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("token: fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token: fetch jwks: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return fmt.Errorf("token: read jwks: %w", err)
	}
	if len(body) > maxJWKSBytes {
		return errors.New("token: jwks too large")
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return fmt.Errorf("token: parse jwks: %w", err)
	}
	e.set = set
	e.fetched = now
	return nil
}

func find(set jose.JSONWebKeySet, kid string) (jose.JSONWebKey, bool) {
	for _, k := range set.Keys {
		if k.KeyID == kid && k.IsPublic() && (k.Use == "" || k.Use == "sig") {
			return k, true
		}
	}
	return jose.JSONWebKey{}, false
}

// key returns the JWKS key for kid, fetching when the cache is empty or older
// than the TTL and refetching once when kid is unknown and the last attempt is
// over a minute old.
func (e *External) key(ctx context.Context, kid string) (jose.JSONWebKey, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if e.fetched.IsZero() || now.Sub(e.fetched) >= e.ttl() {
		if err := e.fetch(ctx, now); err != nil {
			return jose.JSONWebKey{}, err
		}
	}
	if k, ok := find(e.set, kid); ok {
		return k, nil
	}
	if now.Sub(e.lastAttempt) >= refetchInterval {
		if err := e.fetch(ctx, now); err != nil {
			return jose.JSONWebKey{}, err
		}
		if k, ok := find(e.set, kid); ok {
			return k, nil
		}
	}
	return jose.JSONWebKey{}, ErrUnknownKey
}

// Verify checks a bearer from this issuer and returns the mapped identity.
func (e *External) Verify(ctx context.Context, raw string) (string, error) {
	if e.Issuer == "" || e.Audience == "" || e.JWKSURI == "" {
		return "", errors.New("token: external issuer needs an issuer, an audience and a JWKS URI")
	}
	if strings.Count(raw, ".") != 2 {
		return "", invalid("not a compact JWS")
	}
	algs := e.Algorithms
	if algs == nil {
		algs = defaultExternalAlgorithms
	}
	tok, err := jwt.ParseSigned(raw, algs)
	if err != nil {
		return "", invalid("%v", err)
	}
	if len(tok.Headers) != 1 {
		return "", invalid("expected one signature")
	}
	h := tok.Headers[0]
	if h.KeyID == "" {
		return "", invalid("kid header is required")
	}
	jwk, err := e.key(ctx, h.KeyID)
	if err != nil {
		return "", err
	}
	if jwk.Algorithm != "" && jwk.Algorithm != h.Algorithm {
		return "", invalid("key alg %q does not match the token's %q", jwk.Algorithm, h.Algorithm)
	}
	var c jwt.Claims
	if err := tok.Claims(jwk.Key, &c); err != nil {
		return "", invalid("signature: %v", err)
	}
	exp := jwt.Expected{Issuer: e.Issuer, AnyAudience: jwt.Audience{e.Audience}, Time: e.now()}
	if err := c.ValidateWithLeeway(exp, Leeway); err != nil {
		return "", invalid("%v", err)
	}
	if c.Expiry == nil {
		return "", invalid("exp is required")
	}
	if c.Subject == "" {
		return "", invalid("sub is required")
	}
	identity, ok := e.Subjects[c.Subject]
	if !ok || identity == "" {
		return "", invalid("subject %q is not mapped", c.Subject)
	}
	return identity, nil
}
