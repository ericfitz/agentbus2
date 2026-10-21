package embed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/core"
)

func TestHTTPEmbedRequestAndValidation(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	reply := `{"data":[{"index":1,"embedding":[0,2]},{"index":0,"embedding":[3,0]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = io.WriteString(w, reply)
	}))
	defer srv.Close()
	e := NewHTTP(srv.URL, "m1", "sekret", nil)
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sekret" || gotBody["model"] != "m1" {
		t.Fatalf("auth=%q body=%v", gotAuth, gotBody)
	}
	if in, _ := gotBody["input"].([]any); len(in) != 2 || in[0] != "a" || in[1] != "b" {
		t.Fatalf("input = %v", gotBody["input"])
	}
	if vecs[0][0] != 3 || vecs[1][1] != 2 { // returned by index, not by position
		t.Fatalf("vecs = %v", vecs)
	}
	if e.Model() != "m1" {
		t.Fatalf("Model = %q", e.Model())
	}

	gotAuth = "unset"
	if _, err := NewHTTP(srv.URL, "m1", "", nil).Embed(context.Background(), []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization sent without a key: %q", gotAuth)
	}

	bad := map[string]string{
		"count":     `{"data":[{"index":0,"embedding":[1]}]}`,
		"range":     `{"data":[{"index":0,"embedding":[1]},{"index":2,"embedding":[1]}]}`,
		"duplicate": `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[1]}]}`,
		"empty":     `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[]}]}`,
		"ragged":    `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[1,2]}]}`,
		"malformed": `not json`,
	}
	for name, body := range bad {
		reply = body
		_, err := e.Embed(context.Background(), []string{"a", "b"})
		if err == nil || core.IsRowRejected(err) {
			t.Errorf("%s: err = %v, want a plain error", name, err)
		}
	}
}

func TestHTTPRowRejectedStatuses(t *testing.T) {
	status := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "input   too\nlong")
	}))
	e := NewHTTP(srv.URL, "m", "k", nil)
	for _, code := range []int{400, 413, 422} {
		status = code
		_, err := e.Embed(context.Background(), []string{"x"})
		var rr *core.RowRejected
		if !core.IsRowRejected(err) || !errors.As(err, &rr) || rr.Index != -1 || rr.Reason != "input too long" {
			t.Errorf("status %d: err = %v", code, err)
		}
	}
	for _, code := range []int{401, 403, 429, 500, 503} {
		status = code
		_, err := e.Embed(context.Background(), []string{"x"})
		if err == nil || core.IsRowRejected(err) {
			t.Errorf("status %d: err = %v, want a plain error", code, err)
		}
		if code == 401 && (err == nil || !strings.Contains(err.Error(), "API key")) {
			t.Errorf("401 text %v does not name the key", err)
		}
	}
	srv.Close()
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil || core.IsRowRejected(err) {
		t.Errorf("closed server: err = %v, want a plain error", err)
	}
}
