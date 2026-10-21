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
	fetchTimeout    = 10 * time.Second
	maxJWKSBytes    = 1 << 20
)

var defaultExternalAlgorithms = []jose.SignatureAlgorithm{jose.EdDSA, jose.ES256, jose.RS256}

// External is a configured external issuer (Decision 57, Milestone 5 choice 11):
// its tokens are verified against its JWKS and their sub is mapped to an
// identity. The zero value of the unexported state is ready to use; share one
// External by pointer.
//
// The JWKS cache never blocks a request on a slow or down IdP for longer than
// one fetch: concurrent requests share a single fetch, a request that a cached
// key can answer does not wait for a refresh in progress, and a failed fetch is
// not retried for a minute while the previous key set keeps serving.
type External struct {
	Issuer, JWKSURI, Audience string
	Algorithms                []jose.SignatureAlgorithm // nil: EdDSA, ES256, RS256
	Subjects                  map[string]string         // external sub -> identity
	Client                    *http.Client              // nil: a 10 s client
	TTL                       time.Duration             // 0: 300 s
	Now                       func() time.Time          // nil: time.Now

	mu          sync.Mutex // guards everything below; never held across a fetch
	set         jose.JSONWebKeySet
	fetched     time.Time // last successful fetch; zero: none cached
	lastAttempt time.Time // start of the last fetch attempt, successful or not
	lastErr     error     // error of the last attempt; nil when it succeeded
	inflight    *flight   // the fetch in progress, if any
}

// flight is one fetch in progress; done closes when its outcome is recorded.
type flight struct{ done chan struct{} }

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

// fetch GETs the JWKS. It touches no state of e, so it runs without e.mu.
func (e *External) fetch(ctx context.Context) (jose.JSONWebKeySet, error) {
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.JWKSURI, nil)
	if err != nil {
		return jose.JSONWebKeySet{}, fmt.Errorf("token: jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return jose.JSONWebKeySet{}, fmt.Errorf("token: fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return jose.JSONWebKeySet{}, fmt.Errorf("token: fetch jwks: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return jose.JSONWebKeySet{}, fmt.Errorf("token: read jwks: %w", err)
	}
	if len(body) > maxJWKSBytes {
		return jose.JSONWebKeySet{}, errors.New("token: jwks too large")
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return jose.JSONWebKeySet{}, fmt.Errorf("token: parse jwks: %w", err)
	}
	return set, nil
}

func find(set jose.JSONWebKeySet, kid string) (jose.JSONWebKey, bool) {
	for _, k := range set.Keys {
		if k.KeyID == kid && k.IsPublic() && (k.Use == "" || k.Use == "sig") {
			return k, true
		}
	}
	return jose.JSONWebKey{}, false
}

// due reports whether a fetch should start now; e.mu is held. An unknown kid
// refetches when the last attempt, successful or not, is over a minute old. An
// empty or TTL-expired cache refetches at once, except that after a failed
// attempt it waits a minute so a down IdP is not hit by every request.
func (e *External) due(now time.Time, unknownKid bool) bool {
	if unknownKid {
		return now.Sub(e.lastAttempt) >= refetchInterval
	}
	if !e.fetched.IsZero() && now.Sub(e.fetched) < e.ttl() {
		return false
	}
	return e.lastErr == nil || now.Sub(e.lastAttempt) >= refetchInterval
}

// refresh starts a fetch when one is due, or joins the one in progress, and
// returns when it has finished or ctx is done. It reports nothing: the outcome
// is the cache and lastErr. A request whose kid the cache already holds does
// not wait for a fetch in progress; any other request does, because that fetch
// may bring the kid.
func (e *External) refresh(ctx context.Context, now time.Time, kid string, unknownKid bool) {
	e.mu.Lock()
	f := e.inflight
	switch {
	case f != nil:
		if _, ok := find(e.set, kid); ok {
			e.mu.Unlock()
			return
		}
	case !e.due(now, unknownKid):
		e.mu.Unlock()
		return
	default:
		f = &flight{done: make(chan struct{})}
		e.inflight = f
		e.lastAttempt = now
		go e.run(ctx, f, now)
	}
	e.mu.Unlock()
	select {
	case <-f.done:
	case <-ctx.Done():
	}
}

// run performs the fetch of f and records its outcome. The fetch is detached
// from the request that started it, which may be canceled while the requests
// that joined it still need the result; fetchTimeout bounds it.
func (e *External) run(ctx context.Context, f *flight, now time.Time) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	defer cancel()
	set, err := e.fetch(ctx)
	e.mu.Lock()
	if err != nil {
		e.lastErr = err // the previous set, if any, stays
	} else {
		e.set, e.fetched, e.lastErr = set, now, nil
	}
	e.inflight = nil
	e.mu.Unlock()
	close(f.done)
}

func (e *External) lookup(kid string) (jose.JSONWebKey, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return find(e.set, kid)
}

// key returns the JWKS key for kid, fetching when the cache is empty or older
// than the TTL and refetching once when kid is unknown and the last attempt is
// over a minute old. When the fetch that would have found kid failed, the
// error is the fetch's, not ErrUnknownKey.
func (e *External) key(ctx context.Context, kid string) (jose.JSONWebKey, error) {
	now := e.now()
	e.refresh(ctx, now, kid, false)
	if k, ok := e.lookup(kid); ok {
		return k, nil
	}
	if err := ctx.Err(); err != nil {
		return jose.JSONWebKey{}, err
	}
	e.refresh(ctx, now, kid, true)
	if k, ok := e.lookup(kid); ok {
		return k, nil
	}
	if err := ctx.Err(); err != nil {
		return jose.JSONWebKey{}, err
	}
	e.mu.Lock()
	err := e.lastErr
	e.mu.Unlock()
	if err != nil {
		return jose.JSONWebKey{}, err
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
		return "", invalidCause("", err)
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
		return "", invalidCause("signature", err)
	}
	exp := jwt.Expected{Issuer: e.Issuer, AnyAudience: jwt.Audience{e.Audience}, Time: e.now()}
	if err := c.ValidateWithLeeway(exp, Leeway); err != nil {
		return "", invalidCause("", err)
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
