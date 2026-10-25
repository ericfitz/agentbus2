package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

func readProblem(t *testing.T, resp *http.Response) gen.Problem {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("status %d content type %q (want application/problem+json), body %q", resp.StatusCode, ct, b)
	}
	var p gen.Problem
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("problem body %q: %v", b, err)
	}
	return p
}

func newReq(t *testing.T, method, path string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, "http://inproc"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func TestEveryFrameworkErrorIsProblem(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	limit := e.core.Cfg.MaxMessageKiB*1024 + 1<<20
	big := bytes.Repeat([]byte("a"), limit+1)

	type tc struct {
		name     string
		identity string
		req      func() *http.Request
		status   int
		code     string
		detail   string
	}
	cases := []tc{
		{"unknown path", "pat", func() *http.Request { return newReq(t, "GET", "/v1/nope", nil) }, 404, "not_found", ""},
		{"wrong method", "pat", func() *http.Request { return newReq(t, "PATCH", "/v1/sessions/x", nil) }, 404, "not_found", "method not allowed"},
		{"bad query", "pat", func() *http.Request { return newReq(t, "GET", "/v1/sessions/s/watch?timeout=abc", nil) }, 400, "validation", ""},
		{"wrong type", "pat", func() *http.Request { return newReq(t, "POST", "/v1/channels", strings.NewReader(`{"name":1}`)) }, 400, "validation", ""},
		{"not json", "pat", func() *http.Request { return newReq(t, "POST", "/v1/channels", strings.NewReader(`not json`)) }, 400, "validation", ""},
		{"enum outside", "pat", func() *http.Request {
			return newReq(t, "POST", "/v1/channels", strings.NewReader(`{"name":"a","kind":"dm"}`))
		}, 400, "validation", ""},
		{"oversized with length", "pat", func() *http.Request {
			r := newReq(t, "POST", "/v1/channels", bytes.NewReader(big))
			r.ContentLength = int64(len(big))
			return r
		}, 413, "validation", ""},
		{"oversized chunked", "pat", func() *http.Request {
			r := newReq(t, "POST", "/v1/channels", io.NopCloser(bytes.NewReader(big)))
			r.ContentLength = -1
			r.TransferEncoding = []string{"chunked"}
			return r
		}, 413, "validation", ""},
		{"oversized on open operation", "", func() *http.Request {
			r := newReq(t, "GET", "/v1/discover", io.NopCloser(bytes.NewReader(big)))
			r.ContentLength = -1
			return r
		}, 413, "validation", ""},
		{"oversized without identity", "", func() *http.Request {
			r := newReq(t, "POST", "/v1/channels", io.NopCloser(bytes.NewReader(big)))
			r.ContentLength = -1
			return r
		}, 413, "validation", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := e.raw(c.identity, c.req())
			p := readProblem(t, resp)
			if resp.StatusCode != c.status || string(p.Code) != c.code {
				t.Fatalf("got %d %s (%v), want %d %s", resp.StatusCode, p.Code, p.Detail, c.status, c.code)
			}
			if p.Status != c.status {
				t.Errorf("problem status member %d", p.Status)
			}
			if c.detail != "" && (p.Detail == nil || !strings.Contains(*p.Detail, c.detail)) {
				t.Errorf("detail %v does not contain %q", p.Detail, c.detail)
			}
		})
	}
}

func TestSecurityFromDocument(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	ctx := context.Background()

	anon := e.client("")
	r, err := anon.ListChannelsWithResponse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 401 || r.ApplicationproblemJSONDefault == nil || r.ApplicationproblemJSONDefault.Code != gen.ProblemCode("unauthenticated") {
		t.Fatalf("anonymous ListChannels: %d %s", r.StatusCode(), r.Body)
	}
	for name, get := range map[string]func() (int, error){
		"wellKnown": func() (int, error) { x, err := anon.WellKnownWithResponse(ctx); return x.StatusCode(), err },
		"discover":  func() (int, error) { x, err := anon.DiscoverWithResponse(ctx); return x.StatusCode(), err },
		"openid":    func() (int, error) { x, err := anon.OpenidConfigurationWithResponse(ctx); return x.StatusCode(), err },
	} {
		if code, err := get(); err != nil || code != 200 {
			t.Errorf("%s anonymous: %d %v", name, code, err)
		}
	}

	// With the registered identity the route is reached: the stub's 500
	// problem now, the real 200 once Task 6 lands. 401 and 404 would mean the
	// chain order or the document is wrong.
	resp := e.raw("pat", newReq(t, "GET", "/v1/channels", nil))
	switch resp.StatusCode {
	case 500:
		p := readProblem(t, resp)
		if p.Code != gen.ProblemCode("internal") || p.Detail == nil || !strings.HasPrefix(*p.Detail, "server.ListChannels:") {
			t.Fatalf("stub problem %+v", p)
		}
	case 200:
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("200 content type %q", ct)
		}
	default:
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("authenticated ListChannels: %d %s", resp.StatusCode, b)
	}
}

func TestNilAuthenticatorRefuses(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	h := New(e.core, Options{})
	rt := InProcessTransport(h, "pat")
	resp, err := rt.RoundTrip(newReq(t, "GET", "/v1/channels", nil))
	if err != nil {
		t.Fatal(err)
	}
	if p := readProblem(t, resp); resp.StatusCode != 401 || p.Code != gen.ProblemCode("unauthenticated") {
		t.Fatalf("got %d %+v", resp.StatusCode, p)
	}
	resp, err = rt.RoundTrip(newReq(t, "GET", "/v1/discover", nil))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("discover: %v %v", resp, err)
	}
}

func TestRefuseAllNeverAttaches(t *testing.T) {
	e := newEnv(t, envOptions{auth: RefuseAll{}})
	e.identity("pat", false)
	req := newReq(t, "GET", "/v1/channels", nil)
	req.Header.Set("Authorization", "Bearer anything")
	resp := e.raw("pat", req)
	if p := readProblem(t, resp); resp.StatusCode != 401 || p.Code != gen.ProblemCode("unauthenticated") {
		t.Fatalf("got %d %+v", resp.StatusCode, p)
	}
}

func TestIdentityKeysAreSeparate(t *testing.T) {
	ctx := withInprocIdentity(context.Background(), "pat")
	if got := IdentityFrom(ctx); got != "" {
		t.Fatalf("IdentityFrom saw the transport's key: %q", got)
	}
	id, err := InProcess{}.Authenticate(httptest.NewRequest("GET", "/", nil))
	if id != "" || err != nil {
		t.Fatalf("InProcess without the key: %q %v", id, err)
	}
	id, err = InProcess{}.Authenticate(httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	if id != "pat" || err != nil {
		t.Fatalf("InProcess with the key: %q %v", id, err)
	}
	if got := IdentityFrom(WithIdentity(context.Background(), "x")); got != "x" {
		t.Fatalf("WithIdentity round trip: %q", got)
	}
}

type authFunc func(*http.Request) (string, error)

func (f authFunc) Authenticate(r *http.Request) (string, error) { return f(r) }

func TestAuthenticateErrorIs401(t *testing.T) {
	e := newEnv(t, envOptions{auth: authFunc(func(*http.Request) (string, error) { return "", errors.New("token expired") })})
	resp := e.raw("", newReq(t, "GET", "/v1/channels", nil))
	p := readProblem(t, resp)
	// The validator may prefix the text, so the detail contains the reason.
	if resp.StatusCode != 401 || p.Code != gen.ProblemCode("unauthenticated") || p.Detail == nil || !strings.Contains(*p.Detail, "token expired") {
		t.Fatalf("got %d %+v", resp.StatusCode, p)
	}
	// An open operation ignores the error.
	if resp := e.raw("", newReq(t, "GET", "/v1/discover", nil)); resp.StatusCode != 200 {
		t.Fatalf("discover: %d", resp.StatusCode)
	}
}

func TestResponseErrorHookWritesTypedProblem(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/x", nil)

	rec := httptest.NewRecorder()
	responseError(rec, r, &bus.Error{Code: bus.CodeConflict, Message: "taken"})
	p := readProblem(t, rec.Result())
	if rec.Code != 409 || p.Code != gen.ProblemCode("conflict") || *p.Detail != "taken" || p.Retryable {
		t.Fatalf("conflict: %d %+v", rec.Code, p)
	}

	rec = httptest.NewRecorder()
	responseError(rec, r, context.Canceled)
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("canceled wrote %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	responseError(rec, r, errors.New("disk"))
	p = readProblem(t, rec.Result())
	if rec.Code != 500 || p.Code != gen.ProblemCode("internal") || !p.Retryable {
		t.Fatalf("internal: %d %+v", rec.Code, p)
	}
}

func TestInProcessTransportCancels(t *testing.T) {
	e := newEnv(t, envOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := newReq(t, "GET", "/v1/discover", nil).WithContext(ctx)
	if _, err := InProcessTransport(e.handler(), "").RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("RoundTrip err = %v", err)
	}
}

func TestLimitBodyPassesBoundedBodyThrough(t *testing.T) {
	var got []byte
	var n int64
	h := limitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		n = r.ContentLength
	}), 10)
	req := httptest.NewRequest("POST", "/", io.NopCloser(strings.NewReader("0123456789")))
	req.ContentLength = -1
	h.ServeHTTP(httptest.NewRecorder(), req)
	if string(got) != "0123456789" || n != 10 {
		t.Fatalf("handler saw %q len %d", got, n)
	}
}

func TestSpecLoads(t *testing.T) {
	s := Spec()
	if s == nil || len(s.Servers) != 0 || s.Paths == nil {
		t.Fatalf("Spec() = %+v", s)
	}
}

// A request body with a member the schema does not declare is a 400
// validation error (the additionalProperties: false convention), on a body
// that used to be open.
func TestRequestBodyUnknownMemberRefused(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	for path, body := range map[string]string{
		"/v1/channels": `{"name":"c1","nope":1}`,
		"/v1/sessions": `{"hint":"h","nope":1}`,
	} {
		st, b, _ := e.postBody("pat", path, body)
		if st != 400 || !strings.Contains(b, `"validation"`) {
			t.Errorf("%s: %d %s", path, st, b)
		}
	}
}
