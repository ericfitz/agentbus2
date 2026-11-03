package mcpcli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// proxyServer fronts the real server at base. before may answer a request
// itself (true: handled); after may rewrite the real answer's JSON body.
func proxyServer(t *testing.T, base string, before func(w http.ResponseWriter, r *http.Request) bool, after func(r *http.Request, doc map[string]any)) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.ModifyResponse = func(resp *http.Response) error {
		if after == nil {
			return nil
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		var doc map[string]any
		if json.Unmarshal(b, &doc) == nil {
			after(resp.Request, doc)
			if b, err = json.Marshal(doc); err != nil {
				return err
			}
		}
		resp.Body = io.NopCloser(bytes.NewReader(b))
		resp.ContentLength = int64(len(b))
		resp.Header.Set("Content-Length", strconv.Itoa(len(b)))
		return nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if before != nil && before(w, r) {
			return
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestCheckFailSteps: each later step's FAIL line names the step, the cause
// and its fix, prints no later line, and the summary goes to stderr.
func TestCheckFailSteps(t *testing.T) {
	t.Chdir(t.TempDir())
	base := startServer(t)
	environ, configDir := testEnviron(t)
	if code, out, errs := run([]string{"register", "-server", base, "-identity", "pat"}, environ); code != 0 {
		t.Fatalf("register: %d %q %q", code, out, errs)
	}

	// Cases that go through a proxy name the registered key as a file, since
	// the stored profile names the real server.
	keyFile := filepath.Join(configDir, "credentials", "default", "pat")
	other, _ := testEnviron(t)

	t.Run("health", func(t *testing.T) {
		px := proxyServer(t, base, func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/v1/health" {
				return false
			}
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"store unreachable","status":503}`))
			return true
		}, nil)
		code, out, errs := run([]string{"check", "-server", px, "-identity", "pat", "-credential", keyFile}, other)
		got := lines(out)
		if code != 1 || len(got) != 2 || !strings.HasPrefix(got[0], "ok   reach: ") || !strings.HasPrefix(got[1], "FAIL health: ") ||
			!strings.HasSuffix(got[1], "fix: ask the server's operator: its store does not answer") ||
			strings.TrimSpace(errs) != "agentmesh-mcponly: check: health failed" {
			t.Errorf("health: code %d, stdout %q, stderr %q", code, out, errs)
		}
	})

	t.Run("credential_process", func(t *testing.T) {
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := `{"profiles":{"proc":{"mode":"server","server":"` + base + `","credential_process":["true"]}}}`
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		// The process step is reported as such; the token step then reports the
		// process's empty answer (the command prints nothing).
		code, out, errs := run([]string{"check", "-profile", "proc", "-identity", "pat"}, environ)
		got := lines(out)
		if code != 1 || len(got) != 5 || got[3] != "ok   credential: credential_process true" ||
			!strings.HasPrefix(got[4], "FAIL token: ") || !strings.Contains(got[4], "true") ||
			strings.TrimSpace(errs) != "agentmesh-mcponly: check: token failed" {
			t.Errorf("credential_process: code %d, stdout %q, stderr %q", code, out, errs)
		}
	})

	t.Run("token unavailable", func(t *testing.T) {
		px := proxyServer(t, base, func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/v1/auth/token" {
				return false
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"temporarily_unavailable","error_description":"the signing key is unavailable"}`))
			return true
		}, nil)
		code, out, errs := run([]string{"check", "-server", px, "-identity", "pat", "-credential", keyFile}, other)
		got := lines(out)
		if code != 1 || len(got) != 5 || !strings.HasPrefix(got[4], "FAIL token: unavailable: the signing key is unavailable") ||
			!strings.HasSuffix(got[4], "fix: retry later; the server is busy or its store is down") ||
			strings.TrimSpace(errs) != "agentmesh-mcponly: check: token failed" {
			t.Errorf("token unavailable: code %d, stdout %q, stderr %q", code, out, errs)
		}
	})

	t.Run("access tier mismatch", func(t *testing.T) {
		px := proxyServer(t, base, nil, func(r *http.Request, doc map[string]any) {
			if r.URL.Path == "/v1/health" && r.Header.Get("Authorization") != "" {
				doc["tier"] = "probe"
			}
		})
		code, out, errs := run([]string{"check", "-server", px, "-identity", "pat", "-credential", keyFile}, other)
		got := lines(out)
		if code != 1 || len(got) != 6 || !strings.HasPrefix(got[5], "FAIL access: the server answered at the probe tier, not the identity tier") ||
			!strings.HasSuffix(got[5], "fix: agentmesh-mcponly help troubleshoot") ||
			strings.TrimSpace(errs) != "agentmesh-mcponly: check: access failed" {
			t.Errorf("access tier: code %d, stdout %q, stderr %q", code, out, errs)
		}
	})
}
