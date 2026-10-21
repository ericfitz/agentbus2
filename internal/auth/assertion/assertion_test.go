package assertion

import (
	"crypto"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

const endpoint = "https://bus.example/v1/auth/token"

var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

var both = []keys.Algorithm{keys.EdDSA, keys.ES256}

func gen(t *testing.T, alg keys.Algorithm) (crypto.Signer, string) {
	t.Helper()
	k, err := keys.Generate(alg)
	if err != nil {
		t.Fatal(err)
	}
	kid, err := keys.KID(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	return k, kid
}

func sign(t *testing.T, alg keys.Algorithm) (string, crypto.Signer, string) {
	t.Helper()
	k, kid := gen(t, alg)
	raw, err := Sign(Input{Identity: "alice", TokenEndpoint: endpoint, Key: k, KID: kid, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return raw, k, kid
}

// custom signs arbitrary claims and headers with a real key, for malformed shapes.
func custom(t *testing.T, k crypto.Signer, kid string, claims jwt.Claims, extra map[string]any) string {
	t.Helper()
	alg := jose.EdDSA
	if a, _ := keys.AlgorithmOf(k.Public()); a == keys.ES256 {
		alg = jose.ES256
	}
	opts := (&jose.SignerOptions{}).WithType("JWT")
	if kid != "" {
		opts = opts.WithHeader("kid", kid)
	}
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: k}, opts)
	if err != nil {
		t.Fatal(err)
	}
	b := jwt.Signed(s).Claims(claims)
	if extra != nil {
		b = b.Claims(extra)
	}
	raw, err := b.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func goodClaims() jwt.Claims {
	return jwt.Claims{
		Issuer: "alice", Subject: "alice", Audience: jwt.Audience{endpoint}, ID: "j1",
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute)),
	}
}

func TestSignThenParseAndVerify(t *testing.T) {
	for _, alg := range both {
		raw, k, kid := sign(t, alg)
		p, err := Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", alg, err)
		}
		if p.Identity != "alice" || p.KID != kid || p.Alg != alg || p.JTI == "" {
			t.Errorf("%s: parsed %+v", alg, p)
		}
		if !p.Expiry.Equal(now.Add(MaxLifetime)) {
			t.Errorf("%s: expiry %v", alg, p.Expiry)
		}
		if err := p.Verify(k.Public(), endpoint, now, time.Minute); err != nil {
			t.Errorf("%s: Verify: %v", alg, err)
		}
		// Header carries typ JWT.
		jws, _ := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.EdDSA, jose.ES256})
		if jws.Signatures[0].Header.ExtraHeaders[jose.HeaderType] != "JWT" {
			t.Errorf("%s: typ header missing", alg)
		}
	}
	// jti is fresh each time.
	a, _, _ := sign(t, keys.EdDSA)
	pa, _ := Parse(a)
	k, kid := gen(t, keys.EdDSA)
	r1, _ := Sign(Input{Identity: "a", TokenEndpoint: endpoint, Key: k, KID: kid, Now: now})
	r2, _ := Sign(Input{Identity: "a", TokenEndpoint: endpoint, Key: k, KID: kid, Now: now})
	p1, _ := Parse(r1)
	p2, _ := Parse(r2)
	if p1.JTI == p2.JTI || p1.JTI == pa.JTI {
		t.Error("jti repeated")
	}
}

func TestSignRejectsBadInput(t *testing.T) {
	k, kid := gen(t, keys.EdDSA)
	base := Input{Identity: "a", TokenEndpoint: endpoint, Key: k, KID: kid, Now: now}
	for name, mut := range map[string]func(*Input){
		"identity": func(i *Input) { i.Identity = "" },
		"endpoint": func(i *Input) { i.TokenEndpoint = "" },
		"kid":      func(i *Input) { i.KID = "" },
		"key":      func(i *Input) { i.Key = nil },
		"negative": func(i *Input) { i.Lifetime = -time.Second },
	} {
		in := base
		mut(&in)
		if _, err := Sign(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSignRejectsLongLifetime(t *testing.T) {
	k, kid := gen(t, keys.ES256)
	in := Input{Identity: "a", TokenEndpoint: endpoint, Key: k, KID: kid, Now: now, Lifetime: MaxLifetime + time.Second}
	if _, err := Sign(in); err == nil {
		t.Fatal("lifetime above MaxLifetime accepted")
	}
	in.Lifetime = MaxLifetime
	if _, err := Sign(in); err != nil {
		t.Fatal(err)
	}
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestParseRefusesOtherAlgorithms(t *testing.T) {
	// HS256 with a shared secret.
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
		(&jose.SignerOptions{}).WithHeader("kid", "k"))
	if err != nil {
		t.Fatal(err)
	}
	hs, err := jwt.Signed(s).Claims(goodClaims()).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	none := b64(`{"alg":"none","kid":"k"}`) + "." + b64(`{"iss":"alice","sub":"alice","jti":"j","exp":4102444800}`) + "."
	for name, raw := range map[string]string{"hs256": hs, "none": none, "garbage": "x.y.z", "empty": ""} {
		if _, err := Parse(raw); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestParseRefusesClaimShapes(t *testing.T) {
	k, kid := gen(t, keys.EdDSA)
	mut := func(f func(*jwt.Claims)) jwt.Claims { c := goodClaims(); f(&c); return c }
	cases := map[string]string{
		"iss!=sub": custom(t, k, kid, mut(func(c *jwt.Claims) { c.Subject = "bob" }), nil),
		"no iss":   custom(t, k, kid, mut(func(c *jwt.Claims) { c.Issuer = ""; c.Subject = "" }), nil),
		"no kid":   custom(t, k, "", goodClaims(), nil),
		"no jti":   custom(t, k, kid, mut(func(c *jwt.Claims) { c.ID = "" }), nil),
		"no exp":   custom(t, k, kid, mut(func(c *jwt.Claims) { c.Expiry = nil }), nil),
	}
	for name, raw := range cases {
		if _, err := Parse(raw); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := Parse(custom(t, k, kid, goodClaims(), nil)); err != nil {
		t.Errorf("control: %v", err)
	}
}

func TestVerifyRefuses(t *testing.T) {
	k, kid := gen(t, keys.EdDSA)
	other, _ := gen(t, keys.EdDSA)
	ec, _ := gen(t, keys.ES256)
	mut := func(f func(*jwt.Claims)) string {
		c := goodClaims()
		f(&c)
		return custom(t, k, kid, c, nil)
	}
	cases := []struct {
		name string
		raw  string
		pub  crypto.PublicKey
	}{
		{"wrong key", mut(func(*jwt.Claims) {}), other.Public()},
		{"other algorithm", mut(func(*jwt.Claims) {}), ec.Public()},
		{"other aud", mut(func(c *jwt.Claims) { c.Audience = jwt.Audience{"https://evil/v1/auth/token"} }), k.Public()},
		{"expired", mut(func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(now.Add(-2 * time.Minute)) }), k.Public()},
		{"20 min ahead", mut(func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(now.Add(20 * time.Minute)) }), k.Public()},
		{"nbf future", mut(func(c *jwt.Claims) { c.NotBefore = jwt.NewNumericDate(now.Add(10 * time.Minute)) }), k.Public()},
		{"iat future", mut(func(c *jwt.Claims) { c.IssuedAt = jwt.NewNumericDate(now.Add(10 * time.Minute)) }), k.Public()},
	}
	for _, c := range cases {
		p, err := Parse(c.raw)
		if err != nil {
			t.Fatalf("%s: parse: %v", c.name, err)
		}
		if err := p.Verify(c.pub, endpoint, now, time.Minute); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	// A token whose signature was tampered with.
	raw := mut(func(*jwt.Claims) {})
	parts := strings.Split(raw, ".")
	parts[2] = strings.Repeat("A", len(parts[2]))
	p, err := Parse(strings.Join(parts, "."))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Verify(k.Public(), endpoint, now, time.Minute); !errors.Is(err, ErrInvalid) {
		t.Errorf("tampered: %v", err)
	}
	// Control: the same claims verify.
	p, _ = Parse(mut(func(*jwt.Claims) {}))
	if err := p.Verify(k.Public(), endpoint, now, time.Minute); err != nil {
		t.Errorf("control: %v", err)
	}
	// A zero-value Parsed cannot verify.
	if err := (Parsed{}).Verify(k.Public(), endpoint, now, time.Minute); !errors.Is(err, ErrInvalid) {
		t.Errorf("zero Parsed: %v", err)
	}
}
