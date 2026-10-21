package oapi_test

// Spike test for oapi-codegen (std-http-server + strict-server + client).
// Everything runs in-process: the generated client talks to the generated
// server handler through an http.RoundTripper that calls ServeHTTP on an
// httptest.ResponseRecorder (decision 35).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"spike/oapi/gen"
)

// inproc is the injected transport: no sockets, no goroutine for a server.
type inproc struct{ h http.Handler }

func (t inproc) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	// req already carries the caller's context; ServeHTTP sees it as r.Context().
	t.h.ServeHTTP(rec, req)
	// Mirror net/http: a canceled request context is an error, not a response.
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	return rec.Result(), nil
}

func problem(status int, code, detail string) gen.Problem {
	return gen.Problem{Type: "https://agentbus2.dev/problems/" + code, Title: http.StatusText(status),
		Status: status, Detail: &detail, Code: code, Retryable: status >= 500}
}

// writeProblem is the one hand-written encoder every framework error hook shares.
func writeProblem(w http.ResponseWriter, status int, code string, err error) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem(status, code, err.Error()))
}

// bus is the hand-written StrictServerInterface implementation.
type bus struct {
	mu        sync.Mutex
	gotKey    string
	ctxErr    error         // what the blocking handler saw from ctx.Err()
	blockDone chan struct{} // closed when the blocking handler returns
}

func (b *bus) WellKnown(ctx context.Context, _ gen.WellKnownRequestObject) (gen.WellKnownResponseObject, error) {
	return gen.WellKnown200JSONResponse{Discover: "/v1/discover"}, nil
}

func (b *bus) Discover(ctx context.Context, _ gen.DiscoverRequestObject) (gen.DiscoverResponseObject, error) {
	return gen.Discover200JSONResponse{
		ApiVersions:   []string{"v1"},
		ServerVersion: "spike",
		Capabilities:  []string{"long-poll"},
		AuthMethods:   []string{"private_key_jwt"},
	}, nil
}

func (b *bus) SendMessage(ctx context.Context, req gen.SendMessageRequestObject) (gen.SendMessageResponseObject, error) {
	b.mu.Lock()
	b.gotKey = req.Params.IdempotencyKey
	b.mu.Unlock()
	return gen.SendMessage201JSONResponse{Seq: 7, Channel: req.Channel, Text: req.Body.Text}, nil
}

func (b *bus) Watch(ctx context.Context, req gen.WatchRequestObject) (gen.WatchResponseObject, error) {
	switch req.Session {
	case "wake":
		return gen.Watch200JSONResponse{Seq: req.Params.After + 1, Count: 1, Channels: []string{"general"}}, nil
	case "idle":
		return gen.Watch204Response{}, nil
	case "replaced":
		p := problem(409, "replaced", "")
		return gen.Watch409ApplicationProblemPlusJSONResponse(p), nil
	case "down":
		return gen.WatchdefaultApplicationProblemPlusJSONResponse{
			StatusCode: 503, Body: problem(503, "store_unavailable", ""),
		}, nil
	case "boom":
		return nil, errors.New("disk on fire") // routed through ResponseErrorHandlerFunc
	case "block":
		// Long-poll: block until the request context is canceled.
		<-ctx.Done()
		b.mu.Lock()
		b.ctxErr = ctx.Err()
		b.mu.Unlock()
		close(b.blockDone)
		return gen.Watch204Response{}, nil
	}
	return nil, errors.New("unknown session")
}

func newClient(t *testing.T, h http.Handler) *gen.ClientWithResponses {
	t.Helper()
	c, err := gen.NewClientWithResponses("http://inproc", gen.WithHTTPClient(&http.Client{Transport: inproc{h}}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// plainServer: defaults everywhere. Framework errors come out as text/plain
// via http.Error.
func plainServer(b *bus) http.Handler {
	// Plain net/http: HandlerFromMux registers "GET /v1/..." patterns on a *http.ServeMux.
	return gen.HandlerFromMux(gen.NewStrictHandler(b, nil), http.NewServeMux())
}

// problemServer: the three generated hook points needed so that every
// framework error is application/problem+json.
func problemServer(b *bus) http.Handler {
	strict := gen.NewStrictHandlerWithOptions(b, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeProblem(w, 400, "bad_request", err)
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeProblem(w, 500, "internal", err)
		},
	})
	return gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{
		BaseRouter: http.NewServeMux(),
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeProblem(w, 400, "bad_request", err) // parameter binding errors
		},
	})
}

func TestWatchRoundTrip(t *testing.T) {
	b := &bus{blockDone: make(chan struct{})}
	c := newClient(t, plainServer(b))
	ctx := context.Background()

	// 200
	r, err := c.WatchWithResponse(ctx, "wake", &gen.WatchParams{After: 41})
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 200 || r.JSON200 == nil || r.JSON200.Seq != 42 || r.JSON200.Channels[0] != "general" {
		t.Fatalf("200: status=%d json200=%+v", r.StatusCode(), r.JSON200)
	}

	// 204: no body, no typed field; only the status code says what happened.
	r, err = c.WatchWithResponse(ctx, "idle", &gen.WatchParams{After: 41})
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 204 || r.JSON200 != nil || len(r.Body) != 0 {
		t.Fatalf("204: status=%d body=%q", r.StatusCode(), r.Body)
	}

	// 409 problem+json: typed, and not a Go error.
	r, err = c.WatchWithResponse(ctx, "replaced", &gen.WatchParams{After: 41})
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 409 || r.ApplicationproblemJSON409 == nil || r.ApplicationproblemJSON409.Code != "replaced" {
		t.Fatalf("409: status=%d p=%+v", r.StatusCode(), r.ApplicationproblemJSON409)
	}
	if ct := r.HTTPResponse.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("409 content-type = %q", ct)
	}

	// default problem+json with 503: typed via ApplicationproblemJSONDefault.
	r, err = c.WatchWithResponse(ctx, "down", &gen.WatchParams{After: 41})
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 503 || r.ApplicationproblemJSONDefault == nil || !r.ApplicationproblemJSONDefault.Retryable ||
		r.ApplicationproblemJSONDefault.Code != "store_unavailable" {
		t.Fatalf("default: status=%d p=%+v", r.StatusCode(), r.ApplicationproblemJSONDefault)
	}
	if ct := r.HTTPResponse.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("default content-type = %q", ct)
	}
	t.Logf("oapi-codegen: 200 JSON200=%T 204=status-only 409=%T default=%T",
		r.JSON200, r.ApplicationproblemJSON409, r.ApplicationproblemJSONDefault)
}

func TestHandlerErrorsBecomeProblems(t *testing.T) {
	b := &bus{blockDone: make(chan struct{})}
	// Default wiring: a Go error from the strict handler is text/plain 500.
	r, err := newClient(t, plainServer(b)).WatchWithResponse(context.Background(), "boom", &gen.WatchParams{After: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("default wiring, Go error -> %d ct=%q body=%q typed=%v", r.StatusCode(),
		r.HTTPResponse.Header.Get("Content-Type"), r.Body, r.ApplicationproblemJSONDefault != nil)
	if r.StatusCode() != 500 || r.ApplicationproblemJSONDefault != nil {
		t.Fatalf("expected untyped 500 from default wiring")
	}
	// With ResponseErrorHandlerFunc: typed problem on the client.
	r, err = newClient(t, problemServer(b)).WatchWithResponse(context.Background(), "boom", &gen.WatchParams{After: 0})
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 500 || r.ApplicationproblemJSONDefault == nil || r.ApplicationproblemJSONDefault.Code != "internal" ||
		*r.ApplicationproblemJSONDefault.Detail != "disk on fire" {
		t.Fatalf("problem wiring: status=%d p=%+v", r.StatusCode(), r.ApplicationproblemJSONDefault)
	}
	t.Logf("problem wiring, Go error -> %d code=%s", r.StatusCode(), r.ApplicationproblemJSONDefault.Code)
}

func TestWatchLongPollHonorsContextCancel(t *testing.T) {
	b := &bus{blockDone: make(chan struct{})}
	c := newClient(t, plainServer(b))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.WatchWithResponse(ctx, "block", &gen.WatchParams{After: 0})
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
	b := &bus{blockDone: make(chan struct{})}
	c := newClient(t, plainServer(b))
	r, err := c.SendMessageWithResponse(context.Background(), "general",
		&gen.SendMessageParams{IdempotencyKey: "idem-123"}, gen.NewMessage{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 201 || r.JSON201 == nil || r.JSON201.Seq != 7 {
		t.Fatalf("send: status=%d json=%+v", r.StatusCode(), r.JSON201)
	}
	if b.gotKey != "idem-123" {
		t.Fatalf("server saw Idempotency-Key=%q", b.gotKey)
	}
	// Missing header: rejected by the generated parameter binder (400), handler never runs.
	req, _ := http.NewRequest(http.MethodPost, "http://inproc/v1/channels/general/messages", nil)
	rec := httptest.NewRecorder()
	plainServer(b).ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("missing Idempotency-Key: status=%d body=%q", rec.Code, rec.Body.String())
	}
	t.Logf("default wiring, missing Idempotency-Key -> %d ct=%q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	rec = httptest.NewRecorder()
	problemServer(b).ServeHTTP(rec, req)
	if rec.Code != 400 || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("problem wiring, missing Idempotency-Key: status=%d ct=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	t.Logf("problem wiring, missing Idempotency-Key -> %d %s", rec.Code, rec.Body.String())
}

func TestDotPathAndUnauthenticatedOps(t *testing.T) {
	b := &bus{blockDone: make(chan struct{})}
	c := newClient(t, plainServer(b))
	wk, err := c.WellKnownWithResponse(context.Background())
	if err != nil || wk.StatusCode() != 200 || wk.JSON200.Discover != "/v1/discover" {
		t.Fatalf("well-known: err=%v status=%d", err, wk.StatusCode())
	}
	d, err := c.DiscoverWithResponse(context.Background())
	if err != nil || d.StatusCode() != 200 || d.JSON200.ApiVersions[0] != "v1" {
		t.Fatalf("discover: err=%v status=%d", err, d.StatusCode())
	}
}

// TestSecurityViaValidatorMiddleware is the auth path oapi-codegen v2.8.0
// recommends: kin-openapi's openapi3filter evaluates the spec's security
// requirements and calls AuthenticationFunc once per candidate scheme.
func TestSecurityViaValidatorMiddleware(t *testing.T) {
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = nil // let the validator match any host
	var seen []string
	var mu sync.Mutex
	authn := func(ctx context.Context, in *openapi3filter.AuthenticationInput) error {
		mu.Lock()
		seen = append(seen, fmt.Sprintf("%s:%s", in.SecuritySchemeName, in.SecurityScheme.Type))
		mu.Unlock()
		if in.SecuritySchemeName == "bearerAuth" && in.RequestValidationInput.Request.Header.Get("Authorization") == "Bearer ok" {
			return nil
		}
		return errors.New("no valid credential")
	}
	b := &bus{blockDone: make(chan struct{})}
	mwDefault := nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{AuthenticationFunc: authn},
	})
	mwProblem := nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{AuthenticationFunc: authn},
		ErrorHandlerWithOpts: func(ctx context.Context, err error, w http.ResponseWriter, r *http.Request, opts nethttpmiddleware.ErrorHandlerOpts) {
			code := "bad_request"
			if opts.StatusCode == 401 {
				code = "unauthenticated"
			}
			writeProblem(w, opts.StatusCode, code, err)
		},
	})

	// Unauthenticated op (security: []) passes with no Authorization header.
	c := newClient(t, mwDefault(plainServer(b)))
	d, err := c.DiscoverWithResponse(context.Background())
	if err != nil || d.StatusCode() != 200 {
		t.Fatalf("discover without auth: err=%v status=%d body=%s", err, d.StatusCode(), d.Body)
	}
	// Protected op without a token is rejected before the handler (text/plain by default).
	r, err := c.WatchWithResponse(context.Background(), "wake", &gen.WatchParams{After: 0})
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 401 {
		t.Fatalf("watch without auth: status=%d", r.StatusCode())
	}
	t.Logf("default middleware, watch without token -> %d ct=%q %q typed=%v", r.StatusCode(),
		r.HTTPResponse.Header.Get("Content-Type"), r.Body, r.ApplicationproblemJSONDefault != nil)
	// Same with the problem+json error handler: typed on the client.
	cp := newClient(t, mwProblem(problemServer(b)))
	r, err = cp.WatchWithResponse(context.Background(), "wake", &gen.WatchParams{After: 0})
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 401 || r.ApplicationproblemJSONDefault == nil || r.ApplicationproblemJSONDefault.Code != "unauthenticated" {
		t.Fatalf("problem middleware, watch without token: status=%d p=%+v body=%s", r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body)
	}
	t.Logf("problem middleware, watch without token -> %d code=%s detail=%q", r.StatusCode(),
		r.ApplicationproblemJSONDefault.Code, *r.ApplicationproblemJSONDefault.Detail)
	// With a bearer token it passes.
	r, err = cp.WatchWithResponse(context.Background(), "wake", &gen.WatchParams{After: 0},
		func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer ok")
			return nil
		})
	if err != nil || r.StatusCode() != 200 {
		t.Fatalf("watch with token: err=%v status=%d body=%s", err, r.StatusCode(), r.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Logf("AuthenticationFunc calls (scheme:type): %v", seen)
	foundOIDC := false
	for _, s := range seen {
		if s == "oidc:openIdConnect" {
			foundOIDC = true
		}
	}
	if !foundOIDC {
		t.Fatalf("validator never offered the openIdConnect scheme: %v", seen)
	}
}
