package gen_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/gen/spec"
)

func loadDoc(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := spec.GetSpec()
	if err != nil {
		t.Fatalf("GetSpec: %v", err)
	}
	return doc
}

func TestDocumentIs31AndValidates(t *testing.T) {
	doc := loadDoc(t)
	if doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi = %q, want 3.1.0", doc.OpenAPI)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	schemes := doc.Components.SecuritySchemes
	if len(schemes) != 1 {
		t.Fatalf("security schemes: %d, want 1", len(schemes))
	}
	if b := schemes["bearerAuth"]; b == nil || b.Value.Type != "http" || b.Value.Scheme != "bearer" {
		t.Fatalf("bearerAuth: %+v", b)
	}
	if len(doc.Security) != 1 {
		t.Fatalf("global security: %v", doc.Security)
	}
	names := map[string]bool{}
	for _, r := range doc.Security {
		for n := range r {
			names[n] = true
		}
	}
	if !names["bearerAuth"] || len(names) != 1 {
		t.Fatalf("global security names: %v", names)
	}
	for _, p := range []string{"/.well-known/agentbus2", "/.well-known/oauth-authorization-server", "/.well-known/openid-configuration", "/v1/discover"} {
		op := doc.Paths.Value(p).Get
		if op.Security == nil || len(*op.Security) != 0 {
			t.Fatalf("%s: security = %v, want an empty list", p, op.Security)
		}
	}
	code := doc.Components.Schemas["Problem"].Value.Properties["code"].Value
	want := []string{"validation", "not_found", "conflict", "forbidden", "not_registered", "rate_limited", "replaced", "session_lapsed", "session_closed", "unavailable", "internal", "unauthenticated"}
	if len(code.Enum) != len(want) {
		t.Fatalf("Problem.code enum: %v", code.Enum)
	}
	for i, w := range want {
		if code.Enum[i] != w {
			t.Fatalf("Problem.code enum[%d] = %v, want %s", i, code.Enum[i], w)
		}
	}
}

// lowerFirst undoes oapi-codegen v2.8.0 capitalizing each operationId in the
// embedded document (it names Go methods from them); the YAML keeps them as
// written, and nothing that reads the embedded document may match on case.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func TestOperationTable(t *testing.T) {
	want := map[string]string{
		"wellKnown":                   "GET /.well-known/agentbus2",
		"openidConfiguration":         "GET /.well-known/openid-configuration",
		"discover":                    "GET /v1/discover",
		"openSession":                 "POST /v1/sessions",
		"closeSession":                "DELETE /v1/sessions/{session}",
		"receive":                     "POST /v1/sessions/{session}/receive",
		"watch":                       "GET /v1/sessions/{session}/watch",
		"peek":                        "GET /v1/sessions/{session}/peek",
		"listIdentities":              "GET /v1/identities",
		"listChannels":                "GET /v1/channels",
		"createChannel":               "POST /v1/channels",
		"deleteChannel":               "DELETE /v1/channels/{channel}",
		"renameChannel":               "PATCH /v1/channels/{channel}",
		"getACL":                      "GET /v1/channels/{channel}/acl",
		"setACL":                      "PUT /v1/channels/{channel}/acl",
		"subscribe":                   "PUT /v1/channels/{channel}/subscription",
		"unsubscribe":                 "DELETE /v1/channels/{channel}/subscription",
		"listTagSubscriptions":        "GET /v1/tag-subscriptions",
		"subscribeTags":               "POST /v1/tag-subscriptions",
		"unsubscribeTags":             "DELETE /v1/tag-subscriptions",
		"sendMessage":                 "POST /v1/channels/{channel}/messages",
		"history":                     "GET /v1/channels/{channel}/messages",
		"search":                      "GET /v1/search",
		"getMemory":                   "GET /v1/memories/{id}",
		"editMemory":                  "POST /v1/memories/{id}/revisions",
		"listMemoryRevisions":         "GET /v1/memories/{id}/revisions",
		"deleteMemory":                "DELETE /v1/memories/{id}",
		"createTask":                  "POST /v1/channels/{channel}/tasks",
		"listTasks":                   "GET /v1/channels/{channel}/tasks",
		"getTask":                     "GET /v1/tasks/{id}",
		"updateTask":                  "PATCH /v1/tasks/{id}",
		"claimTask":                   "POST /v1/tasks/{id}/claim",
		"releaseTask":                 "POST /v1/tasks/{id}/release",
		"adminStatus":                 "GET /v1/admin/status",
		"adminUsage":                  "GET /v1/admin/usage",
		"token":                       "POST /v1/auth/token",
		"jwks":                        "GET /v1/auth/jwks",
		"register":                    "POST /v1/auth/register",
		"adminReset":                  "POST /v1/admin/reset",
		"resetCredential":             "POST /v1/admin/identities/{identity}/reset-credential",
		"createIdentityByEmail":       "POST /v1/admin/identities/by-email",
		"createAgentIdentity":         "POST /v1/admin/identities",
		"createScope":                 "POST /v1/admin/scopes",
		"listScopes":                  "GET /v1/admin/scopes",
		"getScope":                    "GET /v1/admin/scopes/{scope}",
		"doneScope":                   "POST /v1/admin/scopes/{scope}/done",
		"createBridge":                "POST /v1/admin/bridges",
		"listBridges":                 "GET /v1/admin/bridges",
		"getBridge":                   "GET /v1/admin/bridges/{channel}",
		"removeBridge":                "DELETE /v1/admin/bridges/{channel}",
		"authorizationServerMetadata": "GET /.well-known/oauth-authorization-server",
	}
	doc := loadDoc(t)
	got := map[string]string{}
	open, protected := 0, 0
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			id := lowerFirst(op.OperationID)
			if _, dup := got[id]; dup {
				t.Fatalf("duplicate operationId %s", id)
			}
			got[id] = method + " " + path
			if op.Security != nil && len(*op.Security) == 0 {
				open++
			} else if op.Security == nil {
				protected++
			} else {
				t.Fatalf("%s: unexpected own security %v", op.OperationID, *op.Security)
			}
		}
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: got %q, want %q", id, got[id], w)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("unexpected operation %s", id)
		}
	}
	if open != 7 || protected != 44 {
		t.Fatalf("security: %d open, %d protected; want 7 and 44", open, protected)
	}
}

// stub implements only the operations the re-test calls; the nil embedded
// interface makes any other call panic, and none is made.
type stub struct {
	gen.StrictServerInterface
	mu        sync.Mutex
	channel   string
	patterns  []string
	key       *string
	body      *gen.SendMessageJSONRequestBody
	called    int
	assertion string
}

func (s *stub) Token(_ context.Context, r gen.TokenRequestObject) (gen.TokenResponseObject, error) {
	s.mu.Lock()
	s.called++
	if r.Body.ClientAssertion != nil {
		s.assertion = *r.Body.ClientAssertion
	}
	s.mu.Unlock()
	return gen.Token200JSONResponse{AccessToken: "t", TokenType: "Bearer", ExpiresIn: 3600}, nil
}

func (s *stub) Register(_ context.Context, _ gen.RegisterRequestObject) (gen.RegisterResponseObject, error) {
	s.mu.Lock()
	s.called++
	s.mu.Unlock()
	return gen.Register201JSONResponse{Identity: "pat", Kid: "k", Algorithm: gen.EdDSA, Created: true}, nil
}

func (s *stub) AdminReset(_ context.Context, _ gen.AdminResetRequestObject) (gen.AdminResetResponseObject, error) {
	s.mu.Lock()
	s.called++
	s.mu.Unlock()
	return gen.AdminReset204Response{}, nil
}

func (s *stub) WellKnown(_ context.Context, _ gen.WellKnownRequestObject) (gen.WellKnownResponseObject, error) {
	s.mu.Lock()
	s.called++
	s.mu.Unlock()
	return gen.WellKnown200JSONResponse{Discover: "/v1/discover"}, nil
}

func (s *stub) Discover(_ context.Context, _ gen.DiscoverRequestObject) (gen.DiscoverResponseObject, error) {
	s.mu.Lock()
	s.called++
	s.mu.Unlock()
	return gen.Discover200JSONResponse{ApiVersions: []string{"v1"}, ServerVersion: "x", Capabilities: []string{}, AuthMethods: []gen.AuthMethod{}, Issuers: []string{}}, nil
}

func (s *stub) SendMessage(_ context.Context, r gen.SendMessageRequestObject) (gen.SendMessageResponseObject, error) {
	s.mu.Lock()
	s.called++
	s.channel, s.key, s.body = r.Channel, r.Params.IdempotencyKey, r.Body
	s.mu.Unlock()
	return gen.SendMessage201JSONResponse{Seq: 7}, nil
}

func (s *stub) History(_ context.Context, _ gen.HistoryRequestObject) (gen.HistoryResponseObject, error) {
	s.mu.Lock()
	s.called++
	s.mu.Unlock()
	return gen.History200JSONResponse{}, nil
}

func (s *stub) Subscribe(_ context.Context, r gen.SubscribeRequestObject) (gen.SubscribeResponseObject, error) {
	s.mu.Lock()
	s.called++
	s.channel = r.Channel
	s.mu.Unlock()
	return gen.Subscribe204Response{}, nil
}

func (s *stub) UnsubscribeTags(_ context.Context, r gen.UnsubscribeTagsRequestObject) (gen.UnsubscribeTagsResponseObject, error) {
	s.mu.Lock()
	s.called++
	s.patterns = r.Params.Pattern
	s.mu.Unlock()
	return gen.UnsubscribeTags204Response{}, nil
}

// validated builds the generated strict handler behind the kin-openapi
// request validator over the real document, with a bearer check that accepts
// only "Authorization: Bearer ok".
func validated(t *testing.T, s *stub) http.Handler {
	t.Helper()
	doc := loadDoc(t)
	doc.Servers = nil
	mux := http.NewServeMux()
	gen.HandlerFromMux(gen.NewStrictHandler(s, nil), mux)
	mw := nethttpmiddleware.OapiRequestValidatorWithOptions(doc, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc: func(_ context.Context, in *openapi3filter.AuthenticationInput) error {
				if in.RequestValidationInput.Request.Header.Get("Authorization") == "Bearer ok" {
					return nil
				}
				return openapi3filter.ErrAuthenticationServiceMissing
			},
		},
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, opts nethttpmiddleware.ErrorHandlerOpts) {
			http.Error(w, err.Error(), opts.StatusCode)
		},
	})
	return mw(mux)
}

func do(t *testing.T, h http.Handler, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var tok = map[string]string{"Authorization": "Bearer ok"}

func TestValidatorOnRealDocument(t *testing.T) {
	s := &stub{}
	h := validated(t, s)

	if rec := do(t, h, "GET", "/v1/channels/dev/messages?count=abc", "", tok); rec.Code != 400 || s.called != 0 {
		t.Fatalf("count=abc: status %d called %d: %s", rec.Code, s.called, rec.Body)
	}

	rec := do(t, h, "POST", "/v1/channels/dev/messages", `{"content":"hi"}`, map[string]string{"Authorization": "Bearer ok", "Idempotency-Key": "k1"})
	if rec.Code != 201 || s.channel != "dev" || s.body == nil || s.body.Content != "hi" || s.key == nil || *s.key != "k1" {
		t.Fatalf("send with key: status %d channel %q body %+v key %v: %s", rec.Code, s.channel, s.body, s.key, rec.Body)
	}
	rec = do(t, h, "POST", "/v1/channels/dev/messages", `{"content":"hi"}`, tok)
	if rec.Code != 201 || s.key != nil {
		t.Fatalf("send without key: status %d key %v", rec.Code, s.key)
	}

	before := s.called
	if rec := do(t, h, "POST", "/v1/channels/dev/messages", `{"content":"hi"}`, nil); rec.Code != 401 || s.called != before {
		t.Fatalf("no token: status %d called %d: %s", rec.Code, s.called-before, rec.Body)
	}

	for _, p := range []string{"/v1/discover", "/.well-known/agentbus2"} {
		before = s.called
		if rec := do(t, h, "GET", p, "", nil); rec.Code != 200 || s.called != before+1 {
			t.Fatalf("%s without token: status %d: %s", p, rec.Code, rec.Body)
		}
	}

	form := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	const grant = "grant_type=client_credentials&client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	if rec := do(t, h, "POST", "/v1/auth/token", grant+"&client_assertion=a.b.c", form); rec.Code != 200 || s.assertion != "a.b.c" {
		t.Fatalf("token: status %d assertion %q: %s", rec.Code, s.assertion, rec.Body)
	}
	// Only a missing grant_type is a schema failure now; an unsupported grant
	// or a missing assertion is the handler's to refuse (RFC 6749 section 5.2).
	before = s.called
	if rec := do(t, h, "POST", "/v1/auth/token", "client_assertion=a.b.c", form); rec.Code != 400 || s.called != before {
		t.Fatalf("token without grant_type: status %d called %d: %s", rec.Code, s.called-before, rec.Body)
	}
	for name, body := range map[string]string{
		"no assertion": "grant_type=client_credentials",
		"password":     "grant_type=password&client_assertion=a.b.c",
	} {
		before = s.called
		if rec := do(t, h, "POST", "/v1/auth/token", body, form); rec.Code != 200 || s.called != before+1 {
			t.Fatalf("token %s: status %d called %d: %s", name, rec.Code, s.called-before, rec.Body)
		}
	}
	before = s.called
	if rec := do(t, h, "POST", "/v1/auth/register", `{"identity":"pat"}`, nil); rec.Code != 400 || s.called != before {
		t.Fatalf("register without key: status %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "POST", "/v1/admin/reset", "", nil); rec.Code != 401 || s.called != before {
		t.Fatalf("reset without token: status %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "POST", "/v1/admin/reset?scope=all", "", tok); rec.Code != 400 || s.called != before {
		t.Fatalf("reset scope=all: status %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "POST", "/v1/admin/reset?scope=everything", "", tok); rec.Code != 204 {
		t.Fatalf("reset scope=everything: status %d: %s", rec.Code, rec.Body)
	}

	rec = do(t, h, "PATCH", "/v1/sessions/x", `{}`, tok)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "method not allowed") {
		t.Fatalf("wrong method: status %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", "/v1/nope", "", tok); rec.Code != 404 {
		t.Fatalf("unknown path: status %d: %s", rec.Code, rec.Body)
	}
}

type recorder struct{ paths []string }

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.paths = append(r.paths, req.URL.EscapedPath())
	return &http.Response{StatusCode: 204, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
}

func TestPathParametersDecodeOnce(t *testing.T) {
	cases := []struct{ wire, name string }{
		{"odd%20name", "odd name"},
		{"100%25", "100%"},
		{"a%252Fb", "a%2Fb"},
		{"what%3F", "what?"},
		{"tag%231", "tag#1"},
		{"c++", "c++"},
		{"caf%C3%A9", "café"},
	}
	s := &stub{}
	h := validated(t, s)
	rt := &recorder{}
	c, err := gen.NewClientWithResponses("http://inproc", gen.WithHTTPClient(&http.Client{Transport: rt}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		rec := do(t, h, "PUT", "/v1/channels/"+tc.wire+"/subscription", `{}`, tok)
		if rec.Code != 204 || s.channel != tc.name {
			t.Errorf("%s: status %d bound %q, want %q: %s", tc.wire, rec.Code, s.channel, tc.name, rec.Body)
		}
		if _, err := c.SubscribeWithResponse(context.Background(), tc.name, &gen.SubscribeParams{}, gen.SubscribeJSONRequestBody{}); err != nil {
			t.Fatal(err)
		}
		if got, want := rt.paths[len(rt.paths)-1], "/v1/channels/"+tc.wire+"/subscription"; got != want {
			t.Errorf("client path for %q = %q, want %q", tc.name, got, want)
		}
	}

	rec := do(t, h, "PUT", "/v1/channels/a%2Fb/subscription", `{}`, tok)
	if rec.Code != 204 || s.channel != "a/b" {
		t.Errorf("a%%2Fb: status %d bound %q: %s", rec.Code, s.channel, rec.Body)
	}
	rec = do(t, h, "PUT", "/v1/channels//subscription", `{}`, tok)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "no matching operation was found") {
		t.Errorf("empty segment: status %d: %s", rec.Code, rec.Body)
	}

	rec = do(t, h, "DELETE", "/v1/tag-subscriptions?pattern=env%3A%2A%2Cx&pattern=deploy", "", tok)
	if rec.Code != 204 || len(s.patterns) != 2 || s.patterns[0] != "env:*,x" || s.patterns[1] != "deploy" {
		t.Errorf("tag patterns: status %d bound %q: %s", rec.Code, s.patterns, rec.Body)
	}
	if _, err := c.UnsubscribeTagsWithResponse(context.Background(), &gen.UnsubscribeTagsParams{Pattern: []string{"env:*", "deploy"}}); err != nil {
		t.Fatal(err)
	}
	if got, want := rt.paths[len(rt.paths)-1], "/v1/tag-subscriptions"; got != want {
		t.Errorf("client tag path = %q, want %q", got, want)
	}
	if rec = do(t, h, "DELETE", "/v1/tag-subscriptions", "", tok); rec.Code != 400 {
		t.Errorf("no pattern: status %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestProblemRoundTripsAsJSON(t *testing.T) {
	var p gen.Problem
	in := `{"type":"urn:agentbus2:problem:replaced","title":"Conflict","status":409,"code":"replaced","retryable":false}`
	if err := json.Unmarshal([]byte(in), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != gen.Replaced || p.Status != 409 {
		t.Fatalf("%+v", p)
	}
}
