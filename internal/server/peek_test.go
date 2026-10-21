package server

import (
	"context"
	"net/http"
	"sync"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// urlRecorder notes the URL of every request it forwards.
type urlRecorder struct {
	next http.RoundTripper
	mu   sync.Mutex
	urls []string
}

func (r *urlRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.URL.RequestURI())
	r.mu.Unlock()
	return r.next.RoundTrip(req)
}

func (r *urlRecorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.urls[len(r.urls)-1]
}

func TestPeekOnTheWire(t *testing.T) {
	s := newSessionEnv(t, nil)
	path := "/v1/sessions/" + s.session.Id + "/peek"
	get := func(identity, query string, hdr map[string]string) *http.Response {
		req, err := http.NewRequest(http.MethodGet, "http://inproc"+path+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		return s.raw(identity, req)
	}
	if got := get("", "", nil).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("no identity: status %d, want 401", got)
	}
	// An Idempotency-Key is undeclared and ignored.
	if got := get("pat", "", map[string]string{"Idempotency-Key": "k1"}).StatusCode; got != http.StatusOK {
		t.Fatalf("with Idempotency-Key: status %d, want 200", got)
	}
	if got := get("pat", "?include_own=maybe", nil).StatusCode; got != http.StatusBadRequest {
		t.Fatalf("include_own=maybe: status %d, want 400", got)
	}

	rec := &urlRecorder{next: InProcessTransport(s.handler(), "pat")}
	cl, err := gen.NewClientWithResponses("http://inproc", gen.WithHTTPClient(&http.Client{Transport: rec}))
	if err != nil {
		t.Fatal(err)
	}
	b := remote.New(cl)
	ctx := context.Background()
	if _, err := s.sendErr("sam", ""); err != nil {
		t.Fatalf("send: %v", err)
	}
	r, err := b.Peek(ctx, ab2bus.PeekInput{Session: s.session.Id})
	if err != nil || r.Count != 1 || len(r.Channels) != 1 || r.Channels[0] != (ab2bus.PeekChannel{Channel: "dev", Count: 1}) {
		t.Fatalf("Peek = %+v, %v", r, err)
	}
	if got := rec.last(); got != path {
		t.Fatalf("request = %q, want %q (include_own only when set)", got, path)
	}
	if _, err := b.Peek(ctx, ab2bus.PeekInput{Session: s.session.Id, IncludeOwn: true}); err != nil {
		t.Fatal(err)
	}
	if got := rec.last(); got != path+"?include_own=true" {
		t.Fatalf("request = %q, want include_own=true", got)
	}
}
