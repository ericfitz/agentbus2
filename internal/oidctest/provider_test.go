package oidctest

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

const testRedirect = "http://127.0.0.1:9/callback"

// noFollow returns redirects instead of following them.
var noFollow = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func authorizeURL(p *Provider, mod func(url.Values)) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"cid"},
		"redirect_uri":          {testRedirect},
		"scope":                 {"openid"},
		"state":                 {"st1"},
		"code_challenge":        {pkceChallenge("verifier-1")},
		"code_challenge_method": {"S256"},
	}
	if mod != nil {
		mod(q)
	}
	return p.URL() + "/authorize?" + q.Encode()
}

// authorize calls /authorize and returns the status and the Location query.
func authorize(t *testing.T, target string) (int, url.Values) {
	t.Helper()
	resp, err := noFollow.Get(target)
	if err != nil {
		t.Fatalf("GET authorize: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	loc, err := resp.Location()
	if err != nil {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, loc.Query()
}

func postToken(t *testing.T, p *Provider, form url.Values) (int, map[string]any) {
	t.Helper()
	resp, err := noFollow.PostForm(p.URL()+"/token", form)
	if err != nil {
		t.Fatalf("POST token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func codeForm(code, verifier string) url.Values {
	return url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {testRedirect},
		"client_id":     {"cid"},
		"code_verifier": {verifier},
	}
}

func issueCode(t *testing.T, p *Provider) string {
	t.Helper()
	status, q := authorize(t, authorizeURL(p, nil))
	if status != http.StatusFound || q.Get("code") == "" || q.Get("state") != "st1" {
		t.Fatalf("authorize = %d %v", status, q)
	}
	return q.Get("code")
}

func TestProviderDiscoveryEndpoints(t *testing.T) {
	p := New(t, Options{Audience: "pai"})
	_, body := get(t, p.URL()+"/.well-known/openid-configuration")
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["authorization_endpoint"] != p.URL()+"/authorize" || doc["token_endpoint"] != p.URL()+"/token" {
		t.Errorf("discovery = %v", doc)
	}
}

func TestProviderAuthorizeRejectsPlainPKCE(t *testing.T) {
	p := New(t, Options{Audience: "pai", ClientID: "cid"})
	cases := map[string]func(url.Values){
		"plain":         func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"no-method":     func(q url.Values) { q.Del("code_challenge_method") },
		"no-challenge":  func(q url.Values) { q.Del("code_challenge") },
		"wrong-client":  func(q url.Values) { q.Set("client_id", "other") },
		"remote-uri":    func(q url.Values) { q.Set("redirect_uri", "https://evil.example/callback") },
		"bad-response":  func(q url.Values) { q.Set("response_type", "token") },
		"no-redirect":   func(q url.Values) { q.Del("redirect_uri") },
		"malformed-uri": func(q url.Values) { q.Set("redirect_uri", "://x") },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			status, q := authorize(t, authorizeURL(p, mod))
			if status != http.StatusBadRequest || q != nil {
				t.Errorf("status = %d, redirect %v, want 400 and no redirect", status, q)
			}
		})
	}
	if status, _ := authorize(t, authorizeURL(p, nil)); status != http.StatusFound {
		t.Errorf("valid request = %d, want 302", status)
	}
}

func TestProviderTokenChecksVerifier(t *testing.T) {
	p := New(t, Options{Audience: "pai", ClientID: "cid", Claims: map[string]any{"sub": "u1", "email": "a@b.example"}})
	for name, mod := range map[string]func(url.Values){
		"wrong-verifier": func(f url.Values) { f.Set("code_verifier", "nope") },
		"no-verifier":    func(f url.Values) { f.Del("code_verifier") },
		"wrong-client":   func(f url.Values) { f.Set("client_id", "other") },
		"wrong-redirect": func(f url.Values) { f.Set("redirect_uri", "http://127.0.0.1:10/callback") },
		"wrong-grant":    func(f url.Values) { f.Set("grant_type", "password") },
	} {
		t.Run(name, func(t *testing.T) {
			f := codeForm(issueCode(t, p), "verifier-1")
			mod(f)
			status, out := postToken(t, p, f)
			if status != http.StatusBadRequest || out["error"] == nil || out["access_token"] != nil {
				t.Errorf("status = %d body = %v, want 400 with an error", status, out)
			}
		})
	}
	status, out := postToken(t, p, codeForm(issueCode(t, p), "verifier-1"))
	if status != http.StatusOK {
		t.Fatalf("good exchange = %d %v", status, out)
	}
	if out["token_type"] != "Bearer" || out["refresh_token"] != nil {
		t.Errorf("body = %v, want Bearer and no refresh_token", out)
	}
	tok, err := jwt.ParseSigned(out["access_token"].(string), []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]any
	if err := tok.UnsafeClaimsWithoutVerification(&all); err != nil {
		t.Fatal(err)
	}
	if all["email"] != "a@b.example" || all["sub"] != "u1" || all["aud"] != "pai" {
		t.Errorf("claims = %v", all)
	}
	if _, ok := out["expires_in"].(float64); !ok {
		t.Errorf("expires_in missing: %v", out)
	}
}

func TestProviderCodeSingleUse(t *testing.T) {
	p := New(t, Options{Audience: "pai", ClientID: "cid"})
	code := issueCode(t, p)
	if status, _ := postToken(t, p, codeForm(code, "verifier-1")); status != http.StatusOK {
		t.Fatalf("first use = %d", status)
	}
	status, out := postToken(t, p, codeForm(code, "verifier-1"))
	if status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("second use = %d %v, want 400 invalid_grant", status, out)
	}
	// A failed attempt burns the code too.
	code = issueCode(t, p)
	postToken(t, p, codeForm(code, "wrong"))
	if status, _ := postToken(t, p, codeForm(code, "verifier-1")); status != http.StatusBadRequest {
		t.Errorf("code usable after a failed attempt: %d", status)
	}
}

func TestProviderRefreshGrant(t *testing.T) {
	p := New(t, Options{Audience: "pai", ClientID: "cid", RefreshTokens: true})
	_, out := postToken(t, p, codeForm(issueCode(t, p), "verifier-1"))
	rt, _ := out["refresh_token"].(string)
	if rt == "" {
		t.Fatalf("no refresh_token: %v", out)
	}
	status, out := postToken(t, p, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {"cid"}})
	if status != http.StatusOK || out["access_token"] == nil {
		t.Fatalf("refresh = %d %v", status, out)
	}
	for name, f := range map[string]url.Values{
		"unknown":      {"grant_type": {"refresh_token"}, "refresh_token": {"nope"}, "client_id": {"cid"}},
		"wrong-client": {"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {"other"}},
		"missing":      {"grant_type": {"refresh_token"}, "client_id": {"cid"}},
	} {
		status, out := postToken(t, p, f)
		if status != http.StatusBadRequest || out["error"] != "invalid_grant" {
			t.Errorf("%s: %d %v, want 400 invalid_grant", name, status, out)
		}
	}
	// Without RefreshTokens the grant is refused and no refresh token is issued.
	q := New(t, Options{Audience: "pai", ClientID: "cid"})
	status, out = postToken(t, q, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {"cid"}})
	if status != http.StatusBadRequest {
		t.Errorf("refresh on a provider without RefreshTokens = %d %v", status, out)
	}
}

// TestProviderRefreshRotation: with RotateRefreshTokens a refresh retires the
// token presented, and RefreshRejectError names the code for a retired one.
func TestProviderRefreshRotation(t *testing.T) {
	p := New(t, Options{Audience: "pai", ClientID: "cid", RefreshTokens: true, RotateRefreshTokens: true, RefreshRejectError: "invalid_request"})
	_, out := postToken(t, p, codeForm(issueCode(t, p), "verifier-1"))
	rt, _ := out["refresh_token"].(string)
	refresh := func(tok string) (int, map[string]any) {
		return postToken(t, p, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok}, "client_id": {"cid"}})
	}
	status, out := refresh(rt)
	next, _ := out["refresh_token"].(string)
	if status != http.StatusOK || next == "" || next == rt {
		t.Fatalf("refresh = %d %v, want a new refresh token", status, out)
	}
	if status, out := refresh(rt); status != http.StatusBadRequest || out["error"] != "invalid_request" {
		t.Errorf("retired token = %d %v, want 400 invalid_request", status, out)
	}
	if status, _ := refresh(next); status != http.StatusOK {
		t.Errorf("rotated token = %d, want 200", status)
	}
}

func TestProviderAuthorizeError(t *testing.T) {
	p := New(t, Options{Audience: "pai", ClientID: "cid", AuthorizeError: "access_denied"})
	status, q := authorize(t, authorizeURL(p, nil))
	if status != http.StatusFound || q.Get("error") != "access_denied" || q.Get("state") != "st1" || q.Get("code") != "" {
		t.Errorf("authorize = %d %v, want 302 error=access_denied state=st1 and no code", status, q)
	}
}

func TestProviderRequestsAndIsolation(t *testing.T) {
	a := New(t, Options{Audience: "pai", ClientID: "cid"})
	b := New(t, Options{Audience: "pai", ClientID: "cid"})
	code := issueCode(t, a)
	if len(b.Requests()) != 0 {
		t.Errorf("provider b saw %v", b.Requests())
	}
	// A code from one provider is no good at another.
	if status, _ := postToken(t, b, codeForm(code, "verifier-1")); status != http.StatusBadRequest {
		t.Errorf("code crossed providers: %d", status)
	}
	postToken(t, a, codeForm(code, "verifier-1"))
	reqs := a.Requests()
	if len(reqs) != 2 || !strings.HasPrefix(reqs[0], "GET /authorize?") ||
		!strings.HasPrefix(reqs[1], "POST /token ") || !strings.Contains(reqs[1], "code_verifier=verifier-1") {
		t.Errorf("requests = %q", reqs)
	}
	// Requests returns a copy.
	reqs[0] = "mutated"
	if a.Requests()[0] == "mutated" {
		t.Error("Requests exposes its backing slice")
	}
}
