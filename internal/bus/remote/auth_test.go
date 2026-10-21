package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/assertion"
	"github.com/ericfitz/agentbus2/internal/auth/keys"
	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// recorder answers with a canned response and keeps the last request.
type recorder struct {
	status int
	ctype  string
	body   string

	mu      sync.Mutex
	method  string
	path    string
	query   string
	header  http.Header
	reqBody []byte
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.method, r.path, r.query, r.header, r.reqBody = req.Method, req.URL.Path, req.URL.RawQuery, req.Header.Clone(), b
	r.mu.Unlock()
	w.Header().Set("Content-Type", r.ctype)
	w.WriteHeader(r.status)
	_, _ = w.Write([]byte(r.body))
}

func newRecClient(t *testing.T, r *recorder, opts ...gen.ClientOption) *gen.ClientWithResponses {
	t.Helper()
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	c, err := gen.NewClientWithResponses(srv.URL, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const conflictProblem = `{"type":"about:blank","title":"Conflict","status":409,"code":"conflict","detail":"identity already has a key","retryable":false}`
const unauthProblem = `{"type":"about:blank","title":"Unauthorized","status":401,"code":"unauthenticated","detail":"unknown identity or key","retryable":false}`

func TestRemoteRegisterSendsJWK(t *testing.T) {
	for _, alg := range []keys.Algorithm{keys.EdDSA, keys.ES256} {
		t.Run(string(alg), func(t *testing.T) {
			k, err := keys.Generate(alg)
			if err != nil {
				t.Fatal(err)
			}
			kid, _ := keys.KID(k.Public())
			rec := &recorder{status: 201, ctype: "application/json",
				body: `{"identity":"pat","kid":"` + kid + `","algorithm":"` + string(alg) + `","created":true}`}
			c := newRecClient(t, rec)
			res, err := Register(context.Background(), c, "pat", k.Public(), "")
			if err != nil {
				t.Fatal(err)
			}
			if res.Identity != "pat" || res.KID != kid || res.Algorithm != alg || !res.Created {
				t.Fatalf("result = %+v", res)
			}
			if rec.method != http.MethodPost || rec.path != "/v1/auth/register" {
				t.Fatalf("request = %s %s", rec.method, rec.path)
			}
			var got map[string]any
			if err := json.Unmarshal(rec.reqBody, &got); err != nil {
				t.Fatal(err)
			}
			jwk, _ := got["public_key_jwk"].(map[string]any)
			if got["identity"] != "pat" || jwk["kid"] != kid {
				t.Fatalf("body = %s", rec.reqBody)
			}
			wantKty := map[keys.Algorithm]string{keys.EdDSA: "OKP", keys.ES256: "EC"}[alg]
			if jwk["kty"] != wantKty {
				t.Fatalf("kty = %v, want %s", jwk["kty"], wantKty)
			}
			if _, ok := jwk["d"]; ok {
				t.Fatal("the JWK carries private material")
			}
			if _, ok := got["enrollment_token"]; ok {
				t.Fatalf("empty token was sent: %s", rec.reqBody)
			}
			if _, err := Register(context.Background(), c, "pat", k.Public(), "abe_x"); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(rec.reqBody, &got); err != nil || got["enrollment_token"] != "abe_x" {
				t.Fatalf("body = %s", rec.reqBody)
			}
		})
	}
}

func TestRemoteRegisterErrors(t *testing.T) {
	k, _ := keys.Generate(keys.EdDSA)
	c := newRecClient(t, &recorder{status: 409, ctype: "application/problem+json", body: conflictProblem})
	if _, err := Register(context.Background(), c, "pat", k.Public(), ""); ab2bus.CodeOf(err) != ab2bus.CodeConflict {
		t.Fatalf("err = %v, want conflict", err)
	}
	// An unsupported key type is refused before any request.
	rec := &recorder{status: 201, ctype: "application/json", body: `{}`}
	c = newRecClient(t, rec)
	if _, err := Register(context.Background(), c, "pat", "not a key", ""); ab2bus.CodeOf(err) != ab2bus.CodeValidation {
		t.Fatalf("err = %v, want validation", err)
	}
	if rec.method != "" {
		t.Fatal("a request was sent for an unusable key")
	}
	// A 2xx that is not the documented shape is internal.
	c = newRecClient(t, &recorder{status: 201, ctype: "text/plain", body: "ok"})
	if _, err := Register(context.Background(), c, "pat", k.Public(), ""); ab2bus.CodeOf(err) != ab2bus.CodeInternal {
		t.Fatalf("err = %v, want internal", err)
	}
	// A registration naming an algorithm outside Decision 55 is internal, not accepted.
	c = newRecClient(t, &recorder{status: 201, ctype: "application/json", body: `{"identity":"pat","kid":"k1","algorithm":"HS256","created":true}`})
	if _, err := Register(context.Background(), c, "pat", k.Public(), ""); ab2bus.CodeOf(err) != ab2bus.CodeInternal {
		t.Fatalf("err = %v, want internal for an unrecognized algorithm", err)
	}
}

func TestRemoteTokenSendsForm(t *testing.T) {
	rec := &recorder{status: 200, ctype: "application/json", body: `{"access_token":"tok","token_type":"Bearer","expires_in":3600}`}
	c := newRecClient(t, rec)
	at, err := Token(context.Background(), c, "a.b.c")
	if err != nil {
		t.Fatal(err)
	}
	if at.Token != "tok" || at.ExpiresIn.Seconds() != 3600 {
		t.Fatalf("token = %+v", at)
	}
	if got := rec.header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q", got)
	}
	if rec.path != "/v1/auth/token" {
		t.Fatalf("path = %s", rec.path)
	}
	form, err := url.ParseQuery(string(rec.reqBody))
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("grant_type") != "client_credentials" || form.Get("client_assertion") != "a.b.c" ||
		form.Get("client_assertion_type") != assertion.Type || len(form) != 3 {
		t.Fatalf("form = %v", form)
	}

	c = newRecClient(t, &recorder{status: 401, ctype: "application/problem+json", body: unauthProblem})
	if _, err := Token(context.Background(), c, "a.b.c"); ab2bus.CodeOf(err) != ab2bus.CodeUnauthenticated {
		t.Fatalf("err = %v, want unauthenticated", err)
	}
	c = newRecClient(t, &recorder{status: 200, ctype: "text/plain", body: `[]`})
	if _, err := Token(context.Background(), c, "a.b.c"); ab2bus.CodeOf(err) != ab2bus.CodeInternal {
		t.Fatalf("err = %v, want internal", err)
	}
}

func TestWithBearer(t *testing.T) {
	rec := &recorder{status: 200, ctype: "application/json", body: `[]`}
	c := newRecClient(t, rec, gen.WithRequestEditorFn(WithBearer("tok123")))
	if _, err := New(c).ListChannels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := rec.header.Get("Authorization"); got != "Bearer tok123" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestRemoteResetSendsScopeOnlyWhenSet(t *testing.T) {
	rec := &recorder{status: 204, ctype: "application/json"}
	b := New(newRecClient(t, rec))
	ctx := context.Background()
	cases := []struct {
		scope     ab2bus.ResetScope
		wantQuery string
	}{{"", ""}, {ab2bus.ResetData, "scope=data"}, {ab2bus.ResetEverything, "scope=everything"}}
	for _, tc := range cases {
		if err := b.Reset(ctx, ab2bus.ResetInput{Scope: tc.scope, IdempotencyKey: "k"}); err != nil {
			t.Fatalf("scope %q: %v", tc.scope, err)
		}
		if rec.query != tc.wantQuery || rec.path != "/v1/admin/reset" || rec.header.Get("Idempotency-Key") != "k" {
			t.Fatalf("scope %q: %s?%s key=%q", tc.scope, rec.path, rec.query, rec.header.Get("Idempotency-Key"))
		}
	}
	// Problems map through errorOf.
	b = New(newRecClient(t, &recorder{status: 403, ctype: "application/problem+json",
		body: `{"type":"about:blank","title":"Forbidden","status":403,"code":"forbidden","detail":"admin only","retryable":false}`}))
	if err := b.Reset(ctx, ab2bus.ResetInput{}); ab2bus.CodeOf(err) != ab2bus.CodeForbidden {
		t.Fatalf("err = %v, want forbidden", err)
	}
}

func TestRemoteResetCredential(t *testing.T) {
	rec := &recorder{status: 200, ctype: "application/json", body: `{"identity":"pat","keys_removed":2}`}
	b := New(newRecClient(t, rec))
	res, err := b.ResetCredential(context.Background(), ab2bus.ResetCredentialInput{Identity: "pat", IdempotencyKey: "k"})
	if err != nil || res.Identity != "pat" || res.KeysRemoved != 2 {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if rec.path != "/v1/admin/identities/pat/reset-credential" || rec.header.Get("Idempotency-Key") != "k" {
		t.Fatalf("request = %s key=%q", rec.path, rec.header.Get("Idempotency-Key"))
	}
	b = New(newRecClient(t, &recorder{status: 200, ctype: "text/plain", body: `[]`}))
	if _, err := b.ResetCredential(context.Background(), ab2bus.ResetCredentialInput{Identity: "pat"}); ab2bus.CodeOf(err) != ab2bus.CodeInternal {
		t.Fatalf("err = %v, want internal", err)
	}
}
