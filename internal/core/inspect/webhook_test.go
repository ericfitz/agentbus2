package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
)

func testRequest(content string) core.InspectRequest {
	return core.InspectRequest{
		Version: core.InspectRequestVersion, RequestID: "0123456789abcdef", Operation: core.OpSend,
		Source: core.SourceAPI, Sender: "alice", SenderKind: "agent",
		Channel: core.InspectChannel{Name: "general", Kind: "channel"},
		Record:  core.InspectRecord{Content: content, Tags: []string{"a"}},
	}
}

func failClass(t *testing.T, err error) string {
	t.Helper()
	var ie *core.InspectError
	if !errors.As(err, &ie) {
		t.Fatalf("error %v is not a *core.InspectError", err)
	}
	return ie.Class
}

func TestWebhookPostsRequest(t *testing.T) {
	for _, key := range []string{"k", ""} {
		var got core.InspectRequest
		var method, ctype, accept, auth string
		var hasAuth bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			method, ctype, accept = r.Method, r.Header.Get("Content-Type"), r.Header.Get("Accept")
			auth = r.Header.Get("Authorization")
			_, hasAuth = r.Header["Authorization"]
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &got)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"verdict": "allow" }`))
		}))
		want := testRequest("hello")
		raw, err := NewWebhook(srv.URL, key, nil).Inspect(context.Background(), want)
		srv.Close()
		if err != nil {
			t.Fatalf("key %q: %v", key, err)
		}
		if string(raw) != `{"verdict": "allow" }` {
			t.Fatalf("reply %q", raw)
		}
		if method != http.MethodPost || ctype != "application/json" || accept != "application/json" {
			t.Fatalf("method %q ctype %q accept %q", method, ctype, accept)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("body %+v want %+v", got, want)
		}
		if key == "" && hasAuth {
			t.Fatalf("Authorization sent without a key: %q", auth)
		}
		if key != "" && auth != "Bearer k" {
			t.Fatalf("Authorization %q", auth)
		}
	}
}

func TestWebhookFailures(t *testing.T) {
	reply := func(ctype string, status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ctype != "" {
				w.Header().Set("Content-Type", ctype)
			} else {
				w.Header()["Content-Type"] = nil // suppress sniffing
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}
	var second atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"verdict":"allow"}`))
	}))
	defer target.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	tests := []struct {
		name    string
		srv     *httptest.Server
		url     string
		timeout time.Duration
		class   string // "" means success
		bodyLen int
	}{
		{name: "500", srv: reply("application/json", 500, `{}`), class: core.FailStatus},
		{name: "302", srv: httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound)), class: core.FailStatus},
		{name: "text/plain", srv: reply("text/plain", 200, `{"verdict":"allow"}`), class: core.FailMalformed},
		{name: "no content type", srv: reply("", 200, `{"verdict":"allow"}`), class: core.FailMalformed},
		{name: "charset", srv: reply("application/json; charset=utf-8", 200, `{"verdict":"allow"}`)},
		{name: "65537 bytes", srv: reply("application/json", 200, strings.Repeat("a", core.MaxVerdictBytes+1)), class: core.FailMalformed},
		{name: "65536 bytes", srv: reply("application/json", 200, strings.Repeat("a", core.MaxVerdictBytes)), bodyLen: core.MaxVerdictBytes},
		{name: "closed server", url: closedURL, class: core.FailTransport},
		{name: "deadline", srv: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(300 * time.Millisecond):
			}
		})), timeout: 50 * time.Millisecond, class: core.FailTransport},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url := tc.url
			if tc.srv != nil {
				defer tc.srv.Close()
				url = tc.srv.URL
			}
			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			raw, err := NewWebhook(url, "", nil).Inspect(ctx, testRequest("x"))
			if tc.class == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.bodyLen != 0 && len(raw) != tc.bodyLen {
					t.Fatalf("body %d bytes, want %d", len(raw), tc.bodyLen)
				}
				return
			}
			if err == nil {
				t.Fatalf("no error, reply %q", raw)
			}
			if got := failClass(t, err); got != tc.class {
				t.Fatalf("class %q want %q (%v)", got, tc.class, err)
			}
			if tc.name == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error %v does not wrap context.DeadlineExceeded", err)
			}
		})
	}
	if n := second.Load(); n != 0 {
		t.Fatalf("the redirect target saw %d requests", n)
	}
}

func TestWebhookErrorsCarryNoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_, _ = w.Write([]byte("SECRET-BODY"))
	}))
	defer srv.Close()
	_, err := NewWebhook(srv.URL+"/hook?token=SECRET-QUERY", "SECRET-KEY", nil).Inspect(context.Background(), testRequest("SECRET-CONTENT"))
	if err == nil {
		t.Fatal("no error")
	}
	check := func(label string, err error) {
		for _, s := range []string{"SECRET-BODY", "SECRET-CONTENT", "SECRET-KEY", "SECRET-QUERY", "/hook"} {
			if strings.Contains(err.Error(), s) {
				t.Fatalf("%s error %q carries %q", label, err, s)
			}
		}
	}
	check("status", err)

	// A text/plain reply with a body, and a transport failure (the URL's
	// query string must not leak through *url.Error).
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("SECRET-BODY"))
	}))
	defer srv2.Close()
	_, err = NewWebhook(srv2.URL+"/hook?token=SECRET-QUERY", "SECRET-KEY", nil).Inspect(context.Background(), testRequest("SECRET-CONTENT"))
	if err == nil {
		t.Fatal("no error")
	}
	check("media type", err)

	closed := httptest.NewServer(http.NotFoundHandler())
	u := closed.URL
	closed.Close()
	_, err = NewWebhook(u+"/hook?token=SECRET-QUERY", "SECRET-KEY", nil).Inspect(context.Background(), testRequest("SECRET-CONTENT"))
	if err == nil {
		t.Fatal("no error")
	}
	check("transport", err)
}
