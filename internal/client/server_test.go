package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/server/cmd"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *lockedBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *lockedBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var listeningRE = regexp.MustCompile(`listening on (\S+) `)

// startServer runs `agentbus2 server` on a loopback port over a temp SQLite
// file (open registration) and returns its base URL.
func startServer(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out, errb := &lockedBuf{}, &lockedBuf{}
	done := make(chan int, 1)
	go func() {
		done <- cmd.Run(ctx, []string{"-listen", "127.0.0.1:0", "-sqlite", filepath.Join(t.TempDir(), "srv.sqlite")}, nil, out, errb)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("server did not stop")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := listeningRE.FindStringSubmatch(out.String()); m != nil {
			return "http://" + m[1]
		}
		select {
		case code := <-done:
			t.Fatalf("server exited %d: %s %s", code, out.String(), errb.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("no listening line: %s %s", out.String(), errb.String())
	return ""
}

// countingRT counts requests by path.
type countingRT struct {
	mu    sync.Mutex
	paths map[string]int
}

func (c *countingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	if c.paths == nil {
		c.paths = map[string]int{}
	}
	c.paths[r.URL.Path]++
	c.mu.Unlock()
	return http.DefaultTransport.RoundTrip(r)
}

func (c *countingRT) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paths[path]
}

func (c *countingRT) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.paths {
		n += v
	}
	return n
}

// registerServerProfile registers identity at base with a fresh key saved in
// the credential store of profile "lab", and writes the profile.
func (e *env) registerServerProfile(t *testing.T, base, identity string) {
	t.Helper()
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	c, err := gen.NewClientWithResponses(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Register(context.Background(), c, identity, k.Public(), ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := (credential.Store{Dir: profile.CredentialDir(e.config, "lab")}).Save(identity, k); err != nil {
		t.Fatal(err)
	}
	e.save(t, profile.Profile{Name: "default", Mode: profile.ModeLocal}, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: base})
}

func TestServerOpenExchangesAndCalls(t *testing.T) {
	ctx := context.Background()
	base := startServer(t)
	e := newEnv(t)
	e.registerServerProfile(t, base, "pat")
	rt := &countingRT{}
	o := Options{Environ: e.environ, Cwd: t.TempDir(), Profile: "lab", HTTP: &http.Client{Transport: rt}}

	c, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if c.Resolved.Identity != "pat" || c.Resolved.IdentitySource != "credential" || c.Source == nil || c.Core != nil {
		t.Fatalf("resolved %+v", c.Resolved)
	}
	chs, err := c.Bus.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) == 0 || chs[0].Name != "general" {
		t.Fatalf("channels %+v", chs)
	}
	cache := filepath.Join(e.data, "tokens", "lab", "pat.json")
	fi, err := os.Stat(cache)
	if err != nil {
		t.Fatalf("token cache: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode %v", fi.Mode().Perm())
	}
	if n := rt.count("/v1/auth/token"); n != 1 {
		t.Fatalf("%d exchanges, want 1", n)
	}

	// A second Open reuses the cached bearer: no exchange.
	c2, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.Close() }()
	if _, err := c2.Bus.ListChannels(ctx); err != nil {
		t.Fatal(err)
	}
	if n := rt.count("/v1/auth/token"); n != 1 {
		t.Fatalf("%d exchanges after the second Open, want 1", n)
	}
}

// A cached bearer the server no longer accepts: the first call is a 401, the
// transport refreshes once and the retry succeeds.
func TestServerStaleCacheRefreshes(t *testing.T) {
	ctx := context.Background()
	base := startServer(t)
	e := newEnv(t)
	e.registerServerProfile(t, base, "pat")
	rt := &countingRT{}
	o := Options{Environ: e.environ, Cwd: t.TempDir(), Profile: "lab", HTTP: &http.Client{Transport: rt}}
	c, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Bus.ListChannels(ctx); err != nil {
		t.Fatal(err)
	}
	// Corrupt the cached token but keep it valid-looking.
	cache := filepath.Join(e.data, "tokens", "lab", "pat.json")
	raw, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	tokKey := ""
	for k, v := range m {
		if s, ok := v.(string); ok && strings.Count(s, ".") == 2 {
			tokKey = k
			m[k] = s[:len(s)-4] + "AAAA"
		}
	}
	if tokKey == "" {
		t.Fatalf("no JWT member in the cache entry: %s", raw)
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(cache, b, 0o600); err != nil {
		t.Fatal(err)
	}
	c2, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.Close() }()
	before := rt.count("/v1/auth/token")
	if _, err := c2.Bus.ListChannels(ctx); err != nil {
		t.Fatalf("ListChannels with a stale cached bearer: %v", err)
	}
	if n := rt.count("/v1/auth/token") - before; n != 1 {
		t.Fatalf("%d exchanges for the stale bearer, want exactly 1", n)
	}
	_ = c.Close()
}

func TestServerOpenWithoutCredential(t *testing.T) {
	e := newEnv(t)
	e.save(t, profile.Profile{Name: "default", Mode: profile.ModeLocal}, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"})
	_, err := Open(context.Background(), Options{Environ: e.environ, Cwd: t.TempDir(), Profile: "lab", Identity: "pat"})
	if err == nil || !strings.Contains(err.Error(), "run agentbus2 register") || !strings.Contains(err.Error(), `"pat"`) || !strings.Contains(err.Error(), `"lab"`) {
		t.Fatalf("err %v", err)
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err %T is not a *ResolveError", err)
	}
}

func TestServerOpenRefusesWideCredential(t *testing.T) {
	e := newEnv(t)
	e.save(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"})
	e.credential(t, "lab", "pat")
	path := filepath.Join(profile.CredentialDir(e.config, "lab"), "pat")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), Options{Environ: e.environ, Cwd: t.TempDir(), Profile: "lab"}); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("err %v", err)
	}
}

func TestServerOpenNeverExchangesBeforeFirstCall(t *testing.T) {
	e := newEnv(t)
	e.save(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"})
	e.credential(t, "lab", "pat")
	rt := &countingRT{}
	c, err := Open(context.Background(), Options{Environ: e.environ, Cwd: t.TempDir(), Profile: "lab", HTTP: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if n := rt.total(); n != 0 {
		t.Fatalf("%d requests during Open, want 0", n)
	}
	if _, err := os.Stat(filepath.Join(e.data, "tokens")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open created the token directory: %v", err)
	}
	// The first call dials (and fails: nothing listens on port 1).
	if _, err := c.Bus.ListChannels(context.Background()); err == nil {
		t.Fatal("ListChannels reached a server on port 1")
	}
}

// The client Source exchanges tokens through must not refresh on a 401 of its
// own: a rejected assertion is one attempt, then an error.
func TestServerTokenEndpoint401IsNotRetried(t *testing.T) {
	var tokens, others atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "token_endpoint": srv.URL + "/v1/auth/token", "jwks_uri": srv.URL + "/jwks"})
		case "/v1/auth/token":
			tokens.Add(1)
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"x","status":401,"code":"unauthenticated","detail":"no"}`))
		default:
			others.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	e := newEnv(t)
	e.save(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: srv.URL})
	e.credential(t, "lab", "pat")
	c, err := Open(context.Background(), Options{Environ: e.environ, Cwd: t.TempDir(), Profile: "lab"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, err = c.Bus.ListChannels(context.Background())
	if err == nil {
		t.Fatal("ListChannels succeeded against a server rejecting every assertion")
	}
	if n := tokens.Load(); n != 1 {
		t.Fatalf("%d token requests, want 1", n)
	}
	if n := others.Load(); n != 0 {
		t.Fatalf("%d bearer requests were sent without a token", n)
	}
}
