package credential

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingSource is a TokenSource whose Refresh is slow and counted.
type countingSource struct {
	refreshes atomic.Int32
}

func (c *countingSource) Token(context.Context) (string, error) { return "old", nil }
func (c *countingSource) Invalidate() error                     { return nil }
func (c *countingSource) Refresh(context.Context) (string, error) {
	c.refreshes.Add(1)
	time.Sleep(50 * time.Millisecond)
	return "new", nil
}

func TestTransportCollapsesConcurrent401Refreshes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	src := &countingSource{}
	c := &http.Client{Transport: Transport(nil, src)}
	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
			req.Header.Set("Authorization", "Bearer old")
			resp, err := c.Do(req)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			codes[i] = resp.StatusCode
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("request %d: status %d", i, code)
		}
	}
	if got := src.refreshes.Load(); got != 1 {
		t.Errorf("%d refreshes for %d concurrent 401s, want 1", got, n)
	}
}

// seqSource refreshes through a script: each Refresh returns the next entry,
// or fails when the entry is empty.
type seqSource struct {
	script    []string
	refreshes int
}

func (s *seqSource) Token(context.Context) (string, error) { return "old", nil }
func (s *seqSource) Invalidate() error                     { return nil }
func (s *seqSource) Refresh(context.Context) (string, error) {
	tok := s.script[s.refreshes]
	s.refreshes++
	if tok == "" {
		return "", errors.New("refresh failed")
	}
	return tok, nil
}

func statusWith(t *testing.T, c *http.Client, url, bearer string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func onlyFresh(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A Refresh that returns the refused bearer again must not be remembered: the
// next 401 on that bearer refreshes again.
func TestTransportStaleRefreshIsNotRemembered(t *testing.T) {
	srv := onlyFresh(t)
	src := &seqSource{script: []string{"old", "old", "fresh"}}
	c := &http.Client{Transport: Transport(nil, src)}
	for i, want := range []int{401, 401, 200} {
		if got := statusWith(t, c, srv.URL, "old"); got != want {
			t.Fatalf("request %d: status %d, want %d", i, got, want)
		}
	}
	if src.refreshes != 3 {
		t.Errorf("%d refreshes, want 3 (a stale result must not short-circuit later 401s)", src.refreshes)
	}
}

// A failed refresh leaves nothing behind: the next 401 tries again.
func TestTransportRefreshFailureThenRecovery(t *testing.T) {
	srv := onlyFresh(t)
	src := &seqSource{script: []string{"", "fresh"}}
	c := &http.Client{Transport: Transport(nil, src)}
	if got := statusWith(t, c, srv.URL, "old"); got != 401 {
		t.Fatalf("failed refresh: status %d, want the original 401", got)
	}
	if got := statusWith(t, c, srv.URL, "old"); got != 200 {
		t.Fatalf("after recovery: status %d, want 200", got)
	}
}
