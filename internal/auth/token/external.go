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

// External is a configured external issuer (Decision 57, spec 5.1): its
// tokens are verified against its JWKS and their claims are read for the
// token exchange; a provider token is never accepted as a bearer (Decision
// 10). The zero value of the unexported state is ready to use; share one
// External by pointer.
//
// The JWKS cache never blocks a request on a slow or down IdP for longer than
// one fetch: concurrent requests share a single fetch, a request that a cached
// key can answer does not wait for a refresh in progress, and a failed fetch is
// not retried for a minute while the previous key set keeps serving.
type External struct {
	Name                      string // the issuers entry name
	Issuer, JWKSURI, Audience string
	Algorithms                []jose.SignatureAlgorithm // nil: EdDSA, ES256, RS256
	Claims                    ClaimNames                // empty members: email, groups, sub
	HumanLifetime             time.Duration             // lifetime of the human bearer minted for this issuer; the caller applies it
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

// ClaimNames are the provider claims that carry the email, the groups and the
// subject; an empty member means the default (email, groups, sub).
type ClaimNames struct{ Email, Groups, Subject string }

func (n ClaimNames) orDefault() ClaimNames {
	if n.Email == "" {
		n.Email = "email"
	}
	if n.Groups == "" {
		n.Groups = "groups"
	}
	if n.Subject == "" {
		n.Subject = "sub"
	}
	return n
}

// Federated is what a verified provider token says about its holder.
type Federated struct {
	Issuer, Name, Email string
	Groups              []string
	Subject, ClientID   string // ClientID: the token's client_id, else azp, else ""
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
		return jose.JSONWebKeySet{}, fmt.Errorf("%w: jwks request: %w", ErrJWKSUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return jose.JSONWebKeySet{}, fmt.Errorf("%w: fetch jwks: %w", ErrJWKSUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return jose.JSONWebKeySet{}, fmt.Errorf("%w: fetch jwks: status %d", ErrJWKSUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return jose.JSONWebKeySet{}, fmt.Errorf("%w: read jwks: %w", ErrJWKSUnavailable, err)
	}
	if len(body) > maxJWKSBytes {
		return jose.JSONWebKeySet{}, fmt.Errorf("%w: jwks too large", ErrJWKSUnavailable)
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return jose.JSONWebKeySet{}, fmt.Errorf("%w: parse jwks: %w", ErrJWKSUnavailable, err)
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

// Verify checks a provider token (signature, iss, aud, exp, nbf, algorithm
// allowlist) and returns the claims the exchange needs.
func (e *External) Verify(ctx context.Context, raw string) (Federated, error) {
	if e.Issuer == "" || e.Audience == "" || e.JWKSURI == "" {
		return Federated{}, errors.New("token: external issuer needs an issuer, an audience and a JWKS URI")
	}
	if strings.Count(raw, ".") != 2 {
		return Federated{}, invalid("not a compact JWS")
	}
	algs := e.Algorithms
	if algs == nil {
		algs = defaultExternalAlgorithms
	}
	tok, err := jwt.ParseSigned(raw, algs)
	if err != nil {
		return Federated{}, invalidCause("", err)
	}
	if len(tok.Headers) != 1 {
		return Federated{}, invalid("expected one signature")
	}
	h := tok.Headers[0]
	if h.KeyID == "" {
		return Federated{}, invalid("kid header is required")
	}
	jwk, err := e.key(ctx, h.KeyID)
	if err != nil {
		return Federated{}, err
	}
	if jwk.Algorithm != "" && jwk.Algorithm != h.Algorithm {
		return Federated{}, invalid("key alg %q does not match the token's %q", jwk.Algorithm, h.Algorithm)
	}
	var c jwt.Claims
	var all map[string]any
	if err := tok.Claims(jwk.Key, &c, &all); err != nil {
		return Federated{}, invalidCause("signature", err)
	}
	exp := jwt.Expected{Issuer: e.Issuer, AnyAudience: jwt.Audience{e.Audience}, Time: e.now()}
	if err := c.ValidateWithLeeway(exp, Leeway); err != nil {
		return Federated{}, invalidCause("", err)
	}
	if c.Expiry == nil {
		return Federated{}, invalid("exp is required")
	}
	names := e.Claims.orDefault()
	f := Federated{Issuer: e.Issuer, Name: e.Name}
	if f.Subject, _ = all[names.Subject].(string); f.Subject == "" {
		return Federated{}, invalid("%s claim is required", names.Subject)
	}
	if f.Email, _ = all[names.Email].(string); f.Email == "" {
		return Federated{}, fmt.Errorf("%w: %w: %s claim is required", ErrInvalid, ErrNoEmail, names.Email)
	}
	if f.Groups, err = groupsOf(all[names.Groups]); err != nil {
		return Federated{}, invalid("%s claim: %v", names.Groups, err)
	}
	for _, name := range []string{"client_id", "azp"} {
		v, ok := all[name]
		if !ok {
			continue
		}
		s, isStr := v.(string)
		if !isStr {
			return Federated{}, invalid("%s claim must be a string", name)
		}
		f.ClientID = s
		break
	}
	return f, nil
}

// groupsOf reads a groups claim: absent is nil, a string is one group, an
// array of strings is those.
func groupsOf(v any) ([]string, error) {
	switch g := v.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{g}, nil
	case []any:
		out := make([]string, 0, len(g))
		for _, x := range g {
			s, ok := x.(string)
			if !ok {
				return nil, errors.New("must be a string or an array of strings")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, errors.New("must be a string or an array of strings")
}
