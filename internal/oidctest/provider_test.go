package oidctest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func get(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func jwks(t *testing.T, p *Provider) jose.JSONWebKeySet {
	t.Helper()
	status, body := get(t, p.JWKSURI())
	if status != http.StatusOK {
		t.Fatalf("GET jwks = %d %s", status, body)
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		t.Fatalf("jwks body: %v", err)
	}
	return set
}

func TestProviderMintDefaults(t *testing.T) {
	for _, alg := range []string{"", "RS256", "ES256", "EdDSA"} {
		t.Run("alg-"+alg, func(t *testing.T) {
			p := New(t, Options{Audience: "pai", Algorithm: alg})
			raw := p.Mint(map[string]any{"sub": "u1"})
			want := alg
			if want == "" {
				want = "RS256"
			}
			tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.SignatureAlgorithm(want)})
			if err != nil {
				t.Fatalf("ParseSigned: %v", err)
			}
			kid := tok.Headers[0].KeyID
			if kid == "" {
				t.Fatal("no kid header")
			}
			set := jwks(t, p)
			keys := set.Key(kid)
			if len(keys) != 1 {
				t.Fatalf("jwks has %d keys for kid %q, want 1", len(keys), kid)
			}
			var c jwt.Claims
			var all map[string]any
			if err := tok.Claims(keys[0].Key, &c, &all); err != nil {
				t.Fatalf("verify against /jwks: %v", err)
			}
			if c.Issuer != p.URL() || !c.Audience.Contains("pai") || c.Subject != "u1" {
				t.Errorf("claims = %+v", all)
			}
			if c.IssuedAt == nil || c.Expiry == nil {
				t.Fatalf("iat/exp missing: %+v", all)
			}
			if d := time.Until(c.Expiry.Time()); d < 4*time.Minute || d > 6*time.Minute {
				t.Errorf("exp in %v, want about 5 minutes", d)
			}
		})
	}
}

func TestProviderMintOverridesAndOmits(t *testing.T) {
	p := New(t, Options{Audience: "pai", ClientID: "cid"})
	raw := p.Mint(map[string]any{"iss": "https://other", "aud": "x", "exp": 1, "email": "a@b", "client_id": nil, "iat": nil})
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]any
	if err := tok.UnsafeClaimsWithoutVerification(&all); err != nil {
		t.Fatal(err)
	}
	if all["iss"] != "https://other" || all["aud"] != "x" || all["exp"] != float64(1) || all["email"] != "a@b" {
		t.Errorf("overrides lost: %v", all)
	}
	if _, ok := all["client_id"]; ok {
		t.Errorf("nil member client_id present: %v", all)
	}
	if _, ok := all["iat"]; ok {
		t.Errorf("nil member iat present: %v", all)
	}
	// Options.ClientID is the default client_id when the member is absent.
	raw = p.Mint(map[string]any{})
	tok, _ = jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	all = nil
	if err := tok.UnsafeClaimsWithoutVerification(&all); err != nil {
		t.Fatal(err)
	}
	if all["client_id"] != "cid" {
		t.Errorf("default client_id = %v, want cid", all["client_id"])
	}
}

func TestProviderDiscovery(t *testing.T) {
	p := New(t, Options{Audience: "pai"})
	status, body := get(t, p.URL()+"/.well-known/openid-configuration")
	if status != http.StatusOK {
		t.Fatalf("discovery = %d %s", status, body)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["issuer"] != p.URL() || doc["jwks_uri"] != p.JWKSURI() {
		t.Errorf("discovery = %v, want issuer %s jwks_uri %s", doc, p.URL(), p.JWKSURI())
	}
	if !strings.HasPrefix(p.JWKSURI(), p.URL()) {
		t.Errorf("JWKSURI %q not under URL %q", p.JWKSURI(), p.URL())
	}
}

func TestProviderBreakJWKS(t *testing.T) {
	p := New(t, Options{Audience: "pai"})
	p.BreakJWKS()
	if status, _ := get(t, p.JWKSURI()); status != http.StatusServiceUnavailable {
		t.Errorf("broken jwks = %d, want 503", status)
	}
	if status, _ := get(t, p.URL()+"/.well-known/openid-configuration"); status != http.StatusOK {
		t.Errorf("discovery after BreakJWKS = %d, want 200", status)
	}
}

// TestProviderClosedAfterCleanup proves the harness cleans up after itself: a
// provider made in a sub-test refuses connections once the sub-test ends.
func TestProviderClosedAfterCleanup(t *testing.T) {
	var url string
	t.Run("inner", func(t *testing.T) {
		p := New(t, Options{Audience: "pai"})
		url = p.JWKSURI()
		if status, _ := get(t, url); status != http.StatusOK {
			t.Fatalf("live provider = %d", status)
		}
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("provider still answers after its test ended (%d)", resp.StatusCode)
	}
}
