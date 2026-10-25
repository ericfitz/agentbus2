package credential

import (
	"context"
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
