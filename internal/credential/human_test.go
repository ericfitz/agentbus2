package credential

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/oidcclient"
	"github.com/ericfitz/agentbus2/internal/oidctest"
)

// humanEnv is a fake corporate provider plus a fake agentbus2 server that
// answers discovery, the RFC 8693 exchange and a protected /v1/echo.
type humanEnv struct {
	prov        *oidctest.Provider
	srv         *httptest.Server
	exchanges   atomic.Int32
	discoveries atomic.Int32
	browsers    atomic.Int32
	echo401s    atomic.Int32 // /v1/echo answers 401 this many times first
	exchange503 atomic.Int32 // /v1/auth/token answers 503 this many times first

	mu       sync.Mutex
	sub      string // sub of the bearers the exchange mints
	subjects []string
	now      time.Time
	last     string // the latest minted bearer
}

func unsignedJWT(sub string, n int32) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	claims := map[string]any{"jti": fmt.Sprintf("n%d", n)}
	if sub != "" {
		claims["sub"] = sub
	}
	return enc(map[string]string{"alg": "none", "typ": "at+jwt"}) + "." + enc(claims) + ".sig"
}

func newHumanEnv(t *testing.T, refreshTokens bool) *humanEnv {
	t.Helper()
	return newHumanEnvOpts(t, oidctest.Options{RefreshTokens: refreshTokens})
}

// newHumanEnvOpts is newHumanEnv with the provider's refresh options from o.
func newHumanEnvOpts(t *testing.T, o oidctest.Options) *humanEnv {
	t.Helper()
	h := &humanEnv{sub: "eric@corp.example", now: t0}
	h.prov = oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid", RefreshTokens: o.RefreshTokens,
		RotateRefreshTokens: o.RotateRefreshTokens, RefreshRejectError: o.RefreshRejectError,
		Claims: map[string]any{"sub": "u1", "email": "eric@corp.example"}})
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		h.discoveries.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": "http://issuer", "token_endpoint": "http://issuer/v1/auth/token", "jwks_uri": "http://issuer/jwks"})
	})
	mux.HandleFunc("/v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" {
			http.Error(w, "bad grant", http.StatusBadRequest)
			return
		}
		if h.exchange503.Add(-1) >= 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		h.exchange503.Store(0)
		n := h.exchanges.Add(1)
		h.mu.Lock()
		h.subjects = append(h.subjects, r.PostForm.Get("subject_token"))
		tok := unsignedJWT(h.sub, n)
		h.last = tok
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 900, "issued_token_type": "urn:ietf:params:oauth:token-type:access_token"})
	})
	mux.HandleFunc("/v1/echo", func(w http.ResponseWriter, r *http.Request) {
		if h.echo401s.Add(-1) >= 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.echo401s.Store(0)
		h.mu.Lock()
		last := h.last
		h.mu.Unlock()
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != last {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *humanEnv) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *humanEnv) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

// openBrowser is the OpenBrowser seam: it GETs the authorization URL and so
// follows the provider's redirect to the loopback listener.
func (h *humanEnv) openBrowser(raw string) error {
	h.browsers.Add(1)
	go func() {
		c := &http.Client{Timeout: 5 * time.Second}
		if resp, err := c.Get(raw); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	return nil
}

func (h *humanEnv) source(t *testing.T, dir string) *HumanSource {
	t.Helper()
	gc, err := gen.NewClientWithResponses(h.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &HumanSource{
		Server: h.srv.URL,
		OIDC: oidcclient.Config{
			Issuer: h.prov.URL(), ClientID: "cid", RedirectPort: 0, // the OS picks the port at listen time; no probe to race
			Scopes: []string{"openid", "email"}, OpenBrowser: h.openBrowser, Timeout: 5 * time.Second,
		},
		Client: gc,
		HTTP:   &http.Client{Timeout: 5 * time.Second},
		Cache:  HumanCache{Dir: dir},
		Now:    h.clock,
	}
}

// providerGrants counts the provider's token-endpoint requests by grant_type.
func providerGrants(h *humanEnv, grant string) int {
	n := 0
	for _, r := range h.prov.Requests() {
		if strings.HasPrefix(r, "POST /token ") && strings.Contains(r, "grant_type="+grant) {
			n++
		}
	}
	return n
}

func TestHumanSourceCachedToken(t *testing.T) {
	h := newHumanEnv(t, true)
	dir := t.TempDir()
	s := h.source(t, dir)
	tok := unsignedJWT("eric@corp.example", 99)
	if err := s.Cache.Save(HumanSession{Server: h.srv.URL, Identity: "eric@corp.example", AccessToken: tok, ExpiresAt: t0.Add(10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Token(t.Context())
	if err != nil || got != tok {
		t.Fatalf("Token = %q, %v", got, err)
	}
	if n := len(h.prov.Requests()); n != 0 {
		t.Errorf("%d provider requests, want 0", n)
	}
	if h.exchanges.Load() != 0 || h.discoveries.Load() != 0 || h.browsers.Load() != 0 {
		t.Errorf("exchanges %d discoveries %d browsers %d, want none", h.exchanges.Load(), h.discoveries.Load(), h.browsers.Load())
	}
}

func TestHumanSourceRefreshTokenPath(t *testing.T) {
	h := newHumanEnv(t, true)
	s := h.source(t, t.TempDir())
	if _, err := s.SignIn(t.Context()); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if h.browsers.Load() != 1 || h.exchanges.Load() != 1 {
		t.Fatalf("after sign-in: browsers %d exchanges %d", h.browsers.Load(), h.exchanges.Load())
	}
	h.advance(20 * time.Minute) // past the 900 s bearer
	tok, err := s.Token(t.Context())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if h.browsers.Load() != 1 {
		t.Errorf("%d browser calls, want 1 (the refresh token path)", h.browsers.Load())
	}
	if h.exchanges.Load() != 2 {
		t.Errorf("%d exchanges, want 2", h.exchanges.Load())
	}
	if n := providerGrants(h, "refresh_token"); n != 1 {
		t.Errorf("%d refresh grants, want 1", n)
	}
	e, ok, err := s.Cache.Load()
	if err != nil || !ok || e.AccessToken != tok || e.ProviderRefreshToken == "" {
		t.Errorf("cached session = %+v, %v, %v", e, ok, err)
	}
	// The cached bearer is used again without a call.
	if again, _ := s.Token(t.Context()); again != tok || h.exchanges.Load() != 2 {
		t.Errorf("second Token = %q after %d exchanges", again, h.exchanges.Load())
	}
}

func TestHumanSourceBrowserWhenNoRefreshToken(t *testing.T) {
	h := newHumanEnv(t, false)
	s := h.source(t, t.TempDir())
	if _, err := s.Token(t.Context()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	h.advance(20 * time.Minute)
	if _, err := s.Token(t.Context()); err != nil {
		t.Fatalf("Token after expiry: %v", err)
	}
	if h.browsers.Load() != 2 {
		t.Errorf("%d browser calls, want 2", h.browsers.Load())
	}
	if n := providerGrants(h, "refresh_token"); n != 0 {
		t.Errorf("%d refresh grants, want 0", n)
	}
}

func TestHumanSourceRefreshRejectedFallsBackToBrowser(t *testing.T) {
	h := newHumanEnv(t, true)
	s := h.source(t, t.TempDir())
	if _, err := s.SignIn(t.Context()); err != nil {
		t.Fatal(err)
	}
	e, _, _ := s.Cache.Load()
	e.ProviderRefreshToken = "refresh-revoked"
	if err := s.Cache.Save(e); err != nil {
		t.Fatal(err)
	}
	h.advance(20 * time.Minute)
	if _, err := s.Token(t.Context()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if h.browsers.Load() != 2 {
		t.Errorf("%d browser calls, want 2", h.browsers.Load())
	}
	if n := providerGrants(h, "refresh_token"); n != 1 {
		t.Errorf("%d refresh grants, want 1 (the rejected one)", n)
	}
	if e2, _, _ := s.Cache.Load(); e2.ProviderRefreshToken == "" || e2.ProviderRefreshToken == "refresh-revoked" {
		t.Errorf("refresh token after fallback = %q, want the new one", e2.ProviderRefreshToken)
	}
}

// TestHumanSourceRefreshKeepsRotatedTokenOnExchangeFailure: the provider
// rotates refresh tokens (Dex), the exchange fails once (server down); the
// rotated token is cached, so the next Token refreshes with it instead of
// presenting the retired one (Dex: 400 invalid_request "claimed twice").
func TestHumanSourceRefreshKeepsRotatedTokenOnExchangeFailure(t *testing.T) {
	h := newHumanEnvOpts(t, oidctest.Options{RefreshTokens: true, RotateRefreshTokens: true, RefreshRejectError: "invalid_request"})
	s := h.source(t, t.TempDir())
	if _, err := s.SignIn(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, _, _ := s.Cache.Load()
	h.advance(20 * time.Minute)
	h.exchange503.Store(1)
	if _, err := s.Token(t.Context()); err == nil {
		t.Fatal("Token with the exchange down: nil error")
	}
	e, ok, err := s.Cache.Load()
	if err != nil || !ok || e.ProviderRefreshToken == "" || e.ProviderRefreshToken == first.ProviderRefreshToken || e.AccessToken != "" {
		t.Fatalf("cache after the failed exchange = %+v, %v, %v; want the rotated refresh token and no bearer", e, ok, err)
	}
	if _, err := s.Token(t.Context()); err != nil {
		t.Fatalf("Token after the server is back: %v", err)
	}
	if h.browsers.Load() != 1 {
		t.Errorf("%d browser calls, want 1 (the refresh token path)", h.browsers.Load())
	}
	if n := providerGrants(h, "refresh_token"); n != 2 {
		t.Errorf("%d refresh grants, want 2", n)
	}
}

// TestHumanSourceInvalidRequestFallsBackToBrowser: a retired refresh token
// that Dex answers with invalid_request sends the human to the browser.
func TestHumanSourceInvalidRequestFallsBackToBrowser(t *testing.T) {
	h := newHumanEnvOpts(t, oidctest.Options{RefreshTokens: true, RotateRefreshTokens: true, RefreshRejectError: "invalid_request"})
	s := h.source(t, t.TempDir())
	if _, err := s.SignIn(t.Context()); err != nil {
		t.Fatal(err)
	}
	e, _, _ := s.Cache.Load()
	e.ProviderRefreshToken = "refresh-claimed-twice"
	if err := s.Cache.Save(e); err != nil {
		t.Fatal(err)
	}
	h.advance(20 * time.Minute)
	if _, err := s.Token(t.Context()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if h.browsers.Load() != 2 {
		t.Errorf("%d browser calls, want 2", h.browsers.Load())
	}
}

func TestHumanSourceIdentityFromSub(t *testing.T) {
	h := newHumanEnv(t, false)
	s := h.source(t, t.TempDir())
	if id := s.Identity(); id != "" {
		t.Errorf("Identity before sign-in = %q", id)
	}
	if _, err := s.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	if id := s.Identity(); id != "eric@corp.example" {
		t.Errorf("Identity = %q", id)
	}

	// A bearer without a sub is an error at save and is not cached.
	h2 := newHumanEnv(t, false)
	h2.sub = ""
	dir := t.TempDir()
	s2 := h2.source(t, dir)
	if _, err := s2.Token(t.Context()); err == nil || !strings.Contains(err.Error(), "sub") {
		t.Fatalf("Token with a sub-less bearer: %v", err)
	}
	if _, ok, _ := s2.Cache.Load(); ok {
		t.Error("a sub-less bearer was cached")
	}
	if err := s2.Cache.Save(HumanSession{Server: "x", AccessToken: "not-a-jwt"}); err == nil {
		t.Error("Save accepted a session with no identity")
	}
}

func TestHumanSourceIgnoresOtherServer(t *testing.T) {
	h := newHumanEnv(t, true)
	s := h.source(t, t.TempDir())
	if err := s.Cache.Save(HumanSession{Server: "http://other.example", Identity: "mallory@x.example",
		AccessToken: unsignedJWT("mallory@x.example", 1), ExpiresAt: t0.Add(time.Hour), ProviderRefreshToken: "refresh-other"}); err != nil {
		t.Fatal(err)
	}
	if id := s.Identity(); id != "" {
		t.Errorf("Identity from another server's session = %q", id)
	}
	tok, err := s.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tok, "mallory") || h.browsers.Load() != 1 {
		t.Errorf("token %q, %d browser calls: the other server's session must be a miss", tok, h.browsers.Load())
	}
	if n := providerGrants(h, "refresh_token"); n != 0 {
		t.Errorf("%d refresh grants: the other server's refresh token was used", n)
	}
}

func TestHumanSourceInvalidateKeepsRefreshToken(t *testing.T) {
	h := newHumanEnv(t, true)
	s := h.source(t, t.TempDir())
	if _, err := s.SignIn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Invalidate(); err != nil {
		t.Fatal(err)
	}
	e, ok, err := s.Cache.Load()
	if err != nil || !ok || e.AccessToken != "" || e.ProviderRefreshToken == "" {
		t.Fatalf("after Invalidate: %+v, %v, %v", e, ok, err)
	}
	if id := s.Identity(); id != "eric@corp.example" {
		t.Errorf("Identity after Invalidate = %q", id)
	}
	if _, err := s.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	if h.browsers.Load() != 1 || h.exchanges.Load() != 2 {
		t.Errorf("browsers %d exchanges %d, want 1 and 2 (refresh path)", h.browsers.Load(), h.exchanges.Load())
	}
	// Invalidate with nothing cached is not an error.
	if err := h.source(t, t.TempDir()).Invalidate(); err != nil {
		t.Errorf("Invalidate on an empty cache: %v", err)
	}
}

func TestHumanSourceAnnounce(t *testing.T) {
	h := newHumanEnv(t, false)
	s := h.source(t, t.TempDir())
	var got []string
	var mu sync.Mutex
	s.SetAnnounce(func(u string) { mu.Lock(); got = append(got, u); mu.Unlock() })
	if _, err := s.SignIn(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.SetAnnounce(nil)
	if _, err := s.SignIn(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || !strings.HasPrefix(got[0], h.prov.URL()) {
		t.Errorf("announced %v, want exactly the first sign-in URL", got)
	}
}

// spySource counts Invalidate calls on a TokenSource.
type spySource struct {
	TokenSource
	invalidated atomic.Int32
}

func (s *spySource) Invalidate() error {
	s.invalidated.Add(1)
	return s.TokenSource.Invalidate()
}

func get(t *testing.T, src TokenSource, url string) int {
	t.Helper()
	c := &http.Client{Transport: Transport(nil, src)}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/v1/echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := src.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestTransportInvalidatesOn401(t *testing.T) {
	t.Run("agent source", func(t *testing.T) {
		f := newFakeServer(t)
		f.accept = func(tok string, _ []byte) bool { return tok == "t2" }
		spy := &spySource{TokenSource: f.source(t, Cache{Dir: t.TempDir()}, t0)}
		if code := get(t, spy, f.srv.URL); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		if n := spy.invalidated.Load(); n != 1 {
			t.Errorf("%d Invalidate calls, want 1", n)
		}
	})
	t.Run("agent source removes its cache file", func(t *testing.T) {
		f := newFakeServer(t)
		f.accept = func(string, []byte) bool { return false }
		dir := t.TempDir()
		s := f.source(t, Cache{Dir: dir}, t0)
		if err := s.Invalidate(); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.Cache.Load(s.Identity); ok {
			t.Error("Invalidate left the cache entry")
		}
	})
	t.Run("human source", func(t *testing.T) {
		h := newHumanEnv(t, true)
		h.echo401s.Store(1)
		spy := &spySource{TokenSource: h.source(t, t.TempDir())}
		if code := get(t, spy, h.srv.URL); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		if n := spy.invalidated.Load(); n != 1 {
			t.Errorf("%d Invalidate calls, want 1", n)
		}
		if h.browsers.Load() != 1 {
			t.Errorf("%d browser calls, want 1: the replay refreshes through the provider's refresh token", h.browsers.Load())
		}
	})
}

func TestHumanCacheModes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tokens", "lab")
	c := HumanCache{Dir: root}
	if _, ok, err := c.Load(); ok || err != nil {
		t.Fatalf("Load of an absent cache = %v, %v", ok, err)
	}
	if err := c.Remove(); err != nil {
		t.Fatalf("Remove of an absent cache: %v", err)
	}
	want := HumanSession{Server: "http://s", Identity: "eric@corp.example", AccessToken: unsignedJWT("eric@corp.example", 1),
		ExpiresAt: t0.Add(time.Hour), Discovery: Discovery{Issuer: "http://i", TokenEndpoint: "http://i/t"}, ProviderRefreshToken: "r1"}
	for range 2 { // the first write and a replacement
		if err := c.Save(want); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "human", "session.json")
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("file %v, %v, want 0600", fi, err)
		}
		di, err := os.Stat(filepath.Join(root, "human"))
		if err != nil || di.Mode().Perm() != 0o700 {
			t.Errorf("dir %v, %v, want 0700", di, err)
		}
	}
	got, ok, err := c.Load()
	if err != nil || !ok || got.AccessToken != want.AccessToken || got.ProviderRefreshToken != "r1" || !got.ExpiresAt.Equal(want.ExpiresAt) || got.Discovery != want.Discovery {
		t.Fatalf("Load = %+v, %v, %v", got, ok, err)
	}
	if err := c.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Load(); ok {
		t.Error("Load after Remove found a session")
	}
	// A zero Dir disables the cache.
	z := HumanCache{}
	if err := z.Save(want); err != nil {
		t.Errorf("Save with no Dir: %v", err)
	}
	if _, ok, err := z.Load(); ok || err != nil {
		t.Errorf("Load with no Dir = %v, %v", ok, err)
	}
	// An unreadable file is an error, not a session.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.Load(); ok || err == nil {
		t.Errorf("Load of a corrupt file = %v, %v", ok, err)
	}
}

// A request deadline shorter than the browser's answer must not abort the
// sign-in (the TUI's bus calls carry a 10 s deadline); cancellation still does.
func TestHumanSourceSignInIgnoresCallerDeadline(t *testing.T) {
	h := newHumanEnv(t, false)
	s := h.source(t, t.TempDir())
	slow := s.OIDC.OpenBrowser
	s.OIDC.OpenBrowser = func(u string) error { time.Sleep(300 * time.Millisecond); return slow(u) }
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.SignIn(ctx); err != nil {
		t.Fatalf("SignIn with a 50 ms deadline: %v", err)
	}
	cctx, ccancel := context.WithCancel(t.Context())
	s.OIDC.OpenBrowser = func(string) error { ccancel(); return nil }
	if _, err := s.SignIn(cctx); err == nil {
		t.Fatal("SignIn survived cancellation")
	}
}
