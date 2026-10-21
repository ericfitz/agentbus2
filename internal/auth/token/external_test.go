package token

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
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
}

func newJWKS(t *testing.T, ks ...crypto.Signer) *jwksServer {
	t.Helper()
	s := &jwksServer{status: 200}
	s.setKeys(t, ks...)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.fetches.Add(1)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.status != 200 {
			http.Error(w, "boom", s.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.set)
	}))
	t.Cleanup(s.Close)
	return s
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
	if _, err := e2.Verify(context.Background(), raw); !errors.Is(err, ErrInvalid) {
		t.Fatalf("restricted: %v", err)
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
	cases := map[string]func(*jwt.Claims){
		"unmapped": func(c *jwt.Claims) { c.Subject = "stranger" },
		"audience": func(c *jwt.Claims) { c.Audience = jwt.Audience{"someone-else"} },
		"issuer":   func(c *jwt.Claims) { c.Issuer = "https://evil.example" },
		"expired":  func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(t0.Add(-5 * time.Minute)) },
		"no exp":   func(c *jwt.Claims) { c.Expiry = nil },
		"no sub":   func(c *jwt.Claims) { c.Subject = "" },
	}
	for name, mut := range cases {
		_, err := e.Verify(ctx, extToken(t, k, mut))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
		if name == "unmapped" && (err == nil || !strings.Contains(err.Error(), `subject "stranger" is not mapped`)) {
			t.Errorf("unmapped text: %v", err)
		}
	}
	// A forged signature under a known kid.
	kid, _ := keys.KID(k.Public())
	forged := signRaw(t, genKey(t, keys.EdDSA), kid, jwt.Claims{Issuer: extIss, Subject: "idp-user-1",
		Audience: jwt.Audience{extAud}, Expiry: jwt.NewNumericDate(t0.Add(time.Hour))})
	if _, err := e.Verify(ctx, forged); !errors.Is(err, ErrInvalid) {
		t.Errorf("forged: %v", err)
	}
	// An empty audience or issuer in the configuration never matches everything.
	bad := newExternal(s, &clock{t: t0})
	bad.Audience = ""
	if _, err := bad.Verify(ctx, extToken(t, k, nil)); err == nil {
		t.Error("empty audience accepted")
	}
	// A fetch failure is an error, not an accept.
	s.mu.Lock()
	s.status = 500
	s.mu.Unlock()
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
