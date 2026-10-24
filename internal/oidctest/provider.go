// Package oidctest is a fake OIDC provider for tests: an httptest server that
// serves discovery and a JWKS and mints signed access tokens. It imports no
// package of this module.
package oidctest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
}

// Provider is a running fake provider.
type Provider struct {
	opts   Options
	srv    *httptest.Server
	signer jose.Signer
	jwk    jose.JSONWebKey
	broken atomic.Bool
}

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
		opts:   o,
		signer: signer,
		jwk:    jose.JSONWebKey{Key: priv.Public(), KeyID: kid, Algorithm: o.Algorithm, Use: "sig"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"issuer": p.URL(), "jwks_uri": p.JWKSURI()})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		if p.broken.Load() {
			http.Error(w, "jwks unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{p.jwk}})
	})
	p.srv = httptest.NewServer(mux)
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
