package credential

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func echo(t *testing.T, f *fakeServer, s *Source, body []byte) (*http.Response, []byte) {
	t.Helper()
	c := &http.Client{Transport: Transport(nil, s)}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.srv.URL+"/v1/echo", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Editor()(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestTransportRefreshesOnceOn401(t *testing.T) {
	f := newFakeServer(t)
	body := bytes.Repeat([]byte("0123456789abcdef"), 64) // 1 KiB
	f.accept = func(tok string, got []byte) bool { return tok == "t2" && bytes.Equal(got, body) }
	s := f.source(t, Cache{Dir: t.TempDir()}, t0)
	resp, got := echo(t, f, s, body)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("status %d, body %d bytes", resp.StatusCode, len(got))
	}
	if n := f.exchanges.Load(); n != 2 {
		t.Errorf("%d exchanges, want 2", n)
	}
	if n := f.echoes.Load(); n != 2 {
		t.Errorf("%d requests, want 2 (original and one replay)", n)
	}
	// The refreshed bearer is the cached one.
	if tok, _ := s.Token(t.Context()); tok != "t2" {
		t.Errorf("cached token %q", tok)
	}
}

func TestTransportNeverLoops(t *testing.T) {
	f := newFakeServer(t)
	f.accept = func(string, []byte) bool { return false }
	s := f.source(t, Cache{Dir: t.TempDir()}, t0)
	resp, _ := echo(t, f, s, []byte("x"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if n := f.exchanges.Load(); n != 2 {
		t.Errorf("%d exchanges, want 2 (the first Token and exactly one refresh)", n)
	}
	if n := f.echoes.Load(); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

func TestTransportRefreshFailureReturnsOriginal401(t *testing.T) {
	f := newFakeServer(t)
	f.accept = func(string, []byte) bool { return false }
	s := f.source(t, Cache{Dir: t.TempDir()}, t0)
	if _, err := s.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.failTokens.Store(true)
	resp, _ := echo(t, f, s, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want the original 401", resp.StatusCode)
	}
	if n := f.echoes.Load(); n != 1 {
		t.Errorf("%d requests, want 1 (no replay without a new token)", n)
	}
}

func TestTransportPassesOtherStatuses(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusOK} {
		f := newFakeServer(t)
		s := f.source(t, Cache{Dir: t.TempDir()}, t0)
		other := http.NewServeMux()
		other.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })
		o := httptest.NewServer(other)
		t.Cleanup(o.Close)
		c := &http.Client{Transport: Transport(nil, s)}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, o.URL+"/x", nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != code {
			t.Errorf("status %d, want %d", resp.StatusCode, code)
		}
		if n := f.exchanges.Load(); n != 0 {
			t.Errorf("status %d: %d exchanges, want 0", code, n)
		}
	}
}
