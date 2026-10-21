package token

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

const (
	extIss = "https://idp.example"
	extAud = "agentbus2"
)

type jwksServer struct {
	*httptest.Server
	fetches atomic.Int32
	mu      sync.Mutex
	set     jose.JSONWebKeySet
	status  int
	gate    chan struct{} // non-nil: a request waits for it to close before it answers
}

func newJWKS(t *testing.T, ks ...crypto.Signer) *jwksServer {
	t.Helper()
	s := &jwksServer{status: 200}
	s.setKeys(t, ks...)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.fetches.Add(1)
		s.mu.Lock()
		status, set, gate := s.status, s.set, s.gate
		s.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		if status != 200 {
			http.Error(w, "boom", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) setStatus(code int) {
	s.mu.Lock()
	s.status = code
	s.mu.Unlock()
}

// block makes the server hold every request until the returned release runs (at the latest at cleanup).
func (s *jwksServer) block(t *testing.T) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	s.mu.Lock()
	s.gate = gate
	s.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release) // runs before the server's Close, which waits for requests
	return release
}

// waitFetches waits until the server has seen n requests.
func (s *jwksServer) waitFetches(t *testing.T, n int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.fetches.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("server saw %d requests, want %d", s.fetches.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *jwksServer) setKeys(t *testing.T, ks ...crypto.Signer) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set = jose.JSONWebKeySet{}
	for _, k := range ks {
		kid, _ := keys.KID(k.Public())
		j, err := keys.ToJWK(k.Public(), kid)
		if err != nil {
			t.Fatal(err)
		}
		s.set.Keys = append(s.set.Keys, j)
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func extToken(t *testing.T, k crypto.Signer, mut func(*jwt.Claims)) string {
	t.Helper()
	kid, _ := keys.KID(k.Public())
	c := jwt.Claims{
		Issuer: extIss, Subject: "idp-user-1", Audience: jwt.Audience{extAud}, ID: "j",
		IssuedAt: jwt.NewNumericDate(t0), Expiry: jwt.NewNumericDate(t0.Add(time.Hour)),
	}
	if mut != nil {
		mut(&c)
	}
	return signRaw(t, k, kid, c)
}

func newExternal(s *jwksServer, clk *clock) *External {
	return &External{
		Issuer: extIss, JWKSURI: s.URL, Audience: extAud,
		Subjects: map[string]string{"idp-user-1": "alice"},
		Now:      clk.Now,
	}
}

func genRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestExternalVerifyMapsSubject(t *testing.T) {
	for _, alg := range bothAlgs {
		k := genKey(t, alg)
		s := newJWKS(t, k)
		e := newExternal(s, &clock{t: t0})
		id, err := e.Verify(context.Background(), extToken(t, k, nil))
		if err != nil || id != "alice" {
			t.Fatalf("%s: %q, %v", alg, id, err)
		}
		// The cache serves a second token without another fetch.
		if _, err := e.Verify(context.Background(), extToken(t, k, nil)); err != nil {
			t.Fatal(err)
		}
		if n := s.fetches.Load(); n != 1 {
			t.Errorf("%s: %d fetches", alg, n)
		}
	}
}

func TestExternalRS256(t *testing.T) {
	// RS256 is in the default allowlist for external issuers.
	k := genRSA(t)
	s := &jwksServer{status: 200}
	s.set = jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "rsa1", Algorithm: "RS256", Use: "sig"}}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(s.set)
	}))
	defer s.Close()
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k}, (&jose.SignerOptions{}).WithHeader("kid", "rsa1"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(sg).Claims(jwt.Claims{Issuer: extIss, Subject: "idp-user-1", Audience: jwt.Audience{extAud},
		Expiry: jwt.NewNumericDate(t0.Add(time.Hour))}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	e := newExternal(s, &clock{t: t0})
	if id, err := e.Verify(context.Background(), raw); err != nil || id != "alice" {
		t.Fatalf("%q %v", id, err)
	}
	// Restricting the list to EdDSA refuses it.
	e2 := newExternal(s, &clock{t: t0})
	e2.Algorithms = []jose.SignatureAlgorithm{jose.EdDSA}
	var ua *jose.ErrUnexpectedSignatureAlgorithm
	if _, err := e2.Verify(context.Background(), raw); !errors.Is(err, ErrInvalid) || !errors.As(err, &ua) {
		t.Fatalf("restricted: %v, want ErrInvalid and *jose.ErrUnexpectedSignatureAlgorithm", err)
	}
}

func TestExternalRefetchOnUnknownKidOnce(t *testing.T) {
	k1 := genKey(t, keys.EdDSA)
	k2 := genKey(t, keys.EdDSA)
	s := newJWKS(t, k1)
	clk := &clock{t: t0}
	e := newExternal(s, clk)
	ctx := context.Background()
	if _, err := e.Verify(ctx, extToken(t, k1, nil)); err != nil {
		t.Fatal(err)
	}
	// The provider rotates in k2; the first unknown-kid token within a minute of the fetch does not refetch.
	s.setKeys(t, k1, k2)
	if _, err := e.Verify(ctx, extToken(t, k2, nil)); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("within a minute: %v", err)
	}
	if n := s.fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1", n)
	}
	// Past a minute (inside the TTL) the unknown kid causes one refetch, which finds k2.
	clk.Add(2 * time.Minute)
	if _, err := e.Verify(ctx, extToken(t, k2, func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(t0.Add(time.Hour)) })); err != nil {
		t.Fatalf("after refetch: %v", err)
	}
	if n := s.fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want 2", n)
	}
	// A kid nobody has: one refetch after a minute, none for the second token.
	k3 := genKey(t, keys.EdDSA)
	clk.Add(2 * time.Minute)
	for i := 0; i < 2; i++ {
		if _, err := e.Verify(ctx, extToken(t, k3, nil)); !errors.Is(err, ErrUnknownKey) {
			t.Fatalf("unknown %d: %v", i, err)
		}
	}
	if n := s.fetches.Load(); n != 3 {
		t.Fatalf("fetches = %d, want 3", n)
	}
	// Past the TTL the cache is refetched even for a known kid.
	clk.Add(10 * time.Minute)
	if _, err := e.Verify(ctx, extToken(t, k1, nil)); err != nil {
		t.Fatal(err)
	}
	if n := s.fetches.Load(); n != 4 {
		t.Fatalf("fetches = %d, want 4", n)
	}
}

func TestExternalRefusesUnmappedAndWrongAudience(t *testing.T) {
	k := genKey(t, keys.EdDSA)
	s := newJWKS(t, k)
	e := newExternal(s, &clock{t: t0})
	ctx := context.Background()
	// cause is the go-jose sentinel that must be in the chain next to ErrInvalid.
	cases := map[string]struct {
		mut   func(*jwt.Claims)
		cause error
	}{
		"unmapped": {func(c *jwt.Claims) { c.Subject = "stranger" }, nil},
		"audience": {func(c *jwt.Claims) { c.Audience = jwt.Audience{"someone-else"} }, jwt.ErrInvalidAudience},
		"issuer":   {func(c *jwt.Claims) { c.Issuer = "https://evil.example" }, jwt.ErrInvalidIssuer},
		"expired":  {func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(t0.Add(-5 * time.Minute)) }, jwt.ErrExpired},
		"no exp":   {func(c *jwt.Claims) { c.Expiry = nil }, nil},
		"no sub":   {func(c *jwt.Claims) { c.Subject = "" }, nil},
	}
	for name, c := range cases {
		_, err := e.Verify(ctx, extToken(t, k, c.mut))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
		if c.cause != nil && !errors.Is(err, c.cause) {
			t.Errorf("%s: %v, want %v in the chain", name, err, c.cause)
		}
		if name == "unmapped" && (!errors.Is(err, ErrUnmappedSubject) || !strings.Contains(err.Error(), `subject "stranger" is not mapped`)) {
			t.Errorf("unmapped text: %v", err)
		}
	}
	// A forged signature under a known kid.
	kid, _ := keys.KID(k.Public())
	forged := signRaw(t, genKey(t, keys.EdDSA), kid, jwt.Claims{Issuer: extIss, Subject: "idp-user-1",
		Audience: jwt.Audience{extAud}, Expiry: jwt.NewNumericDate(t0.Add(time.Hour))})
	if _, err := e.Verify(ctx, forged); !errors.Is(err, ErrInvalid) || !errors.Is(err, jose.ErrCryptoFailure) {
		t.Errorf("forged: %v", err)
	}
	// An empty audience or issuer in the configuration never matches everything.
	bad := newExternal(s, &clock{t: t0})
	bad.Audience = ""
	if _, err := bad.Verify(ctx, extToken(t, k, nil)); err == nil {
		t.Error("empty audience accepted")
	}
	// A fetch failure is an error, not an accept.
	s.setStatus(500)
	e3 := newExternal(s, &clock{t: t0})
	if _, err := e3.Verify(ctx, extToken(t, k, nil)); err == nil {
		t.Error("fetch failure accepted")
	}
}

func TestVerifierRoutesByIssuer(t *testing.T) {
	ek := genKey(t, keys.ES256)
	s := newJWKS(t, ek)
	clk := &clock{t: t0}
	own := newIssuer(t, keys.EdDSA, issURL, nil)
	v := Verifier{Own: own, External: []*External{newExternal(s, clk)}, Now: clk.Now}
	ctx := context.Background()

	raw, _, _ := own.Mint("bob")
	id, ext, err := v.Verify(ctx, raw)
	if err != nil || id != "bob" || ext {
		t.Errorf("own: %q %v %v", id, ext, err)
	}
	id, ext, err = v.Verify(ctx, extToken(t, ek, nil))
	if err != nil || id != "alice" || !ext {
		t.Errorf("external: %q %v %v", id, ext, err)
	}
	other := genKey(t, keys.EdDSA)
	if _, _, err := v.Verify(ctx, extToken(t, other, func(c *jwt.Claims) { c.Issuer = "https://unknown.example" })); !errors.Is(err, ErrUnknownIssuer) {
		t.Errorf("unknown: %v", err)
	}
	if _, _, err := v.Verify(ctx, extToken(t, other, func(c *jwt.Claims) { c.Issuer = "" })); !errors.Is(err, ErrUnknownIssuer) {
		t.Errorf("no iss: %v", err)
	}
	for _, bad := range []string{"", "abc", "a.b.c", "a.!!.c"} {
		if _, _, err := v.Verify(ctx, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	// No own issuer: its tokens have an unknown issuer.
	v2 := Verifier{External: v.External, Now: clk.Now}
	if _, _, err := v2.Verify(ctx, raw); !errors.Is(err, ErrUnknownIssuer) {
		t.Errorf("no own: %v", err)
	}
	// A token for the own issuer is never routed to an external one that names the same URL.
	if iss, err := IssuerOf(raw); err != nil || iss != issURL {
		t.Errorf("IssuerOf = %q, %v", iss, err)
	}
}

func TestIssuerOfKeepsPayloadCause(t *testing.T) {
	var b64err base64.CorruptInputError
	if _, err := IssuerOf("a.!!.c"); !errors.Is(err, ErrInvalid) || !errors.As(err, &b64err) {
		t.Errorf("bad base64: %v, want ErrInvalid and a base64.CorruptInputError", err)
	}
	var syn *json.SyntaxError
	notJSON := "a." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".c"
	if _, err := IssuerOf(notJSON); !errors.Is(err, ErrInvalid) || !errors.As(err, &syn) {
		t.Errorf("bad json: %v, want ErrInvalid and a *json.SyntaxError", err)
	}
}

func TestExternalServesCachedSetWhenRefreshFails(t *testing.T) {
	k := genKey(t, keys.EdDSA)
	s := newJWKS(t, k)
	clk := &clock{t: t0}
	e := newExternal(s, clk)
	ctx := context.Background()
	if _, err := e.Verify(ctx, extToken(t, k, nil)); err != nil {
		t.Fatal(err)
	}
	// The IdP goes down and the TTL lapses: the cached set keeps serving, with one failed attempt.
	s.setStatus(500)
	clk.Add(10 * time.Minute)
	for i := 0; i < 4; i++ {
		if id, err := e.Verify(ctx, extToken(t, k, nil)); err != nil || id != "alice" {
			t.Fatalf("outage %d: %q, %v", i, id, err)
		}
	}
	if n := s.fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want 2 (the first, then one failed refresh)", n)
	}
	// A kid the stale set lacks is reported as the fetch failure, not as an unknown key.
	other := genKey(t, keys.EdDSA)
	if _, err := e.Verify(ctx, extToken(t, other, nil)); err == nil || errors.Is(err, ErrUnknownKey) || errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "status 500") {
		t.Fatalf("unknown kid during an outage: %v, want the fetch failure", err)
	}
	// A minute after the failed attempt there is one more try.
	clk.Add(2 * time.Minute)
	if _, err := e.Verify(ctx, extToken(t, k, nil)); err != nil {
		t.Fatal(err)
	}
	if n := s.fetches.Load(); n != 3 {
		t.Fatalf("fetches = %d, want 3", n)
	}
	// The IdP recovers: the next due attempt succeeds, and the cache is fresh again.
	s.setStatus(200)
	clk.Add(2 * time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := e.Verify(ctx, extToken(t, k, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.fetches.Load(); n != 4 {
		t.Fatalf("fetches = %d, want 4", n)
	}
}

func TestExternalFailedFirstFetchBacksOff(t *testing.T) {
	k := genKey(t, keys.EdDSA)
	s := newJWKS(t, k)
	s.setStatus(500)
	clk := &clock{t: t0}
	e := newExternal(s, clk)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := e.Verify(ctx, extToken(t, k, nil))
		if err == nil || !strings.Contains(err.Error(), "status 500") {
			t.Fatalf("attempt %d: %v, want the fetch failure", i, err)
		}
	}
	if n := s.fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1 within a minute", n)
	}
	clk.Add(2 * time.Minute)
	if _, err := e.Verify(ctx, extToken(t, k, nil)); err == nil {
		t.Fatal("accepted while the IdP is down")
	}
	if n := s.fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want 2", n)
	}
	s.setStatus(200)
	clk.Add(2 * time.Minute)
	if id, err := e.Verify(ctx, extToken(t, k, nil)); err != nil || id != "alice" {
		t.Fatalf("after recovery: %q, %v", id, err)
	}
}

func TestExternalOutageSharesOneFetch(t *testing.T) {
	k := genKey(t, keys.EdDSA)
	s := newJWKS(t, k)
	s.setStatus(500)
	release := s.block(t)
	e := newExternal(s, &clock{t: t0})
	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		raw := extToken(t, k, nil)
		go func() {
			_, err := e.Verify(context.Background(), raw)
			errs <- err
		}()
	}
	s.waitFetches(t, 1)
	release()
	for i := 0; i < n; i++ {
		if err := <-errs; err == nil {
			t.Error("accepted while the IdP is down")
		}
	}
	if got := s.fetches.Load(); got != 1 {
		t.Fatalf("fetches = %d, want 1 shared by all %d requests", got, n)
	}
}

func TestExternalCachedKeyDoesNotWaitForRefresh(t *testing.T) {
	k := genKey(t, keys.EdDSA)
	s := newJWKS(t, k)
	clk := &clock{t: t0}
	e := newExternal(s, clk)
	ctx := context.Background()
	if _, err := e.Verify(ctx, extToken(t, k, nil)); err != nil {
		t.Fatal(err)
	}
	clk.Add(10 * time.Minute) // past the TTL
	release := s.block(t)
	first := make(chan error, 1)
	rawA := extToken(t, k, nil)
	go func() { _, err := e.Verify(ctx, rawA); first <- err }()
	s.waitFetches(t, 2) // the refresh is in flight and held by the server
	second := make(chan error, 1)
	rawB := extToken(t, k, nil)
	go func() { _, err := e.Verify(ctx, rawB); second <- err }()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("request served from the cached set: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a request for a cached key queued behind the refresh")
	}
	release()
	if err := <-first; err != nil {
		t.Fatalf("request that refreshed: %v", err)
	}
	if n := s.fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want 2", n)
	}
}

func TestExternalUnknownKidWaitsForRefreshInFlight(t *testing.T) {
	k1 := genKey(t, keys.EdDSA)
	k2 := genKey(t, keys.EdDSA)
	s := newJWKS(t, k1)
	clk := &clock{t: t0}
	e := newExternal(s, clk)
	ctx := context.Background()
	if _, err := e.Verify(ctx, extToken(t, k1, nil)); err != nil {
		t.Fatal(err)
	}
	// The provider rotates in k2 and the TTL lapses; a refresh is in flight.
	s.setKeys(t, k1, k2)
	clk.Add(10 * time.Minute)
	release := s.block(t)
	first := make(chan error, 1)
	rawA := extToken(t, k1, nil)
	go func() { _, err := e.Verify(ctx, rawA); first <- err }()
	s.waitFetches(t, 2)
	// A token under the new kid waits for that refresh instead of failing as unknown.
	second := make(chan error, 1)
	rawB := extToken(t, k2, nil)
	go func() { _, err := e.Verify(ctx, rawB); second <- err }()
	select {
	case err := <-second:
		t.Fatalf("returned before the refresh finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-second; err != nil {
		t.Fatalf("token under the rotated-in kid: %v", err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if n := s.fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want 2", n)
	}
}

func TestExternalCanceledRequestDoesNotFailTheFetch(t *testing.T) {
	k := genKey(t, keys.EdDSA)
	s := newJWKS(t, k)
	release := s.block(t)
	e := newExternal(s, &clock{t: t0})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	raw := extToken(t, k, nil)
	go func() { _, err := e.Verify(ctx, raw); done <- err }()
	s.waitFetches(t, 1)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled request: %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled request did not return")
	}
	// The fetch the canceled request started still lands; the next request uses it and does not fetch again.
	release()
	if id, err := e.Verify(context.Background(), extToken(t, k, nil)); err != nil || id != "alice" {
		t.Fatalf("after cancellation: %q, %v", id, err)
	}
	if n := s.fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1", n)
	}
}
