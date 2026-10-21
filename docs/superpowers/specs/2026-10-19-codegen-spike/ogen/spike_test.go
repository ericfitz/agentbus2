package ogen_test

// Spike test for ogen. The generated client talks to the generated *Server
// (an http.Handler) through an in-process http.RoundTripper backed by
// httptest.ResponseRecorder (decision 35).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ogen-go/ogen/ogenerrors"

	"spike/ogen/gen"
)

type inproc struct{ h http.Handler }

func (t inproc) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, req)
	// Mirror net/http: a canceled request context is an error, not a response.
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	return rec.Result(), nil
}

func problem(status int, code string, detail string) gen.Problem {
	return gen.Problem{Type: url.URL{Scheme: "https", Host: "agentbus2.dev", Path: "/problems/" + code},
		Title: http.StatusText(status), Status: status, Detail: gen.NewOptString(detail), Code: code, Retryable: status >= 500}
}

// bus implements gen.Handler by hand.
type bus struct {
	mu        sync.Mutex
	gotKey    string
	ctxErr    error
	blockDone chan struct{}
}

// NewError is generated on the Handler interface because every operation has
// the same default response ("convenient errors"). Any Go error a handler
// returns is turned into the default problem+json response here.
//
// Quirk: in this mode SecurityHandler failures also arrive here (not at
// WithErrorHandler), so the status must come from ogenerrors.ErrorCode.
func (b *bus) NewError(ctx context.Context, err error) *gen.ProblemStatusCode {
	code := ogenerrors.ErrorCode(err) // 401 for *ogenerrors.SecurityError, 500 otherwise
	kind := "internal"
	var secErr *ogenerrors.SecurityError
	if errors.As(err, &secErr) {
		kind = "unauthenticated"
	}
	return &gen.ProblemStatusCode{StatusCode: code, Response: problem(code, kind, err.Error())}
}

func (b *bus) WellKnown(ctx context.Context) (*gen.WellKnown, error) {
	return &gen.WellKnown{Discover: "/v1/discover"}, nil
}

func (b *bus) Discover(ctx context.Context) (*gen.Discovery, error) {
	return &gen.Discovery{APIVersions: []string{"v1"}, ServerVersion: "spike",
		Capabilities: []string{"long-poll"}, AuthMethods: []string{"private_key_jwt"}}, nil
}

func (b *bus) SendMessage(ctx context.Context, req *gen.NewMessage, params gen.SendMessageParams) (*gen.Message, error) {
	b.mu.Lock()
	b.gotKey = params.IdempotencyKey
	b.mu.Unlock()
	return &gen.Message{Seq: 7, Channel: params.Channel, Text: req.Text}, nil
}

func (b *bus) Watch(ctx context.Context, params gen.WatchParams) (gen.WatchRes, error) {
	switch params.Session {
	case "wake":
		return &gen.Wake{Seq: params.After + 1, Count: 1, Channels: []string{"general"}}, nil
	case "idle":
		return &gen.WatchNoContent{}, nil
	case "replaced":
		// *Problem is the 409 member of the WatchRes sum type.
		p := problem(409, "replaced", "")
		p.Retryable = false
		return &p, nil
	case "down":
		// With convenient errors the default response is not a WatchRes member;
		// it is returned as the error (*ProblemStatusCode implements error).
		return nil, &gen.ProblemStatusCode{StatusCode: 503, Response: problem(503, "store_unavailable", "")}
	case "boom":
		return nil, errors.New("disk on fire") // routed through NewError
	case "typed":
		return nil, &gen.ProblemStatusCode{StatusCode: 429, Response: problem(429, "rate_limited", "")} // *ProblemStatusCode is an error
	case "block":
		<-ctx.Done()
		b.mu.Lock()
		b.ctxErr = ctx.Err()
		b.mu.Unlock()
		close(b.blockDone)
		return &gen.WatchNoContent{}, nil
	}
	return nil, errors.New("unknown session")
}

// sec is the server-side auth hook ogen generates.
type sec struct {
	mu    sync.Mutex
	calls []string
}

func (s *sec) HandleBearerAuth(ctx context.Context, op gen.OperationName, t gen.BearerAuth) (context.Context, error) {
	s.mu.Lock()
	s.calls = append(s.calls, string(op)+":"+t.Token)
	s.mu.Unlock()
	if t.Token != "ok" {
		return ctx, errors.New("bad token")
	}
	return ctx, nil
}

// src is the client-side token provider.
type src struct{ token string }

func (s src) BearerAuth(ctx context.Context, _ gen.OperationName) (gen.BearerAuth, error) {
	return gen.BearerAuth{Token: s.token}, nil
}

// problemErrorHandler replaces ogen's default {"error_message":...} JSON with
// the spec's problem+json for framework errors (decode, security, 404).
func problemErrorHandler(ctx context.Context, w http.ResponseWriter, r *http.Request, err error) {
	code := ogenerrors.ErrorCode(err)
	kind := "bad_request"
	var secErr *ogenerrors.SecurityError
	if errors.As(err, &secErr) {
		kind = "unauthenticated"
	}
	p := &gen.ProblemStatusCode{StatusCode: code, Response: problem(code, kind, err.Error())}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(code)
	data, _ := p.Response.MarshalJSON()
	_, _ = w.Write(data)
}

func newServer(t *testing.T, b *bus, s *sec) *gen.Server {
	t.Helper()
	srv, err := gen.NewServer(b, s, gen.WithErrorHandler(problemErrorHandler))
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func newPair(t *testing.T, token string) (*bus, *sec, *gen.Client) {
	t.Helper()
	b := &bus{blockDone: make(chan struct{})}
	s := &sec{}
	srv := newServer(t, b, s)
	c, err := gen.NewClient("http://inproc", src{token}, gen.WithClient(&http.Client{Transport: inproc{srv}}))
	if err != nil {
		t.Fatal(err)
	}
	return b, s, c
}

func TestWatchRoundTrip(t *testing.T) {
	_, _, c := newPair(t, "ok")
	ctx := context.Background()

	res, err := c.Watch(ctx, gen.WatchParams{Session: "wake", After: 41})
	if err != nil {
		t.Fatal(err)
	}
	w, ok := res.(*gen.Wake)
	if !ok || w.Seq != 42 || w.Channels[0] != "general" {
		t.Fatalf("200: %T %+v", res, res)
	}

	res, err = c.Watch(ctx, gen.WatchParams{Session: "idle", After: 41})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.(*gen.WatchNoContent); !ok {
		t.Fatalf("204: %T", res)
	}

	res, err = c.Watch(ctx, gen.WatchParams{Session: "replaced", After: 41})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := res.(*gen.Problem)
	if !ok || p.Code != "replaced" || p.Status != 409 {
		t.Fatalf("409: %T %+v", res, res)
	}

	// default: comes back as the client error, typed via errors.As.
	res, err = c.Watch(ctx, gen.WatchParams{Session: "down", After: 41})
	var ps *gen.ProblemStatusCode
	if err == nil || !errors.As(err, &ps) {
		t.Fatalf("default: res=%T err=%v", res, err)
	}
	if ps.StatusCode != 503 || !ps.Response.Retryable || ps.Response.Code != "store_unavailable" {
		t.Fatalf("default: %+v", ps)
	}
	t.Logf("ogen: 200=%T 204=%T 409=%T (values, err==nil); default=%T via err=%q", w, &gen.WatchNoContent{}, p, ps, err)

	// Wire check: the 409 really went out as application/problem+json.
	b2 := &bus{blockDone: make(chan struct{})}
	srv := newServer(t, b2, &sec{})
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/replaced/watch?after=1", nil)
	req.Header.Set("Authorization", "Bearer ok")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 409 || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("wire 409: code=%d ct=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	t.Logf("wire 409 body: %s", rec.Body.String())
}

func TestHandlerErrorsBecomeProblems(t *testing.T) {
	_, _, c := newPair(t, "ok")
	// Plain Go error -> NewError -> 500 problem+json, decoded by the client as a value.
	_, err := c.Watch(context.Background(), gen.WatchParams{Session: "boom", After: 0})
	var ps *gen.ProblemStatusCode
	if !errors.As(err, &ps) || ps.StatusCode != 500 || ps.Response.Code != "internal" || ps.Response.Detail.Value != "disk on fire" {
		t.Fatalf("boom: err=%v", err)
	}
	// *ProblemStatusCode returned as the error is encoded as-is (429 here).
	_, err = c.Watch(context.Background(), gen.WatchParams{Session: "typed", After: 0})
	if !errors.As(err, &ps) || ps.StatusCode != 429 || ps.Response.Code != "rate_limited" {
		t.Fatalf("typed: err=%v", err)
	}
	t.Logf("Go error -> %d code=%s; *ProblemStatusCode as error -> %d", 500, "internal", ps.StatusCode)
}

func TestWatchLongPollHonorsContextCancel(t *testing.T) {
	b, _, c := newPair(t, "ok")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Watch(ctx, gen.WatchParams{Session: "block", After: 0})
	elapsed := time.Since(start)
	select {
	case <-b.blockDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never observed ctx cancellation")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !errors.Is(b.ctxErr, context.DeadlineExceeded) {
		t.Fatalf("handler ctx.Err() = %v", b.ctxErr)
	}
	t.Logf("client err=%v after %s; handler saw %v", err, elapsed.Round(time.Millisecond), b.ctxErr)
}

func TestIdempotencyKeyHeader(t *testing.T) {
	b, _, c := newPair(t, "ok")
	m, err := c.SendMessage(context.Background(), &gen.NewMessage{Text: "hi"},
		gen.SendMessageParams{Channel: "general", IdempotencyKey: "idem-123"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Seq != 7 {
		t.Fatalf("send: %+v", m)
	}
	if b.gotKey != "idem-123" {
		t.Fatalf("server saw Idempotency-Key=%q", b.gotKey)
	}
	// Missing header: rejected by the generated decoder (400), handler never runs.
	// With WithErrorHandler the 400 is problem+json and the client decodes it as a value.
	srv := newServer(t, b, &sec{})
	req := httptest.NewRequest(http.MethodPost, "/v1/channels/general/messages", nil)
	req.Header.Set("Authorization", "Bearer ok")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 400 || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("missing Idempotency-Key: status=%d ct=%q body=%q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	t.Logf("missing Idempotency-Key -> %d %s", rec.Code, rec.Body.String())
}

func TestDotPathAndUnauthenticatedOps(t *testing.T) {
	_, s, c := newPair(t, "ok")
	wk, err := c.WellKnown(context.Background())
	if err != nil || wk.Discover != "/v1/discover" {
		t.Fatalf("well-known: err=%v %+v", err, wk)
	}
	d, err := c.Discover(context.Background())
	if err != nil || d.APIVersions[0] != "v1" {
		t.Fatalf("discover: err=%v %+v", err, d)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) != 0 {
		t.Fatalf("SecurityHandler was called for security: [] ops: %v", s.calls)
	}
}

func TestSecurityHandlerRejects(t *testing.T) {
	_, s, c := newPair(t, "bad")
	_, err := c.Watch(context.Background(), gen.WatchParams{Session: "wake", After: 0})
	var ps *gen.ProblemStatusCode
	if !errors.As(err, &ps) || ps.StatusCode != 401 || ps.Response.Code != "unauthenticated" {
		t.Fatalf("bad token: err=%v", err)
	}
	t.Logf("bad token -> %d code=%s detail=%q", ps.StatusCode, ps.Response.Code, ps.Response.Detail.Value)
	s.mu.Lock()
	calls := append([]string(nil), s.calls...)
	s.mu.Unlock()
	if len(calls) != 1 || calls[0] != "Watch:bad" {
		t.Fatalf("SecurityHandler calls = %v", calls)
	}
	// Without WithErrorHandler: security failures still reach NewError (convenient
	// errors), so they are problem+json; but parameter decode failures go to
	// ogen's DefaultErrorHandler, which writes application/json {"error_message":...}
	// that the generated client refuses to decode against the problem+json default.
	srvDefault, _ := gen.NewServer(&bus{}, s)
	cDefault, _ := gen.NewClient("http://inproc", src{"bad"}, gen.WithClient(&http.Client{Transport: inproc{srvDefault}}))
	_, err = cDefault.Watch(context.Background(), gen.WatchParams{Session: "wake", After: 0})
	if !errors.As(err, &ps) || ps.StatusCode != 401 {
		t.Fatalf("no WithErrorHandler, bad token: err=%v", err)
	}
	t.Logf("no WithErrorHandler, bad token -> still typed %d via NewError", ps.StatusCode)
	req := httptest.NewRequest(http.MethodPost, "/v1/channels/general/messages", nil)
	req.Header.Set("Authorization", "Bearer ok")
	rec := httptest.NewRecorder()
	srvDefault.ServeHTTP(rec, req)
	t.Logf("no WithErrorHandler, missing Idempotency-Key -> %d ct=%q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected DefaultErrorHandler's application/json, got %q", rec.Header().Get("Content-Type"))
	}
}
