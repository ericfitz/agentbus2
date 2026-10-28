package server

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/assertion"
	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/core/maintenance"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/store"
)

func genKey(t *testing.T, alg keys.Algorithm) crypto.Signer {
	t.Helper()
	k, err := keys.Generate(alg)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func kidOf(t *testing.T, k crypto.Signer) string {
	t.Helper()
	kid, err := keys.KID(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	return kid
}

// do sends one request with no transport identity (the Bearer authenticator
// ignores the in-process key anyway).
func (e *env) do(method, path, contentType, bearer string, body string) *http.Response {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := newReq(e.t, method, path, rd)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	return e.raw("", req)
}

func jwkJSON(t *testing.T, k crypto.Signer) map[string]any {
	t.Helper()
	j, err := keys.ToJWK(k.Public(), kidOf(t, k))
	if err != nil {
		t.Fatal(err)
	}
	b, err := j.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// register posts the public key of key as name and returns the response.
func (e *env) register(name string, key crypto.Signer, enrollment string) *http.Response {
	e.t.Helper()
	body := map[string]any{"identity": name, "public_key_jwk": jwkJSON(e.t, key)}
	if enrollment != "" {
		body["enrollment_token"] = enrollment
	}
	b, _ := json.Marshal(body)
	return e.do("POST", "/v1/auth/register", "application/json", "", string(b))
}

func (e *env) mustRegister(name string, key crypto.Signer) {
	e.t.Helper()
	if resp := e.register(name, key, ""); resp.StatusCode != 201 {
		e.t.Fatalf("register %q: %d %s", name, resp.StatusCode, readBody(e.t, resp))
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertionFor signs a client assertion for name aimed at the token endpoint.
func (e *env) assertionFor(name string, key crypto.Signer, aud string, life time.Duration) string {
	e.t.Helper()
	a, err := assertion.Sign(assertion.Input{
		Identity: name, Audience: aud, Key: key, KID: kidOf(e.t, key), Now: e.clock.Now(), Lifetime: life,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *env) exchange(clientAssertion, bearer string) *http.Response {
	e.t.Helper()
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_assertion_type": {assertion.Type},
		"client_assertion":      {clientAssertion},
	}
	return e.do("POST", "/v1/auth/token", "application/x-www-form-urlencoded", bearer, form.Encode())
}

// token exchanges a fresh assertion of name for a bearer.
func (e *env) token(name string, key crypto.Signer) string {
	e.t.Helper()
	resp := e.exchange(e.assertionFor(name, key, "http://inproc/v1/auth/token", 0), "")
	if resp.StatusCode != 200 {
		e.t.Fatalf("token %q: %d %s", name, resp.StatusCode, readBody(e.t, resp))
	}
	var at gen.AccessToken
	if err := json.Unmarshal([]byte(readBody(e.t, resp)), &at); err != nil {
		e.t.Fatal(err)
	}
	return at.AccessToken
}

func wantAuthProblem(t *testing.T, resp *http.Response, status int, code, detail string) {
	t.Helper()
	p := readProblem(t, resp)
	if resp.StatusCode != status || string(p.Code) != code {
		t.Fatalf("got %d %q (detail %v), want %d %q", resp.StatusCode, p.Code, p.Detail, status, code)
	}
	if detail != "" && (p.Detail == nil || !strings.Contains(*p.Detail, detail)) {
		t.Fatalf("detail %v does not contain %q", p.Detail, detail)
	}
}

func TestRegisterTokenThenCall(t *testing.T) {
	for _, alg := range []keys.Algorithm{keys.EdDSA, keys.ES256} {
		t.Run(string(alg), func(t *testing.T) {
			e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
			key := genKey(t, alg)
			jwk := jwkJSON(t, key)
			if alg == keys.ES256 && (jwk["kty"] != "EC" || jwk["crv"] != "P-256") {
				t.Fatalf("ES256 request JWK %v", jwk)
			}
			resp := e.register("pat", key, "")
			if resp.StatusCode != 201 {
				t.Fatalf("register: %d %s", resp.StatusCode, readBody(t, resp))
			}
			var reg gen.Registration
			if err := json.Unmarshal([]byte(readBody(t, resp)), &reg); err != nil {
				t.Fatal(err)
			}
			if reg.Kid != kidOf(t, key) || string(reg.Algorithm) != string(alg) || reg.Identity != "pat" || !reg.Created {
				t.Fatalf("registration %+v", reg)
			}

			resp = e.exchange(e.assertionFor("pat", key, "http://inproc/v1/auth/token", 0), "")
			if resp.StatusCode != 200 {
				t.Fatalf("token: %d %s", resp.StatusCode, readBody(t, resp))
			}
			var at gen.AccessToken
			if err := json.Unmarshal([]byte(readBody(t, resp)), &at); err != nil {
				t.Fatal(err)
			}
			if at.TokenType != "Bearer" || at.ExpiresIn != 3600 || at.AccessToken == "" {
				t.Fatalf("access token %+v", at)
			}

			if resp := e.do("GET", "/v1/channels", "", "Bearer "+at.AccessToken, ""); resp.StatusCode != 200 {
				t.Fatalf("with bearer: %d %s", resp.StatusCode, readBody(t, resp))
			}
			wantAuthProblem(t, e.do("GET", "/v1/channels", "", "", ""), 401, "unauthenticated", "")
			wantAuthProblem(t, e.do("GET", "/v1/channels", "", "Bearer x.y.z", ""), 401, "unauthenticated", "invalid bearer token")
		})
	}
}

func TestOpenOperationsIgnoreBadBearer(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	key := genKey(t, keys.EdDSA)
	e.mustRegister("pat", key)
	old := e.token("pat", key)
	e.clock.Advance(2 * time.Hour)

	resp := e.exchange(e.assertionFor("pat", key, "http://inproc/v1/auth/token", 0), "Bearer "+old)
	if resp.StatusCode != 200 {
		t.Fatalf("token with stale bearer: %d %s", resp.StatusCode, readBody(t, resp))
	}
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", "Bearer "+old, ""), 401, "unauthenticated", "expired")
	if resp := e.do("GET", "/v1/discover", "", "Bearer x", ""); resp.StatusCode != 200 {
		t.Fatalf("discover with bad bearer: %d", resp.StatusCode)
	}
	if resp := e.do("GET", "/.well-known/openid-configuration", "", "Bearer", ""); resp.StatusCode != 200 {
		t.Fatalf("openid-configuration with bare scheme: %d", resp.StatusCode)
	}
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", "Bearer", ""), 401, "unauthenticated", "malformed Authorization header")
}

func TestMalformedAuthorizationHeaders(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true})
	for _, h := range []string{"Bearer", "Bearer ", "Bearer  x.y.z", "Bearer x y", "Basic abc", "Token x.y.z", "x"} {
		wantAuthProblem(t, e.do("GET", "/v1/channels", "", h, ""), 401, "unauthenticated", "malformed Authorization header")
	}
	// The scheme is case-insensitive: this one is well formed and fails verification.
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", "bEaReR x.y.z", ""), 401, "unauthenticated", "invalid bearer token")
	// A second Authorization header is refused, not silently ignored.
	req := newReq(t, "GET", "/v1/channels", nil)
	req.Header.Add("Authorization", "Bearer a.b.c")
	req.Header.Add("Authorization", "Bearer a.b.c")
	wantAuthProblem(t, e.raw("", req), 401, "unauthenticated", "malformed Authorization header")
}

func TestTokenNoStore(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	key := genKey(t, keys.EdDSA)
	e.mustRegister("pat", key)
	resp := e.exchange(e.assertionFor("pat", key, "http://inproc/v1/auth/token", 0), "")
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Pragma") != "no-cache" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
}

func TestTokenRefusesForeignKeyAndReplay(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	pat, kim := genKey(t, keys.EdDSA), genKey(t, keys.EdDSA)
	e.mustRegister("pat", pat)
	e.mustRegister("kim", kim)
	const aud = "http://inproc"

	// kim's key signing as pat, naming kim's kid.
	foreign, err := assertion.Sign(assertion.Input{Identity: "pat", Audience: aud, Key: kim, KID: kidOf(t, kim), Now: e.clock.Now()})
	if err != nil {
		t.Fatal(err)
	}
	wantOAuthError(t, e.exchange(foreign, ""), 401, "invalid_client", "unknown identity or key", "")

	// Replay.
	a := e.assertionFor("pat", pat, aud, 0)
	if resp := e.exchange(a, ""); resp.StatusCode != 200 {
		t.Fatalf("first use: %d", resp.StatusCode)
	}
	wantOAuthError(t, e.exchange(a, ""), 401, "invalid_client", "assertion replayed", "")

	// Any audience but the issuer and the token endpoint is refused.
	wantOAuthError(t, e.exchange(e.assertionFor("pat", pat, "http://evil.example", 0), ""), 401, "invalid_client", "audience", "")
	// A signature by the right identity's key but a wrong kid.
	wrongKID, err := assertion.Sign(assertion.Input{Identity: "pat", Audience: aud, Key: pat, KID: "nope", Now: e.clock.Now()})
	if err != nil {
		t.Fatal(err)
	}
	wantOAuthError(t, e.exchange(wrongKID, ""), 401, "invalid_client", "unknown identity or key", "")
	// Expired by more than the leeway.
	stale, err := assertion.Sign(assertion.Input{Identity: "pat", Audience: aud, Key: pat, KID: kidOf(t, pat), Now: e.clock.Now().Add(-10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	wantOAuthError(t, e.exchange(stale, ""), 401, "invalid_client", "", "")
	// A lifetime above the cap cannot be signed by the client library, so forge one.
	long := forgedAssertion(t, pat, "pat", aud, e.clock.Now(), 20*time.Minute)
	wantOAuthError(t, e.exchange(long, ""), 401, "invalid_client", "", "")

	// HS256 is refused as malformed.
	hs, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "x"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(hs).Claims(jwt.Claims{Issuer: "pat", Subject: "pat", Audience: jwt.Audience{aud}, ID: "j", Expiry: jwt.NewNumericDate(e.clock.Now().Add(time.Minute))}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	wantOAuthError(t, e.exchange(raw, ""), 401, "invalid_client", "client assertion", "")
	wantOAuthError(t, e.exchange("not-a-jwt", ""), 401, "invalid_client", "client assertion", "")

	// Validator-level refusals.
	form := url.Values{"grant_type": {"password"}, "client_assertion_type": {assertion.Type}, "client_assertion": {a}}
	wantOAuthError(t, e.do("POST", "/v1/auth/token", "application/x-www-form-urlencoded", "", form.Encode()), 400, "unsupported_grant_type", "", "")
	wantOAuthError(t, e.do("POST", "/v1/auth/token", "application/json", "", `{"client_assertion":"x"}`), 400, "invalid_request", "", "")
	form = url.Values{"grant_type": {"client_credentials"}, "client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:saml2-bearer"}, "client_assertion": {a}}
	wantOAuthError(t, e.do("POST", "/v1/auth/token", "application/x-www-form-urlencoded", "", form.Encode()), 401, "invalid_client", "", "")
}

// forgedAssertion signs an assertion whose lifetime assertion.Sign would refuse.
func forgedAssertion(t *testing.T, key crypto.Signer, name, aud string, now time.Time, life time.Duration) string {
	t.Helper()
	alg := jose.EdDSA
	if a, _ := keys.AlgorithmOf(key.Public()); a == keys.ES256 {
		alg = jose.ES256
	}
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kidOf(t, key)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(s).Claims(jwt.Claims{
		Issuer: name, Subject: name, Audience: jwt.Audience{aud}, ID: "forged",
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(life)),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBearerRefusals(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	key := genKey(t, keys.EdDSA)
	e.mustRegister("pat", key)

	// A second issuer with the same URL and another key.
	other, err := token.NewIssuer(context.Background(), "http://inproc", token.StaticKeys(genKey(t, keys.EdDSA)), time.Hour, e.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	forged, _, err := other.Mint(context.Background(), token.MintInput{Identity: "pat", Kind: token.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", "Bearer "+forged, ""), 401, "unauthenticated", "unknown key")

	// The authenticator attaches; the core judges registration.
	ghost, _, err := e.issuer.Mint(context.Background(), token.MintInput{Identity: "ghost", Kind: token.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", "Bearer "+ghost, ""), 403, "not_registered", "")

	// Expired on the clock.
	good := e.token("pat", key)
	e.clock.Advance(2 * time.Hour)
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", "Bearer "+good, ""), 401, "unauthenticated", "expired")

	// An assertion is not a bearer.
	a := e.assertionFor("pat", key, "http://inproc/v1/auth/token", 0)
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", "Bearer "+a, ""), 401, "unauthenticated", "unknown issuer")
}

// TestRegisterUnsetModeRefuses: a server built without a recognized
// registration mode (only server.New called directly can be) refuses every
// register with 403 forbidden and says why; it never opens, and no row is
// created (Decision 21, Eric 2026-10-23).
func TestRegisterUnsetModeRefuses(t *testing.T) {
	key := genKey(t, keys.EdDSA)
	for _, tc := range []struct {
		name   string
		mode   identities.Mode
		detail string
	}{
		{"unset", "", "registration refused: this server has no registration mode configured"},
		{"bogus", "bogus", `registration refused: registration mode "bogus" is not recognized`},
		{"closed is gone", "closed", `registration refused: registration mode "closed" is not recognized`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, envOptions{issuer: true, registration: tc.mode})
			resp := e.register("pat", key, "")
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
				t.Fatalf("content type %q", ct)
			}
			p := readProblem(t, resp)
			if resp.StatusCode != 403 || string(p.Code) != "forbidden" || p.Detail == nil || *p.Detail != tc.detail {
				t.Fatalf("got %d %q %v, want 403 forbidden %q", resp.StatusCode, p.Code, p.Detail, tc.detail)
			}
			err := e.store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
				_, err := tx.Identity(ctx, "pat")
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("identity row after a refused register: %v", err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRegisterModes(t *testing.T) {
	key := genKey(t, keys.EdDSA)

	t.Run("enrollment", func(t *testing.T) {
		e := newEnv(t, envOptions{issuer: true, registration: identities.ModeEnrollment})
		wantAuthProblem(t, e.register("pat", key, ""), 403, "forbidden", "")
		wantAuthProblem(t, e.register("pat", key, "bogus"), 403, "forbidden", "")
		tok, _, err := identities.Service{Core: e.core}.CreateEnrollmentToken(context.Background(), identities.TokenInput{})
		if err != nil {
			t.Fatal(err)
		}
		if resp := e.register("pat", key, tok); resp.StatusCode != 201 {
			t.Fatalf("with token: %d %s", resp.StatusCode, readBody(t, resp))
		}
	})
	t.Run("open refusals", func(t *testing.T) {
		e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
		// Private key material.
		priv, err := jose.JSONWebKey{Key: key, KeyID: kidOf(t, key)}.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		body := `{"identity":"pat","public_key_jwk":` + string(priv) + `}`
		wantAuthProblem(t, e.do("POST", "/v1/auth/register", "application/json", "", body), 400, "validation", "public_key_jwk")
		// An empty JWK.
		wantAuthProblem(t, e.do("POST", "/v1/auth/register", "application/json", "", `{"identity":"pat","public_key_jwk":{}}`), 400, "validation", "public_key_jwk")
		// A bad name.
		wantAuthProblem(t, e.register("a/b", key, ""), 400, "validation", "")
		// A second key for pat.
		e.mustRegister("pat", key)
		wantAuthProblem(t, e.register("pat", genKey(t, keys.EdDSA), ""), 409, "conflict", "")
		// The same key again is not an error.
		if resp := e.register("pat", key, ""); resp.StatusCode != 201 {
			t.Fatalf("same key again: %d", resp.StatusCode)
		}
	})
}

// TestRegisterUnboundTokenKeylessAdmin is post-build decision 1 on the wire:
// an unbound token for a keyless administrator row answers 403 forbidden and
// is not consumed, so it still enrolls a fresh name afterwards.
func TestRegisterUnboundTokenKeylessAdmin(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeEnrollment})
	svc := identities.Service{Core: e.core}
	if err := svc.RegisterLocal(ctx, "root"); err != nil {
		t.Fatal(err)
	}
	if err := svc.MakeAdmin(ctx, "root", true); err != nil {
		t.Fatal(err)
	}
	tok, _, err := svc.CreateEnrollmentToken(ctx, identities.TokenInput{})
	if err != nil {
		t.Fatal(err)
	}
	wantAuthProblem(t, e.register("root", genKey(t, keys.EdDSA), tok), 403, "forbidden", `identity "root" requires an enrollment token bound to it`)
	if resp := e.register("pat", genKey(t, keys.EdDSA), tok); resp.StatusCode != 201 {
		t.Fatalf("the unconsumed token on a fresh name: %d %s", resp.StatusCode, readBody(t, resp))
	}
}

// The token endpoint accepts an assertion whose audience is the issuer URL
// (draft-ietf-oauth-rfc7523bis) or, for shipped clients, the token endpoint.
func TestTokenAcceptsIssuerOrTokenEndpointAudience(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	pat := genKey(t, keys.EdDSA)
	e.mustRegister("pat", pat)
	for _, aud := range []string{"http://inproc", "http://inproc/v1/auth/token"} {
		if resp := e.exchange(e.assertionFor("pat", pat, aud, 0), ""); resp.StatusCode != 200 {
			t.Errorf("aud %q: %d %s", aud, resp.StatusCode, readBody(t, resp))
		}
	}
}

func TestIssuerIsConfiguredNotRequestHost(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true})
	req := newReq(t, "GET", "/.well-known/openid-configuration", nil)
	req.Host = "other.example"
	resp := e.raw("", req)
	var c gen.AuthorizationServerMetadata
	if err := json.Unmarshal([]byte(readBody(t, resp)), &c); err != nil {
		t.Fatal(err)
	}
	if c.Issuer != "http://inproc" || c.TokenEndpoint != "http://inproc/v1/auth/token" || c.JwksUri != "http://inproc/v1/auth/jwks" {
		t.Errorf("%+v", c)
	}
	resp = e.do("GET", "/v1/auth/jwks", "", "", "")
	var set gen.JWKS
	if err := json.Unmarshal([]byte(readBody(t, resp)), &set); err != nil {
		t.Fatal(err)
	}
	if len(set.Keys) != 1 || set.Keys[0]["kid"] != e.issuer.KID() {
		t.Errorf("jwks %+v", set)
	}
	if _, private := set.Keys[0]["d"]; private {
		t.Errorf("jwks leaks private material")
	}

	n := newEnv(t, envOptions{})
	req = newReq(t, "GET", "/.well-known/openid-configuration", nil)
	req.Host = "other.example"
	resp = n.raw("", req)
	c = gen.AuthorizationServerMetadata{}
	if err := json.Unmarshal([]byte(readBody(t, resp)), &c); err != nil {
		t.Fatal(err)
	}
	if c.Issuer != "http://other.example" {
		t.Errorf("without an issuer: %+v", c)
	}
	wantAuthProblem(t, n.do("GET", "/v1/auth/jwks", "", "", ""), 503, "unavailable", "token issuer not configured")
	wantOAuthError(t, n.exchange("x.y.z", ""), 503, "temporarily_unavailable", "token issuer not configured", "")
}

// TestBearerRefusesCorporateToken: a provider token that the configured
// External would accept is no bearer (Decision 10), even in open registration,
// and it creates no identity.
func TestBearerRefusesCorporateToken(t *testing.T) {
	extKey := genKey(t, keys.ES256)
	jwk, err := keys.ToJWK(extKey.Public(), kidOf(t, extKey))
	if err != nil {
		t.Fatal(err)
	}
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(srv.Close)
	ext := &token.External{Name: "corp", Issuer: "https://idp.example", JWKSURI: srv.URL, Audience: "agentbus2"}
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: extKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kidOf(t, extKey)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(sg).Claims(jwt.Claims{
		Issuer: ext.Issuer, Subject: "sub-1", Audience: jwt.Audience{"agentbus2"}, ID: "j",
		Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).Claims(map[string]any{"email": "pat@corp.example"}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	// The token verifies against the External, so only the bearer path refuses it.
	if f, err := ext.Verify(context.Background(), raw); err != nil || f.Email != "pat@corp.example" {
		t.Fatalf("External.Verify = %+v, %v", f, err)
	}

	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen, external: []*token.External{ext}})
	before := identityCount(t, e)
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", "Bearer "+raw, ""), 401, "unauthenticated", "unknown issuer")
	if after := identityCount(t, e); after != before {
		t.Fatalf("identities %d -> %d: a corporate bearer created a row", before, after)
	}

	resp := e.do("GET", "/v1/discover", "", "", "")
	var d gen.Discovery
	if err := json.Unmarshal([]byte(readBody(t, resp)), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Issuers) != 1 || d.Issuers[0] != ext.Issuer {
		t.Errorf("issuers %v", d.Issuers)
	}
}

func identityCount(t *testing.T, e *env) int {
	t.Helper()
	var n int
	err := e.store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		ids, err := tx.ListIdentities(ctx)
		n = len(ids)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInProcessStillWorks(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	r, err := e.client("pat").ListChannelsWithResponse(context.Background())
	if err != nil || r.StatusCode() != 200 {
		t.Fatalf("%v %v", r, err)
	}
}

// TestTokenReplayInsideLeeway: an assertion is accepted until exp plus
// token.Leeway, so the replay set must remember its jti that long. A replay
// after exp but inside the leeway is refused.
func TestTokenReplayInsideLeeway(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	pat := genKey(t, keys.EdDSA)
	e.mustRegister("pat", pat)
	a := e.assertionFor("pat", pat, "http://inproc/v1/auth/token", time.Minute)
	if resp := e.exchange(a, ""); resp.StatusCode != 200 {
		t.Fatalf("first use: %d %s", resp.StatusCode, readBody(t, resp))
	}
	e.clock.Advance(time.Minute + token.Leeway/2) // past exp, inside the leeway
	wantOAuthError(t, e.exchange(a, ""), 401, "invalid_client", "assertion replayed", "")

	// The last accepted instant, exp + Leeway exactly, is still a replay.
	b := e.assertionFor("pat", pat, "http://inproc/v1/auth/token", time.Minute)
	p, err := assertion.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if resp := e.exchange(b, ""); resp.StatusCode != 200 {
		t.Fatalf("first use of b: %d %s", resp.StatusCode, readBody(t, resp))
	}
	e.clock.Advance(p.Expiry.Add(token.Leeway).Sub(e.clock.Now()))
	// The expiry job running at that instant (on any replica) must keep b's
	// row: it removes a's only.
	if n, err := maintenance.New(e.core).RunJob(context.Background(), "assertion replay expiry"); err != nil || n != 1 {
		t.Fatalf("expiry job at exp+Leeway: %d, %v; want 1 (a's row only)", n, err)
	}
	wantOAuthError(t, e.exchange(b, ""), 401, "invalid_client", "assertion replayed", "")
	// Past the retention the row goes, and Verify refuses b as expired.
	e.clock.Advance(time.Second + time.Millisecond)
	if n, err := maintenance.New(e.core).RunJob(context.Background(), "assertion replay expiry"); err != nil || n != 1 {
		t.Fatalf("expiry job past retention: %d, %v; want 1", n, err)
	}
	wantOAuthError(t, e.exchange(b, ""), 401, "invalid_client", "expired", "")
}

// TestTokenReplayIsShared: two server instances (replicas) over one database
// share the assertion record. Milestone 5's per-process set answered 200
// twice.
func TestTokenReplayIsShared(t *testing.T) {
	e1 := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	e2 := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen, dbPath: e1.dbPath})
	pat := genKey(t, keys.EdDSA)
	e1.mustRegister("pat", pat)
	a := e1.assertionFor("pat", pat, "http://inproc/v1/auth/token", 0)
	if resp := e1.exchange(a, ""); resp.StatusCode != 200 {
		t.Fatalf("first replica: %d %s", resp.StatusCode, readBody(t, resp))
	}
	wantOAuthError(t, e2.exchange(a, ""), 401, "invalid_client", "assertion replayed", "")
	wantOAuthError(t, e1.exchange(a, ""), 401, "invalid_client", "assertion replayed", "")
}

// wantOAuthError checks a token endpoint refusal: RFC 6749 section 5.2 body
// (error and error_description only), application/json, and no assertion
// text in the description.
func wantOAuthError(t *testing.T, resp *http.Response, status int, code, detail, secret string) {
	t.Helper()
	b := readBody(t, resp)
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q, body %q", ct, b)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(b), &m); err != nil {
		t.Fatalf("body %q: %v", b, err)
	}
	for k := range m {
		if k != "error" && k != "error_description" {
			t.Fatalf("unexpected member %q in %q", k, b)
		}
	}
	if resp.StatusCode != status || m["error"] != code {
		t.Fatalf("got %d %v, want %d %q (body %q)", resp.StatusCode, m["error"], status, code, b)
	}
	if d, _ := m["error_description"].(string); detail != "" && !strings.Contains(d, detail) {
		t.Fatalf("error_description %q does not contain %q", d, detail)
	}
	if secret != "" && strings.Contains(b, secret) {
		t.Fatalf("error body carries assertion text: %q", b)
	}
}

func TestTokenOAuthErrors(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	key := genKey(t, keys.EdDSA)
	e.mustRegister("pat", key)
	const aud = "http://inproc"
	const formCT = "application/x-www-form-urlencoded"
	a := e.assertionFor("pat", key, aud, 0)

	post := func(v url.Values) *http.Response {
		return e.do("POST", "/v1/auth/token", formCT, "", v.Encode())
	}
	cc := func(extra map[string]string) url.Values {
		v := url.Values{"grant_type": {"client_credentials"}, "client_assertion_type": {assertion.Type}, "client_assertion": {a}}
		for k, s := range extra {
			v.Set(k, s)
		}
		return v
	}

	wantOAuthError(t, post(url.Values{"client_assertion": {a}}), 400, "invalid_request", "the request is not a well-formed application/x-www-form-urlencoded token request with a grant_type", a)
	wantOAuthError(t, post(url.Values{"grant_type": {""}, "client_assertion": {a}}), 400, "invalid_request", "grant_type is required", a)
	wantOAuthError(t, post(cc(map[string]string{"grant_type": "password"})), 400, "unsupported_grant_type", `grant_type "password" is not supported`, a)
	wantOAuthError(t, e.do("POST", "/v1/auth/token", "application/json", "", `{"grant_type":"client_credentials","client_assertion":"x"}`), 400, "invalid_request", "the request is not a well-formed application/x-www-form-urlencoded token request with a grant_type", "")
	wantOAuthError(t, post(cc(map[string]string{"client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:saml2-bearer"})), 401, "invalid_client", "client_assertion_type must be "+assertion.Type, a)
	missing := cc(nil)
	missing.Del("client_assertion")
	wantOAuthError(t, post(missing), 401, "invalid_client", "client_assertion is required", "")
	wantOAuthError(t, post(cc(map[string]string{"client_assertion": "not-a-jwt.at.all"})), 401, "invalid_client", "client assertion: assertion: malformed", "not-a-jwt.at.all")
	wantOAuthError(t, post(cc(map[string]string{"client_id": "kim"})), 401, "invalid_client", "client_id does not match the assertion issuer", a)

	// Replay: the first use mints, the second is refused.
	if resp := post(cc(nil)); resp.StatusCode != 200 {
		t.Fatalf("first use: %d %s", resp.StatusCode, readBody(t, resp))
	}
	wantOAuthError(t, post(cc(nil)), 401, "invalid_client", "assertion replayed", a)

	// An unconfigured issuer is 503 temporarily_unavailable.
	off := newEnv(t, envOptions{registration: identities.ModeOpen})
	resp := off.do("POST", "/v1/auth/token", formCT, "", cc(nil).Encode())
	wantOAuthError(t, resp, 503, "temporarily_unavailable", "token issuer not configured", a)
}

// A store failure behind the token endpoint is a 500 server_error whose
// description is fixed: it never carries the underlying text (oauthFromBus).
func TestTokenServerError(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	key := genKey(t, keys.EdDSA)
	e.mustRegister("pat", key)
	a := e.assertionFor("pat", key, "http://inproc", 0)
	if err := e.store.Close(); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_assertion_type": {assertion.Type}, "client_assertion": {a}}
	resp := e.do("POST", "/v1/auth/token", "application/x-www-form-urlencoded", "", form.Encode())
	b := readBody(t, resp)
	if resp.StatusCode != 500 || resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	if want := `{"error":"server_error","error_description":"internal error"}`; strings.TrimSpace(b) != want {
		t.Fatalf("body %q, want %q", b, want)
	}
}

// The token endpoint answers every refusal, including the ones made before
// the handler can run, with the RFC 6749 section 5.2 body (ADR 0002 audit,
// Decision 84): a wrong method is 405 with Allow, an oversized body is 413 and
// a path that is not an operation (the trailing slash) is 404.
func TestTokenEndpointPreHandlerRefusals(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	const formCT = "application/x-www-form-urlencoded"
	for _, m := range []string{"GET", "PUT", "DELETE", "PATCH"} {
		t.Run(m, func(t *testing.T) {
			resp := e.do(m, "/v1/auth/token", "", "", "")
			if allow := resp.Header.Get("Allow"); allow != "POST" {
				t.Errorf("Allow = %q", allow)
			}
			wantOAuthError(t, resp, 405, "invalid_request", "POST", "")
		})
	}
	t.Run("oversize", func(t *testing.T) {
		resp := e.do("POST", "/v1/auth/token", formCT, "", "grant_type="+strings.Repeat("a", 3<<20))
		wantOAuthError(t, resp, 413, "invalid_request", "request body exceeds", "")
	})
	t.Run("trailing-slash", func(t *testing.T) {
		resp := e.do("POST", "/v1/auth/token/", formCT, "", "grant_type=client_credentials")
		wantOAuthError(t, resp, 404, "invalid_request", "no such endpoint; the token endpoint is /v1/auth/token", "")
	})
	t.Run("other-paths-keep-problem-json", func(t *testing.T) {
		resp := e.do("POST", "/v1/auth/tokens", formCT, "", "x=1")
		if p := readProblem(t, resp); resp.StatusCode != 404 || p.Code != "not_found" {
			t.Fatalf("%d %+v", resp.StatusCode, p)
		}
	})
}

func TestTokenAcceptsClientIDAndScope(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	key := genKey(t, keys.EdDSA)
	e.mustRegister("pat", key)
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_assertion_type": {assertion.Type},
		"client_assertion":      {e.assertionFor("pat", key, "http://inproc", 0)},
		"client_id":             {"pat"},
		"scope":                 {"anything at all"},
	}
	resp := e.do("POST", "/v1/auth/token", "application/x-www-form-urlencoded", "", form.Encode())
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, readBody(t, resp))
	}
}

func TestOtherEndpointsKeepProblemJSON(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	key := genKey(t, keys.EdDSA)
	e.mustRegister("pat", key)
	bearer := "Bearer " + e.token("pat", key)
	resp := e.do("POST", "/v1/channels", "application/json", bearer, `{not json`)
	p := readProblem(t, resp)
	if resp.StatusCode != 400 || p.Code != "validation" {
		t.Fatalf("%d %+v", resp.StatusCode, p)
	}
	// The register endpoint is not the token endpoint either.
	resp = e.do("POST", "/v1/auth/register", "application/json", "", `{}`)
	_ = readProblem(t, resp)
}

// A repeated form parameter is invalid_request (RFC 6749 sections 3.2, 5.2)
// on both grants; the description names the parameter and never a value. An
// empty client_id or scope is omitted (section 3.2).
func TestTokenRepeatedAndEmptyParams(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	key := genKey(t, keys.EdDSA)
	e.mustRegister("pat", key)
	const formCT = "application/x-www-form-urlencoded"
	a := e.assertionFor("pat", key, "http://inproc", 0)
	base := func() url.Values {
		return url.Values{"grant_type": {"client_credentials"}, "client_assertion_type": {assertion.Type}, "client_assertion": {a}}
	}
	post := func(v url.Values) *http.Response {
		return e.do("POST", "/v1/auth/token", formCT, "", v.Encode())
	}

	for _, tc := range []struct {
		name  string
		build func() url.Values
		param string
	}{
		{"audience", func() url.Values {
			v := base()
			v["audience"] = []string{"http://inproc", "https://evil.example"}
			return v
		}, "audience"},
		{"client_assertion", func() url.Values {
			v := base()
			v["client_assertion"] = []string{a, "second-secret-value"}
			return v
		}, "client_assertion"},
		{"exchange resource", func() url.Values {
			return url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"}, "subject_token": {"x"},
				"resource": {"http://inproc", "https://evil.example"}}
		}, "resource"},
	} {
		resp := post(tc.build())
		body := readBody(t, resp)
		if resp.StatusCode != 400 || !strings.Contains(body, `"invalid_request"`) || !strings.Contains(body, tc.param) {
			t.Errorf("%s: %d %s", tc.name, resp.StatusCode, body)
		}
		for _, leak := range []string{a, "evil.example", "second-secret-value"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: body echoes %q: %s", tc.name, leak, body)
			}
		}
	}

	// A hostile parameter name is clipped in the description, not echoed whole.
	long := strings.Repeat("p", 5000)
	hv := base()
	hv[long] = []string{"1", "2"}
	hb := readBody(t, post(hv))
	if !strings.Contains(hb, `"invalid_request"`) || strings.Contains(hb, strings.Repeat("p", maxLogged+1)) || len(hb) > 1000 {
		t.Errorf("long parameter name echoed (%d bytes): %.300s", len(hb), hb)
	}

	gv := base()
	gv.Set("grant_type", strings.Repeat("g", 5000))
	if gb := readBody(t, post(gv)); !strings.Contains(gb, "unsupported_grant_type") || len(gb) > 1000 {
		t.Errorf("long grant_type echoed (%d bytes): %.300s", len(gb), gb)
	}

	// Empty client_id and scope are treated as omitted.
	v := base()
	v.Set("client_id", "")
	v.Set("scope", "")
	if resp := post(v); resp.StatusCode != 200 {
		t.Fatalf("empty client_id: %d %s", resp.StatusCode, readBody(t, resp))
	}
}

// failingKeys is a KeySource whose signer errs (or, with none set, is absent).
type failingKeys struct {
	key    crypto.Signer
	noSign bool
}

type errSigner struct{ crypto.Signer }

func (errSigner) SignContext(context.Context, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("kms down")
}

func (f failingKeys) LoadKeys(ctx context.Context) (token.KeySet, error) {
	set, err := token.StaticKeys(f.key).LoadKeys(ctx)
	if err != nil {
		return set, err
	}
	if f.noSign {
		set.Signer = nil
	} else {
		set.Signer = errSigner{f.key}
	}
	return set, nil
}

// TestSignerUnavailableIs503 (signing-keys Task 2): when the issuer cannot
// sign, the agent token endpoint and the exchange answer 503
// temporarily_unavailable, and a bearer minted earlier still authenticates.
func TestSignerUnavailableIs503(t *testing.T) {
	const want = "the signing key is unavailable"
	for _, noSign := range []bool{false, true} {
		name := "signer errs"
		if noSign {
			name = "no signer"
		}
		t.Run(name, func(t *testing.T) {
			break_ := func(e *env) {
				broken, err := token.NewIssuer(context.Background(), "http://inproc",
					failingKeys{key: genKey(t, keys.EdDSA), noSign: noSign}, time.Hour, e.clock.Now)
				if err != nil {
					t.Fatal(err)
				}
				e.opts.Issuer = broken
				e.once = sync.Once{} // rebuild the handler over the broken issuer
			}
			e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
			key := genKey(t, keys.EdDSA)
			e.mustRegister("pat", key)
			bearer := e.token("pat", key)
			break_(e)
			wantOAuthError(t, e.exchange(e.assertionFor("pat", key, "http://inproc/v1/auth/token", 0), ""), 503, "temporarily_unavailable", want, "")
			if resp := e.do("GET", "/v1/channels", "", "Bearer "+bearer, ""); resp.StatusCode != 200 {
				t.Errorf("earlier bearer: %d %s", resp.StatusCode, readBody(t, resp))
			}

			f := newFedEnv(t, 0, nil)
			break_(f.env)
			status, body, m := f.post(exchangeForm(f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric}), nil))
			if status != 503 || m["error"] != "temporarily_unavailable" || m["error_description"] != want {
				t.Errorf("exchange = %d %s", status, body)
			}
		})
	}
}

// TestSignerUnavailableIsLogged: both grant paths answer the unchanged 503 and
// leave the signer's error in the server log, once, never the bearer.
func TestSignerUnavailableIsLogged(t *testing.T) {
	break_ := func(e *env, lg *logBuf) {
		e.opts.Logger = slog.New(slog.NewTextHandler(lg, &slog.HandlerOptions{Level: slog.LevelDebug}))
		broken, err := token.NewIssuer(context.Background(), "http://inproc",
			failingKeys{key: genKey(t, keys.EdDSA)}, time.Hour, e.clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		e.opts.Issuer = broken
		e.once = sync.Once{}
	}
	t.Run("agent assertion", func(t *testing.T) {
		lg := &logBuf{}
		e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
		key := genKey(t, keys.EdDSA)
		e.mustRegister("pat", key)
		break_(e, lg)
		wantOAuthError(t, e.exchange(e.assertionFor("pat", key, "http://inproc/v1/auth/token", 0), ""), 503, "temporarily_unavailable", "the signing key is unavailable", "")
		if got := strings.Count(lg.String(), "kms down"); got != 1 {
			t.Errorf("log holds the signer error %d times: %s", got, lg.String())
		}
	})
	t.Run("token exchange", func(t *testing.T) {
		f := newFedEnv(t, 0, nil)
		lg := f.log
		break_(f.env, lg)
		status, body, m := f.post(exchangeForm(f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric}), nil))
		if status != 503 || m["error_description"] != "the signing key is unavailable" {
			t.Errorf("exchange = %d %s", status, body)
		}
		if got := strings.Count(lg.String(), "kms down"); got != 1 {
			t.Errorf("log holds the signer error %d times: %s", got, lg.String())
		}
	})
}
