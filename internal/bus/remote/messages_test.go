package remote

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

const notFoundProblem = `{"type":"about:blank","title":"Not Found","status":404,"code":"not_found","detail":"no matching operation was found","retryable":false}`

// TestSendEmptyChannelIsValidationWithoutARequest pins the empty-name guard:
// core Send answers validation for Channel "", while the wire's empty path
// segment is the router's 404 not_found, so the remote Bus answers validation
// itself and sends nothing.
// Decision (Eric, 2026-10-21): Milestone 4 item 6 (option A: keep the guard).
func TestSendEmptyChannelIsValidationWithoutARequest(t *testing.T) {
	c := &canned{status: 404, ctype: "application/problem+json", body: notFoundProblem}
	b := newBus(t, c)
	_, err := b.Send(context.Background(), ab2bus.SendInput{Content: "x"})
	if got := ab2bus.CodeOf(err); got != ab2bus.CodeValidation {
		t.Fatalf("code = %q (%v), want validation", got, err)
	}
	if n := c.n.Load(); n != 0 {
		t.Fatalf("%d requests were sent, want 0", n)
	}
}

// TestHistoryEmptyChannelIsUnguarded: the core answers not_found for a lookup
// by "" (ChannelByName), the same code the wire's empty segment gives, so the
// remote Bus adds no rule and sends what the generated client builds.
func TestHistoryEmptyChannelIsUnguarded(t *testing.T) {
	c := &canned{status: 404, ctype: "application/problem+json", body: notFoundProblem}
	b := newBus(t, c)
	_, err := b.History(context.Background(), ab2bus.HistoryInput{})
	if got := ab2bus.CodeOf(err); got != ab2bus.CodeNotFound {
		t.Fatalf("code = %q (%v), want not_found", got, err)
	}
	if n := c.n.Load(); n != 1 {
		t.Fatalf("%d requests were sent, want 1", n)
	}
}

// queryRecorder answers 200 with body and records each request's raw query.
type queryRecorder struct {
	body string
	mu   sync.Mutex
	raw  []string
}

func (q *queryRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q.mu.Lock()
	q.raw = append(q.raw, r.URL.RawQuery)
	q.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(q.body))
}

func (q *queryRecorder) last(t *testing.T) string {
	t.Helper()
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.raw) == 0 {
		t.Fatal("no request was sent")
	}
	return q.raw[len(q.raw)-1]
}

func newRecordingBus(t *testing.T, q *queryRecorder) *Bus {
	t.Helper()
	srv := httptest.NewServer(q)
	t.Cleanup(srv.Close)
	cl, err := gen.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return New(cl)
}

// TestTagsQueryEncoding pins a wire-encoding rule: a form/explode:false query
// array cannot encode the empty set (the validator rejects "tags="), and the
// core treats an empty tag list as no filter, so History and Search send an
// empty Tags slice as an absent parameter.
func TestTagsQueryEncoding(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		body string
		call func(b *Bus, tags []string) error
	}{
		{"History", `[]`, func(b *Bus, tags []string) error {
			_, err := b.History(ctx, ab2bus.HistoryInput{Channel: "c", Tags: tags})
			return err
		}},
		{"Search", `{"hits":[]}`, func(b *Bus, tags []string) error {
			_, err := b.Search(ctx, ab2bus.SearchInput{Query: "x", Tags: tags})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &queryRecorder{body: tc.body}
			b := newRecordingBus(t, q)
			for _, tags := range [][]string{nil, {}} {
				if err := tc.call(b, tags); err != nil {
					t.Fatalf("tags %#v: %v", tags, err)
				}
				if raw := q.last(t); strings.Contains(raw, "tags") {
					t.Fatalf("tags %#v: query %q carries tags, want it absent", tags, raw)
				}
			}
			if err := tc.call(b, []string{"a", "b*"}); err != nil {
				t.Fatal(err)
			}
			if raw := q.last(t); !strings.Contains(raw, "tags=a,b%2A") {
				t.Fatalf("query %q, want tags=a,b%%2A", raw)
			}
		})
	}
}

// TestUnparsableSuccessIsInternal: a 2xx whose body is not the documented
// shape is internal and not retryable in every aggregate (a spurious
// retryable unavailable would make a caller resend a keyless Send).
func TestUnparsableSuccessIsInternal(t *testing.T) {
	ctx := context.Background()
	for name, call := range map[string]func(*Bus) error{
		"Send": func(b *Bus) error {
			_, err := b.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "x"})
			return err
		},
		"History": func(b *Bus) error {
			_, err := b.History(ctx, ab2bus.HistoryInput{Channel: "dev"})
			return err
		},
		"Search": func(b *Bus) error {
			_, err := b.Search(ctx, ab2bus.SearchInput{Query: "x"})
			return err
		},
		"Open": func(b *Bus) error {
			_, err := b.Open(ctx, ab2bus.OpenSessionInput{})
			return err
		},
		"ListChannels": func(b *Bus) error {
			_, err := b.ListChannels(ctx)
			return err
		},
		"GetMemory": func(b *Bus) error {
			_, err := b.GetMemory(ctx, 1)
			return err
		},
		"GetTask": func(b *Bus) error {
			_, err := b.GetTask(ctx, 1)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			status := 200
			if name == "Send" || name == "Open" {
				status = 201
			}
			c := &canned{status: status, ctype: "text/plain", body: "not json"}
			be := asBusError(t, call(newBus(t, c)))
			if be.Code != ab2bus.CodeInternal || be.Retryable {
				t.Fatalf("got %+v, want internal and not retryable", be)
			}
			if want := fmt.Sprintf("HTTP %d", status); !strings.Contains(be.Message, want) || !strings.Contains(be.Message, "documented shape") {
				t.Fatalf("message %q does not name %s and the undocumented shape", be.Message, want)
			}
		})
	}
}
