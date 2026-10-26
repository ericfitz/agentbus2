// Package oidctest is a fake OIDC provider for tests: an httptest server that
// serves discovery and a JWKS and mints signed access tokens. It imports no
// package of this module.
package oidctest

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Options configure New.
type Options struct {
	Audience  string // the aud claim of minted tokens
	ClientID  string // when set, the client_id claim of minted tokens
	Algorithm string // RS256 (default), ES256 or EdDSA

	// RefreshTokens makes the token endpoint issue a refresh_token and serve
	// the refresh_token grant.
	RefreshTokens bool
	// RotateRefreshTokens makes each refresh_token grant issue a new refresh
	// token and retire the one presented (as Dex does).
	RotateRefreshTokens bool
	// RefreshRejectError is the error code for an unknown or retired refresh
	// token: invalid_grant (RFC 6749) by default; Dex answers invalid_request.
	RefreshRejectError string
	// AuthorizeError, when set, makes /authorize redirect with that error code
	// (and the request's state) instead of issuing a code.
	AuthorizeError string
	// Claims are the claims of the access tokens the token endpoint issues
	// (see Mint; sub defaults to "oidctest-user").
	Claims map[string]any
}

// Provider is a running fake provider.
type Provider struct {
	opts   Options
	srv    *httptest.Server
	signer jose.Signer
	jwk    jose.JSONWebKey
	broken atomic.Bool

	mu       sync.Mutex
	codes    map[string]authCode // outstanding authorization codes
	refresh  map[string]string   // refresh token -> client_id
	requests []string
}

type authCode struct{ challenge, redirectURI, clientID string }

const kid = "oidctest-key-1"

// New starts a provider and closes it with tb.Cleanup.
func New(tb testing.TB, o Options) *Provider {
	tb.Helper()
	if o.Algorithm == "" {
		o.Algorithm = "RS256"
	}
	var priv crypto.Signer
	var err error
	switch o.Algorithm {
	case "RS256":
		priv, err = rsa.GenerateKey(rand.Reader, 2048)
	case "ES256":
		priv, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "EdDSA":
		_, priv, err = ed25519.GenerateKey(rand.Reader)
	default:
		tb.Fatalf("oidctest: unsupported algorithm %q", o.Algorithm)
	}
	if err != nil {
		tb.Fatalf("oidctest: generate key: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.SignatureAlgorithm(o.Algorithm), Key: priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		tb.Fatalf("oidctest: signer: %v", err)
	}
	p := &Provider{
		opts:    o,
		signer:  signer,
		jwk:     jose.JSONWebKey{Key: priv.Public(), KeyID: kid, Algorithm: o.Algorithm, Use: "sig"},
		codes:   map[string]authCode{},
		refresh: map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                 p.URL(),
			"jwks_uri":               p.JWKSURI(),
			"authorization_endpoint": p.URL() + "/authorize",
			"token_endpoint":         p.URL() + "/token",
		})
	})
	mux.HandleFunc("/authorize", p.authorize)
	mux.HandleFunc("/token", p.token)
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		if p.broken.Load() {
			http.Error(w, "jwks unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{p.jwk}})
	})
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.record(r)
		mux.ServeHTTP(w, r)
	}))
	tb.Cleanup(p.srv.Close)
	return p
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// URL is the provider's issuer URL.
func (p *Provider) URL() string { return p.srv.URL }

// JWKSURI is where the provider serves its key set.
func (p *Provider) JWKSURI() string { return p.srv.URL + "/jwks" }

// BreakJWKS makes the JWKS endpoint answer 503 from now on (a provider outage).
func (p *Provider) BreakJWKS() { p.broken.Store(true) }

// Mint signs a token with claims. It fills iss (the provider URL), aud
// (Options.Audience), client_id (Options.ClientID, when set), iat and exp (five
// minutes from now) unless claims has them; a member set to nil is omitted.
func (p *Provider) Mint(claims map[string]any) string {
	now := time.Now()
	all := map[string]any{
		"iss": p.URL(),
		"aud": p.opts.Audience,
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	}
	if p.opts.ClientID != "" {
		all["client_id"] = p.opts.ClientID
	}
	for k, v := range claims {
		if v == nil {
			delete(all, k)
			continue
		}
		all[k] = v
	}
	raw, err := jwt.Signed(p.signer).Claims(all).Serialize()
	if err != nil {
		panic("oidctest: sign: " + err.Error())
	}
	return raw
}

// record logs a request as "METHOD path?query"; a POST form body follows a space.
func (p *Provider) record(r *http.Request) {
	line := r.Method + " " + r.URL.RequestURI()
	if r.Method == http.MethodPost {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		line += " " + string(body)
	}
	p.mu.Lock()
	p.requests = append(p.requests, line)
	p.mu.Unlock()
}

// Requests returns a copy of every request the provider has served, in order,
// as "METHOD path?query" (a POST appends a space and its form-encoded body).
func (p *Provider) Requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

func (p *Provider) newSecret(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func loopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

// authorize serves /authorize: it validates like a strict provider (a bad
// request is a 400 page, never a redirect) and answers with a 302 to the
// loopback redirect_uri carrying code and state, or error and state.
func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect := q.Get("redirect_uri")
	switch {
	case q.Get("response_type") != "code":
		http.Error(w, "unsupported response_type", http.StatusBadRequest)
		return
	case p.opts.ClientID != "" && q.Get("client_id") != p.opts.ClientID:
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	case !loopback(redirect):
		http.Error(w, "redirect_uri must be a loopback http URL", http.StatusBadRequest)
		return
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		http.Error(w, "PKCE with S256 is required", http.StatusBadRequest)
		return
	}
	target, _ := url.Parse(redirect)
	v := target.Query()
	if state := q.Get("state"); state != "" {
		v.Set("state", state)
	}
	if p.opts.AuthorizeError != "" {
		v.Set("error", p.opts.AuthorizeError)
	} else {
		code := p.newSecret("code-")
		p.mu.Lock()
		p.codes[code] = authCode{challenge: q.Get("code_challenge"), redirectURI: redirect, clientID: q.Get("client_id")}
		p.mu.Unlock()
		v.Set("code", code)
	}
	target.RawQuery = v.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func tokenError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// token serves /token: the authorization_code grant (single-use codes, the
// PKCE verifier checked against the stored challenge) and, with
// Options.RefreshTokens, the refresh_token grant.
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		tokenError(w, "invalid_request")
		return
	}
	f := r.PostForm
	var refresh string
	switch f.Get("grant_type") {
	case "authorization_code":
		p.mu.Lock()
		c, ok := p.codes[f.Get("code")]
		delete(p.codes, f.Get("code")) // single use, even when the attempt fails
		p.mu.Unlock()
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		got := base64.RawURLEncoding.EncodeToString(sum[:])
		if !ok || f.Get("code_verifier") == "" || f.Get("redirect_uri") != c.redirectURI || f.Get("client_id") != c.clientID ||
			subtle.ConstantTimeCompare([]byte(got), []byte(c.challenge)) != 1 {
			tokenError(w, "invalid_grant")
			return
		}
		if p.opts.RefreshTokens {
			refresh = p.newSecret("refresh-")
			p.mu.Lock()
			p.refresh[refresh] = c.clientID
			p.mu.Unlock()
		}
	case "refresh_token":
		p.mu.Lock()
		cid, ok := p.refresh[f.Get("refresh_token")]
		p.mu.Unlock()
		if !p.opts.RefreshTokens || !ok || cid != f.Get("client_id") {
			code := p.opts.RefreshRejectError
			if code == "" {
				code = "invalid_grant"
			}
			tokenError(w, code)
			return
		}
		refresh = f.Get("refresh_token")
		if p.opts.RotateRefreshTokens {
			next := p.newSecret("refresh-")
			p.mu.Lock()
			delete(p.refresh, refresh)
			p.refresh[next] = cid
			p.mu.Unlock()
			refresh = next
		}
	default:
		tokenError(w, "unsupported_grant_type")
		return
	}
	claims := map[string]any{"sub": "oidctest-user"}
	for k, v := range p.opts.Claims {
		claims[k] = v
	}
	out := map[string]any{"access_token": p.Mint(claims), "token_type": "Bearer", "expires_in": 300}
	if refresh != "" {
		out["refresh_token"] = refresh
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, out)
}
