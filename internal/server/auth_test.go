package server

import (
	"context"
	"crypto"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
		Identity: name, TokenEndpoint: aud, Key: key, KID: kidOf(e.t, key), Now: e.clock.Now(), Lifetime: life,
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
	const aud = "http://inproc/v1/auth/token"

	// kim's key signing as pat, naming kim's kid.
	foreign, err := assertion.Sign(assertion.Input{Identity: "pat", TokenEndpoint: aud, Key: kim, KID: kidOf(t, kim), Now: e.clock.Now()})
	if err != nil {
		t.Fatal(err)
	}
	wantAuthProblem(t, e.exchange(foreign, ""), 401, "unauthenticated", "unknown identity or key")

	// Replay.
	a := e.assertionFor("pat", pat, aud, 0)
	if resp := e.exchange(a, ""); resp.StatusCode != 200 {
		t.Fatalf("first use: %d", resp.StatusCode)
	}
	wantAuthProblem(t, e.exchange(a, ""), 401, "unauthenticated", "assertion replayed")

	// Audience is the token endpoint, not the issuer.
	wantAuthProblem(t, e.exchange(e.assertionFor("pat", pat, "http://inproc", 0), ""), 401, "unauthenticated", "audience")
	// A signature by the right identity's key but a wrong kid.
	wrongKID, err := assertion.Sign(assertion.Input{Identity: "pat", TokenEndpoint: aud, Key: pat, KID: "nope", Now: e.clock.Now()})
	if err != nil {
		t.Fatal(err)
	}
	wantAuthProblem(t, e.exchange(wrongKID, ""), 401, "unauthenticated", "unknown identity or key")
	// Expired by more than the leeway.
	stale, err := assertion.Sign(assertion.Input{Identity: "pat", TokenEndpoint: aud, Key: pat, KID: kidOf(t, pat), Now: e.clock.Now().Add(-10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	wantAuthProblem(t, e.exchange(stale, ""), 401, "unauthenticated", "")
	// A lifetime above the cap cannot be signed by the client library, so forge one.
	long := forgedAssertion(t, pat, "pat", aud, e.clock.Now(), 20*time.Minute)
	wantAuthProblem(t, e.exchange(long, ""), 401, "unauthenticated", "")

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
	wantAuthProblem(t, e.exchange(raw, ""), 400, "validation", "client assertion")
	wantAuthProblem(t, e.exchange("not-a-jwt", ""), 400, "validation", "client assertion")

	// Validator-level refusals.
	form := url.Values{"grant_type": {"password"}, "client_assertion_type": {assertion.Type}, "client_assertion": {a}}
	wantAuthProblem(t, e.do("POST", "/v1/auth/token", "application/x-www-form-urlencoded", "", form.Encode()), 400, "validation", "")
	wantAuthProblem(t, e.do("POST", "/v1/auth/token", "application/json", "", `{"client_assertion":"x"}`), 400, "validation", "")
	form = url.Values{"grant_type": {"client_credentials"}, "client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:saml2-bearer"}, "client_assertion": {a}}
	wantAuthProblem(t, e.do("POST", "/v1/auth/token", "application/x-www-form-urlencoded", "", form.Encode()), 400, "validation", "")
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
	other, err := token.NewIssuer("http://inproc", genKey(t, keys.EdDSA), time.Hour, e.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	forged, _, err := other.Mint("pat")
	if err != nil {
		t.Fatal(err)
	}
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", "Bearer "+forged, ""), 401, "unauthenticated", "unknown key")

	// The authenticator attaches; the core judges registration.
	ghost, _, err := e.issuer.Mint("ghost")
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

func TestRegisterModes(t *testing.T) {
	key := genKey(t, keys.EdDSA)

	t.Run("closed", func(t *testing.T) {
		e := newEnv(t, envOptions{issuer: true, registration: identities.ModeClosed})
		wantAuthProblem(t, e.register("pat", key, ""), 403, "forbidden", "")
	})
	t.Run("unset is closed", func(t *testing.T) {
		e := newEnv(t, envOptions{})
		wantAuthProblem(t, e.register("pat", key, ""), 403, "forbidden", "")
	})
	t.Run("unknown mode is refused", func(t *testing.T) {
		e := newEnv(t, envOptions{issuer: true, registration: identities.Mode("bogus")})
		wantAuthProblem(t, e.register("pat", key, ""), 403, "forbidden", "")
	})
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

func TestIssuerIsConfiguredNotRequestHost(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true})
	req := newReq(t, "GET", "/.well-known/openid-configuration", nil)
	req.Host = "other.example"
	resp := e.raw("", req)
	var c gen.OpenIDConfiguration
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
	c = gen.OpenIDConfiguration{}
	if err := json.Unmarshal([]byte(readBody(t, resp)), &c); err != nil {
		t.Fatal(err)
	}
	if c.Issuer != "http://other.example" {
		t.Errorf("without an issuer: %+v", c)
	}
	wantAuthProblem(t, n.do("GET", "/v1/auth/jwks", "", "", ""), 503, "unavailable", "token issuer not configured")
	wantAuthProblem(t, n.exchange("x.y.z", ""), 503, "unavailable", "token issuer not configured")
}

func TestExternalIssuerBearer(t *testing.T) {
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
	ext := &token.External{
		Issuer: "https://idp.example", JWKSURI: srv.URL, Audience: "agentbus2",
		Subjects: map[string]string{"sub-1": "pat", "sub-2": "kim"},
	}
	mint := func(sub, aud string) string {
		s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: extKey},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kidOf(t, extKey)))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := jwt.Signed(s).Claims(jwt.Claims{
			Issuer: ext.Issuer, Subject: sub, Audience: jwt.Audience{aud}, ID: "j-" + sub,
			Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	bearer := func(sub, aud string) string { return "Bearer " + mint(sub, aud) }

	e := newEnv(t, envOptions{issuer: true, external: []*token.External{ext}})
	e.identity("pat", false)
	if resp := e.do("GET", "/v1/channels", "", bearer("sub-1", "agentbus2"), ""); resp.StatusCode != 200 {
		t.Fatalf("mapped subject: %d %s", resp.StatusCode, readBody(t, resp))
	}
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", bearer("sub-9", "agentbus2"), ""), 401, "unauthenticated", "unmapped subject")
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", bearer("sub-1", "elsewhere"), ""), 401, "unauthenticated", "wrong audience")

	resp := e.do("GET", "/v1/discover", "", "", "")
	var d gen.Discovery
	if err := json.Unmarshal([]byte(readBody(t, resp)), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Issuers) != 1 || d.Issuers[0] != ext.Issuer {
		t.Errorf("issuers %v", d.Issuers)
	}

	hasRow := func(env *env, name string) bool {
		_, err := identityRow(env, name)
		return err == nil
	}

	// AutoCreate nil (enrollment and closed modes): no row appears.
	wantAuthProblem(t, e.do("GET", "/v1/channels", "", bearer("sub-2", "agentbus2"), ""), 403, "not_registered", "")
	if hasRow(e, "kim") {
		t.Fatal("kim was created without AutoCreate")
	}

	// AutoCreate set (open mode): the row appears once, without admin, with the general subscription.
	o := newEnv(t, envOptions{issuer: true, external: []*token.External{ext}, autoCreate: true})
	for i := 0; i < 2; i++ {
		if resp := o.do("GET", "/v1/channels", "", bearer("sub-2", "agentbus2"), ""); resp.StatusCode != 200 {
			t.Fatalf("auto-create run %d: %d %s", i, resp.StatusCode, readBody(t, resp))
		}
	}
	row, err := identityRow(o, "kim")
	if err != nil || row.Admin {
		t.Fatalf("kim row %+v err %v", row, err)
	}
	subs, err := subscriptionsOf(o, "kim")
	if err != nil || len(subs) != 1 {
		t.Fatalf("kim subscriptions %v err %v", subs, err)
	}
	// An own-issuer token for a missing identity is never auto-created.
	ghost, _, err := o.issuer.Mint("ghost")
	if err != nil {
		t.Fatal(err)
	}
	wantAuthProblem(t, o.do("GET", "/v1/channels", "", "Bearer "+ghost, ""), 403, "not_registered", "")
	if hasRow(o, "ghost") {
		t.Fatal("ghost was created from an own-issuer token")
	}
}

func identityRow(e *env, name string) (store.Identity, error) {
	var out store.Identity
	err := e.store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.Identity(ctx, name)
		return err
	})
	return out, err
}

func subscriptionsOf(e *env, name string) ([]store.Subscription, error) {
	var out []store.Subscription
	err := e.store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.Subscriptions(ctx, name)
		return err
	})
	return out, err
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
	wantAuthProblem(t, e.exchange(a, ""), 401, "unauthenticated", "assertion replayed")

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
	wantAuthProblem(t, e.exchange(b, ""), 401, "unauthenticated", "assertion replayed")
	// Past the retention the row goes, and Verify refuses b as expired.
	e.clock.Advance(time.Second + time.Millisecond)
	if n, err := maintenance.New(e.core).RunJob(context.Background(), "assertion replay expiry"); err != nil || n != 1 {
		t.Fatalf("expiry job past retention: %d, %v; want 1", n, err)
	}
	wantAuthProblem(t, e.exchange(b, ""), 401, "unauthenticated", "expired")
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
	wantAuthProblem(t, e2.exchange(a, ""), 401, "unauthenticated", "assertion replayed")
	wantAuthProblem(t, e1.exchange(a, ""), 401, "unauthenticated", "assertion replayed")
}
