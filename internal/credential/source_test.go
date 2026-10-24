package credential

import (
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/assertion"
	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/gen"
)

var t0 = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// fakeServer serves discovery, the token endpoint and a protected /v1/echo.
type fakeServer struct {
	srv         *httptest.Server
	key         crypto.Signer
	exchanges   atomic.Int32
	discoveries atomic.Int32
	failTokens  atomic.Bool
	// onAssertion, when set, sees each parsed assertion and its compact form.
	onAssertion func(assertion.Parsed, string)

	mu         sync.Mutex
	advertised string
	issuer     string
	issuers    []string
	// accept decides whether a bearer is valid on /v1/echo.
	accept func(token string, body []byte) bool
	echoes atomic.Int32
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	f.key = k
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		f.discoveries.Add(1)
		f.mu.Lock()
		ep, iss := f.advertised, f.issuer
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": iss, "token_endpoint": ep, "jwks_uri": "http://issuer/jwks"})
	})
	mux.HandleFunc("/v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		if f.failTokens.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p, err := assertion.Parse(r.PostForm.Get("client_assertion"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if f.onAssertion != nil {
			f.onAssertion(p, r.PostForm.Get("client_assertion"))
		}
		f.mu.Lock()
		iss := f.issuer
		f.mu.Unlock()
		if err := p.Verify(f.key.Public(), []string{iss}, p.Expiry.Add(-time.Minute), 0); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.issuers = append(f.issuers, p.Identity)
		f.mu.Unlock()
		n := f.exchanges.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"t%d","token_type":"Bearer","expires_in":3600}`, n)
	})
	mux.HandleFunc("/v1/echo", func(w http.ResponseWriter, r *http.Request) {
		f.echoes.Add(1)
		var body []byte
		if r.Body != nil {
			buf := make([]byte, 0, 2048)
			tmp := make([]byte, 512)
			for {
				n, err := r.Body.Read(tmp)
				buf = append(buf, tmp[:n]...)
				if err != nil {
					break
				}
			}
			body = buf
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		ok := f.accept != nil && f.accept(tok, body)
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(body)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.advertised = f.srv.URL + "/v1/auth/token"
	f.issuer = "http://issuer"
	return f
}

func (f *fakeServer) source(t *testing.T, cache Cache, now time.Time) *Source {
	t.Helper()
	c, err := gen.NewClientWithResponses(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &Source{Server: f.srv.URL, Identity: "pat", Signer: f.key, Client: c, Cache: cache, Now: func() time.Time { return now }}
}

func TestSourceSignsAndExchanges(t *testing.T) {
	f := newFakeServer(t)
	s := f.source(t, Cache{Dir: t.TempDir()}, t0)
	tok, err := s.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "t1" {
		t.Errorf("token %q", tok)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.issuers) != 1 || f.issuers[0] != "pat" {
		t.Errorf("issuers %v", f.issuers)
	}
}

func TestSourceCachesToken(t *testing.T) {
	f := newFakeServer(t)
	cache := Cache{Dir: filepath.Join(t.TempDir(), "tokens", "lab")}
	if tok, err := f.source(t, cache, t0).Token(t.Context()); err != nil || tok != "t1" {
		t.Fatalf("%q %v", tok, err)
	}
	// A second source over the same cache reuses the bearer.
	if tok, err := f.source(t, cache, t0.Add(time.Minute)).Token(t.Context()); err != nil || tok != "t1" {
		t.Fatalf("second source: %q %v", tok, err)
	}
	if n := f.exchanges.Load(); n != 1 {
		t.Errorf("%d exchanges, want 1", n)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(cache.Dir, "pat.json"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("cache mode %v", fi.Mode().Perm())
		}
		di, _ := os.Stat(cache.Dir)
		if di.Mode().Perm() != 0o700 {
			t.Errorf("cache dir mode %v", di.Mode().Perm())
		}
	}
	// 30 s before expiry is inside the skew: a new exchange.
	near := t0.Add(3600*time.Second - 30*time.Second)
	if tok, err := f.source(t, cache, near).Token(t.Context()); err != nil || tok != "t2" {
		t.Fatalf("near expiry: %q %v", tok, err)
	}
	if n := f.exchanges.Load(); n != 2 {
		t.Errorf("%d exchanges, want 2", n)
	}
	// Exactly Skew before expiry is not "more than Skew".
	exp := t0.Add(3600 * time.Second)
	cache2 := Cache{Dir: t.TempDir()}
	f.source(t, cache2, t0).Token(t.Context()) //nolint:errcheck
	before := f.exchanges.Load()
	f.source(t, cache2, exp.Add(-Skew)).Token(t.Context()) //nolint:errcheck
	if f.exchanges.Load() != before+1 {
		t.Errorf("a token with exactly Skew left was reused")
	}
}

func TestSourceNoCacheDir(t *testing.T) {
	f := newFakeServer(t)
	s := f.source(t, Cache{}, t0)
	for i := 0; i < 2; i++ {
		if _, err := s.Token(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.exchanges.Load(); n != 2 {
		t.Errorf("%d exchanges without a cache, want 2", n)
	}
}

func TestSourceCorruptCacheIsAMiss(t *testing.T) {
	f := newFakeServer(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pat.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := f.source(t, Cache{Dir: dir}, t0).Token(t.Context())
	if err != nil || tok != "t1" {
		t.Fatalf("%q %v", tok, err)
	}
	e, ok, err := Cache{Dir: dir}.Load("pat")
	if err != nil || !ok || e.AccessToken != "t1" {
		t.Errorf("cache not rewritten: %+v %v %v", e, ok, err)
	}
}

func TestSourceProcessPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fake process")
	}
	argv := []string{"bash", filepath.Join("testdata", "credproc.sh")}
	t.Run("token", func(t *testing.T) {
		f := newFakeServer(t)
		t.Setenv("CREDPROC_MODE", "token")
		t.Setenv("CREDPROC_TOKEN", "from-vault")
		t.Setenv("CREDPROC_EXPIRES", t0.Add(time.Hour).Format(time.RFC3339))
		count := filepath.Join(t.TempDir(), "count")
		t.Setenv("CREDPROC_COUNT", count)
		cache := Cache{Dir: t.TempDir()}
		mk := func() *Source {
			s := f.source(t, cache, t0)
			s.Signer, s.Process = nil, argv
			return s
		}
		for i := 0; i < 2; i++ {
			tok, err := mk().Token(t.Context())
			if err != nil || tok != "from-vault" {
				t.Fatalf("%q %v", tok, err)
			}
		}
		if n := f.exchanges.Load(); n != 0 {
			t.Errorf("%d exchanges, want 0", n)
		}
		if b, _ := os.ReadFile(count); strings.Count(string(b), "run") != 1 {
			t.Errorf("process ran %q times, want 1 (the result is cached until expiry)", b)
		}
	})
	t.Run("assertion", func(t *testing.T) {
		f := newFakeServer(t)
		raw, err := assertion.Sign(assertion.Input{Identity: "pat", Audience: "http://issuer", Key: f.key, KID: mustKID(t, f.key), Now: t0})
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("CREDPROC_MODE", "assertion")
		t.Setenv("CREDPROC_ASSERTION", raw)
		s := f.source(t, Cache{Dir: t.TempDir()}, t0)
		s.Signer, s.Process = nil, argv
		tok, err := s.Token(t.Context())
		if err != nil || tok != "t1" {
			t.Fatalf("%q %v", tok, err)
		}
		if n := f.exchanges.Load(); n != 1 {
			t.Errorf("%d exchanges, want 1", n)
		}
	})
	t.Run("pem", func(t *testing.T) {
		f := newFakeServer(t)
		pem, _ := keys.EncodePrivatePEM(f.key)
		t.Setenv("CREDPROC_MODE", "pem")
		t.Setenv("CREDPROC_PEM", string(pem))
		count := filepath.Join(t.TempDir(), "count")
		t.Setenv("CREDPROC_COUNT", count)
		cache := Cache{Dir: t.TempDir()}
		s := f.source(t, cache, t0)
		s.Signer, s.Process = nil, argv
		if tok, err := s.Token(t.Context()); err != nil || tok != "t1" {
			t.Fatalf("%q %v", tok, err)
		}
		// A forced refresh in the same process reuses the key held in memory.
		if tok, err := s.Refresh(t.Context()); err != nil || tok != "t2" {
			t.Fatalf("%q %v", tok, err)
		}
		if b, _ := os.ReadFile(count); strings.Count(string(b), "run") != 1 {
			t.Errorf("process ran %q times, want 1", b)
		}
		ents, _ := os.ReadDir(cache.Dir)
		if len(ents) != 1 || ents[0].Name() != "pat.json" {
			t.Errorf("cache dir holds %v, want only pat.json", ents)
		}
		b, _ := os.ReadFile(filepath.Join(cache.Dir, "pat.json"))
		if strings.Contains(string(b), "PRIVATE KEY") {
			t.Error("private key written to the cache")
		}
	})
	t.Run("failure", func(t *testing.T) {
		f := newFakeServer(t)
		t.Setenv("CREDPROC_MODE", "exit1")
		s := f.source(t, Cache{Dir: t.TempDir()}, t0)
		s.Signer, s.Process = nil, argv
		if _, err := s.Token(t.Context()); err == nil || !strings.Contains(err.Error(), "bash") {
			t.Errorf("err %v", err)
		}
	})
}

func mustKID(t *testing.T, k crypto.Signer) string {
	t.Helper()
	kid, err := keys.KID(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	return kid
}

func TestSourceDiscoveryCached(t *testing.T) {
	f := newFakeServer(t)
	cache := Cache{Dir: t.TempDir()}
	if _, err := f.source(t, cache, t0).Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A new source over the same cache, forced to refresh, does not rediscover.
	if tok, err := f.source(t, cache, t0.Add(2*time.Hour)).Token(t.Context()); err != nil || tok != "t2" {
		t.Fatalf("%q %v", tok, err)
	}
	if n := f.discoveries.Load(); n != 1 {
		t.Errorf("%d discovery requests, want 1", n)
	}
}

func TestSourceAudienceFromDocument(t *testing.T) {
	f := newFakeServer(t)
	f.mu.Lock()
	f.advertised = "http://0.0.0.0:1/v1/auth/token"
	f.mu.Unlock()
	tok, err := f.source(t, Cache{Dir: t.TempDir()}, t0).Token(t.Context())
	if err != nil || tok != "t1" {
		t.Fatalf("%q %v", tok, err)
	}
}

func TestSourceRefusesWithoutCredentials(t *testing.T) {
	f := newFakeServer(t)
	s := f.source(t, Cache{}, t0)
	s.Signer = nil
	if _, err := s.Token(t.Context()); err == nil {
		t.Error("no key and no process accepted")
	}
}

func TestCacheNameRule(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	for _, n := range []string{"a/b", "..", ""} {
		if err := c.Save(n, Entry{AccessToken: "x"}); err == nil {
			t.Errorf("Save(%q) accepted", n)
		}
		if _, _, err := c.Load(n); err == nil {
			t.Errorf("Load(%q) accepted", n)
		}
		if err := c.Remove(n); err == nil {
			t.Errorf("Remove(%q) accepted", n)
		}
	}
	if err := c.Remove("absent"); err != nil {
		t.Errorf("Remove of an absent entry: %v", err)
	}
}

func TestSourceCacheBoundToServer(t *testing.T) {
	a, b := newFakeServer(t), newFakeServer(t)
	b.key = a.key // the same credential registered on both servers
	cache := Cache{Dir: t.TempDir()}
	if tok, err := a.source(t, cache, t0).Token(t.Context()); err != nil || tok != "t1" {
		t.Fatalf("%q %v", tok, err)
	}
	// The profile now points at b: a's bearer and discovery are not used.
	if _, err := b.source(t, cache, t0).Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := b.exchanges.Load(); n != 1 {
		t.Errorf("%d exchanges at the new server, want 1 (the old server's bearer was reused)", n)
	}
	if n := b.discoveries.Load(); n != 1 {
		t.Errorf("%d discoveries at the new server, want 1", n)
	}
	e, ok, err := cache.Load("pat")
	if err != nil || !ok || e.Server != b.srv.URL {
		t.Errorf("entry %+v %v %v", e, ok, err)
	}
}

func TestProcessInputAudienceIsIssuer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash fake process")
	}
	f := newFakeServer(t)
	f.mu.Lock()
	f.advertised = "http://0.0.0.0:1/v1/auth/token"
	f.mu.Unlock()
	capDir := t.TempDir()
	t.Setenv("CREDPROC_MODE", "token")
	t.Setenv("CREDPROC_TOKEN", "from-vault")
	t.Setenv("CREDPROC_EXPIRES", t0.Add(time.Hour).Format(time.RFC3339))
	jtis := map[string]bool{}
	for i := 0; i < 2; i++ {
		capFile := filepath.Join(capDir, fmt.Sprint(i))
		t.Setenv("CREDPROC_CAPTURE", capFile)
		s := f.source(t, Cache{}, t0)
		s.Signer, s.Process = nil, []string{"bash", filepath.Join("testdata", "credproc.sh")}
		if _, err := s.Token(t.Context()); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(capFile)
		var in ProcessInput
		if err := json.Unmarshal(b, &in); err != nil {
			t.Fatalf("stdin %q: %v", b, err)
		}
		if in.Version != 1 || in.Identity != "pat" || in.TokenEndpoint != "http://0.0.0.0:1/v1/auth/token" || in.Audience != "http://issuer" {
			t.Errorf("stdin %s", b)
		}
		if len(in.JTI) != 22 {
			t.Errorf("jti %q, want 22 characters", in.JTI)
		}
		jtis[in.JTI] = true
	}
	if len(jtis) != 2 {
		t.Errorf("the jti was reused across runs: %v", jtis)
	}
}

// The assertion names the issuer, not the token endpoint, as its audience
// (draft-ietf-oauth-rfc7523bis), even when the endpoint is advertised elsewhere.
func TestMintSignsForIssuer(t *testing.T) {
	f := newFakeServer(t)
	f.mu.Lock()
	f.advertised = "http://0.0.0.0:1/v1/auth/token"
	f.mu.Unlock()
	var got []string
	f.onAssertion = func(p assertion.Parsed, raw string) {
		parts := strings.Split(raw, ".")
		b, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var c jwt.Claims
		_ = json.Unmarshal(b, &c)
		got = []string(c.Audience)
	}
	if _, err := f.source(t, Cache{}, t0).Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "http://issuer" {
		t.Errorf("aud = %v, want [http://issuer]", got)
	}
}

// A cached discovery whose issuer the server no longer advertises
// does not wedge the profile: a failed exchange rediscovers once and retries.
func TestStaleCachedDiscoveryIsRediscovered(t *testing.T) {
	f := newFakeServer(t)
	cache := Cache{Dir: t.TempDir()}
	if _, err := f.source(t, cache, t0).Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.issuer = "http://issuer-moved-1"
	f.mu.Unlock()
	later := t0.Add(2 * time.Hour) // the cached bearer has expired
	if _, err := f.source(t, cache, later).Token(context.Background()); err != nil {
		t.Fatalf("Token after the issuer moved: %v", err)
	}
	if e, ok, _ := cache.Load("pat"); !ok || e.Discovery.Issuer != "http://issuer-moved-1" {
		t.Fatalf("cached discovery %+v, want the new issuer", e.Discovery)
	}
	f.mu.Lock()
	f.issuer = "http://issuer-moved-2"
	f.mu.Unlock()
	if _, err := f.source(t, cache, later).Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after the issuer moved again: %v", err)
	}
}

// The token exchange is bounded like discovery and the process.
func TestTokenExchangeIsBounded(t *testing.T) {
	prev := exchangeTimeout
	exchangeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { exchangeTimeout = prev })
	block := make(chan struct{})
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": "x", "token_endpoint": srv.URL + "/v1/auth/token"})
	})
	mux.HandleFunc("/v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(block) })
	k, _ := keys.Generate(keys.EdDSA)
	c, _ := gen.NewClientWithResponses(srv.URL, gen.WithHTTPClient(&http.Client{}))
	s := &Source{Server: srv.URL, Identity: "pat", Signer: k, Client: c, Cache: Cache{Dir: t.TempDir()}}
	done := make(chan error, 1)
	go func() { _, err := s.Token(context.Background()); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Token succeeded against a server that never answers")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Token still blocked: the exchange is unbounded")
	}
}
