package token

import (
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/assertion"
	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

const issURL = "https://bus.example"

var t0 = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

var bothAlgs = []keys.Algorithm{keys.EdDSA, keys.ES256}

func genKey(t *testing.T, alg keys.Algorithm) crypto.Signer {
	t.Helper()
	k, err := keys.Generate(alg)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newIssuer(t *testing.T, alg keys.Algorithm, url string, k crypto.Signer) *Issuer {
	t.Helper()
	if k == nil {
		k = genKey(t, alg)
	}
	i, err := NewIssuer(url, k, time.Hour, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// signRaw signs arbitrary claims with the given key and kid header.
func signRaw(t *testing.T, k crypto.Signer, kid string, c jwt.Claims) string {
	t.Helper()
	alg := jose.EdDSA
	if a, _ := keys.AlgorithmOf(k.Public()); a == keys.ES256 {
		alg = jose.ES256
	}
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: k},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(s).Claims(c).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMintThenVerify(t *testing.T) {
	for _, alg := range bothAlgs {
		i := newIssuer(t, alg, issURL, nil)
		raw, exp, err := i.Mint(MintInput{Identity: "alice", Kind: KindAgent})
		if err != nil {
			t.Fatal(err)
		}
		if !exp.Equal(t0.Add(time.Hour)) {
			t.Errorf("%s: expiresAt = %v", alg, exp)
		}
		jws, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.EdDSA, jose.ES256})
		if err != nil {
			t.Fatal(err)
		}
		if got := jws.Signatures[0].Header.KeyID; got != i.KID() {
			t.Errorf("%s: header kid %q, want %q", alg, got, i.KID())
		}
		id, err := i.Verify(raw, t0.Add(time.Minute))
		if err != nil || id != "alice" {
			t.Errorf("%s: Verify = %q, %v", alg, id, err)
		}
		if i.Algorithm() != alg || i.URL() != issURL || i.Lifetime() != time.Hour {
			t.Errorf("%s: accessors", alg)
		}
		if i.TokenEndpoint() != issURL+"/v1/auth/token" || i.JWKSURI() != issURL+"/v1/auth/jwks" {
			t.Errorf("%s: endpoints %q %q", alg, i.TokenEndpoint(), i.JWKSURI())
		}
		if _, _, err := i.Mint(MintInput{Kind: KindAgent}); err == nil {
			t.Errorf("%s: empty identity minted", alg)
		}
	}
}

func TestIssuerVerifyRefusesWrongClaims(t *testing.T) {
	k := genKey(t, keys.EdDSA)
	i := newIssuer(t, keys.EdDSA, issURL, k)
	good := jwt.Claims{
		Issuer: issURL, Subject: "alice", Audience: jwt.Audience{issURL}, ID: "j",
		IssuedAt: jwt.NewNumericDate(t0), Expiry: jwt.NewNumericDate(t0.Add(time.Hour)),
	}
	if _, err := i.Verify(signRaw(t, k, i.KID(), good), t0); err != nil {
		t.Fatalf("control: %v", err)
	}
	// Another key, same URL: unknown kid.
	other := newIssuer(t, keys.EdDSA, issURL, nil)
	raw, _, _ := other.Mint(MintInput{Identity: "alice", Kind: KindAgent})
	if _, err := i.Verify(raw, t0); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("other issuer: %v", err)
	}
	// Right kid header, signature by another key; go-jose's cause stays in the chain.
	if _, err := i.Verify(signRaw(t, genKey(t, keys.EdDSA), i.KID(), good), t0); !errors.Is(err, ErrInvalid) || !errors.Is(err, jose.ErrCryptoFailure) {
		t.Errorf("forged signature: %v", err)
	}
	mut := func(f func(*jwt.Claims)) string { c := good; f(&c); return signRaw(t, k, i.KID(), c) }
	// cause is the go-jose sentinel that must be in the chain next to ErrInvalid.
	for name, c := range map[string]struct {
		raw   string
		cause error
	}{
		"iss":      {mut(func(c *jwt.Claims) { c.Issuer = "https://evil.example" }), jwt.ErrInvalidIssuer},
		"aud":      {mut(func(c *jwt.Claims) { c.Audience = jwt.Audience{i.TokenEndpoint()} }), jwt.ErrInvalidAudience},
		"expired":  {mut(func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(t0.Add(-2 * time.Minute)) }), jwt.ErrExpired},
		"no exp":   {mut(func(c *jwt.Claims) { c.Expiry = nil }), nil},
		"no sub":   {mut(func(c *jwt.Claims) { c.Subject = "" }), nil},
		"nbf":      {mut(func(c *jwt.Claims) { c.NotBefore = jwt.NewNumericDate(t0.Add(time.Hour)) }), jwt.ErrNotValidYet},
		"iat":      {mut(func(c *jwt.Claims) { c.IssuedAt = jwt.NewNumericDate(t0.Add(time.Hour)) }), jwt.ErrIssuedInTheFuture},
		"garbage":  {"not.a.jwt", nil},
		"empty":    {"", nil},
		"one part": {"abc", nil},
	} {
		_, err := i.Verify(c.raw, t0)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
		if c.cause != nil && !errors.Is(err, c.cause) {
			t.Errorf("%s: err = %v, want %v in the chain", name, err, c.cause)
		}
	}
	// An algorithm outside the allowlist is refused with go-jose's error in the chain.
	hs, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("0123456789abcdef0123456789abcdef")},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", i.KID()))
	if err != nil {
		t.Fatal(err)
	}
	hsRaw, err := jwt.Signed(hs).Claims(good).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	var ua *jose.ErrUnexpectedSignatureAlgorithm
	if _, err := i.Verify(hsRaw, t0); !errors.Is(err, ErrInvalid) || !errors.As(err, &ua) {
		t.Errorf("hs256: err = %v, want ErrInvalid and *jose.ErrUnexpectedSignatureAlgorithm", err)
	}
	// Within leeway an expired token is still accepted; past it, refused.
	exp := mut(func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(t0.Add(-30 * time.Second)) })
	if _, err := i.Verify(exp, t0); err != nil {
		t.Errorf("within leeway: %v", err)
	}
	// An assertion presented as a bearer is never accepted.
	cl, kid := genKey(t, keys.EdDSA), ""
	kid, _ = keys.KID(cl.Public())
	asr, err := assertion.Sign(assertion.Input{Identity: "alice", Audience: i.URL(), Key: cl, KID: kid, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := i.Verify(asr, t0); err == nil || id != "" {
		t.Errorf("assertion accepted as bearer: %q, %v", id, err)
	}
	// Even an assertion signed with the issuer's own key and kid is refused: iss is not the URL.
	own := signRaw(t, k, i.KID(), jwt.Claims{
		Issuer: "alice", Subject: "alice", Audience: jwt.Audience{i.TokenEndpoint()}, ID: "j",
		Expiry: jwt.NewNumericDate(t0.Add(time.Minute)),
	})
	if _, err := i.Verify(own, t0); !errors.Is(err, ErrInvalid) {
		t.Errorf("own-key assertion: %v", err)
	}
}

func TestJWKSShape(t *testing.T) {
	for _, alg := range bothAlgs {
		i := newIssuer(t, alg, issURL, nil)
		set := i.JWKS()
		if len(set.Keys) != 1 {
			t.Fatalf("%s: %d keys", alg, len(set.Keys))
		}
		k := set.Keys[0]
		kty := "OKP"
		if alg == keys.ES256 {
			kty = "EC"
		}
		if k.KeyID != i.KID() || k.Algorithm != string(alg) || k.Use != "sig" || !k.IsPublic() {
			t.Errorf("%s: key %+v", alg, k)
		}
		raw, err := k.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"kty":"` + kty + `"`, `"use":"sig"`, `"alg":"` + string(alg) + `"`, `"kid":"` + i.KID() + `"`} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s: json %s lacks %s", alg, raw, want)
			}
		}
		if strings.Contains(string(raw), `"d":`) {
			t.Errorf("%s: private material in JWKS", alg)
		}
	}
}

func TestNewIssuerRejectsURL(t *testing.T) {
	k := genKey(t, keys.EdDSA)
	now := func() time.Time { return t0 }
	for _, u := range []string{
		"", "https://bus.example/", "https://bus.example/path", "ftp://bus.example", "bus.example",
		"https://bus.example?x=1", "https://bus.example#f", "https://", "https://user@bus.example", "https://bus.example?",
	} {
		if _, err := NewIssuer(u, k, time.Hour, now); err == nil {
			t.Errorf("accepted %q", u)
		}
	}
	for _, u := range []string{"http://127.0.0.1:8080", "https://bus.example", "http://inproc"} {
		if _, err := NewIssuer(u, k, time.Hour, now); err != nil {
			t.Errorf("refused %q: %v", u, err)
		}
	}
	if _, err := NewIssuer(issURL, k, 0, now); err == nil {
		t.Error("zero lifetime accepted")
	}
	if _, err := NewIssuer(issURL, nil, time.Hour, now); err == nil {
		t.Error("nil key accepted")
	}
	if _, err := NewIssuer(issURL, k, time.Hour, nil); err == nil {
		t.Error("nil clock accepted")
	}
}

// decodeJWT returns the protected header and the claims of a compact JWS
// without verifying it.
func decodeJWT(t *testing.T, raw string) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("not compact: %q", raw)
	}
	dec := func(s string) map[string]any {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	return dec(parts[0]), dec(parts[1])
}

func TestMintAccessTokenProfile(t *testing.T) {
	i := newIssuer(t, keys.EdDSA, issURL, nil)
	raw, _, err := i.Mint(MintInput{Identity: "alice", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	h, c := decodeJWT(t, raw)
	if h["typ"] != "at+jwt" {
		t.Errorf("typ = %v", h["typ"])
	}
	if c["iss"] != issURL || c["sub"] != "alice" || c["client_id"] != "alice" || c[KindClaim] != "agent" {
		t.Errorf("claims %v", c)
	}
	if aud, _ := c["aud"].([]any); len(aud) != 1 || aud[0] != issURL {
		if s, ok := c["aud"].(string); !ok || s != issURL {
			t.Errorf("aud = %v", c["aud"])
		}
	}
	if jti, _ := c["jti"].(string); jti == "" {
		t.Error("no jti")
	}
	if c["iat"] == nil || c["exp"] == nil {
		t.Errorf("iat/exp missing: %v", c)
	}
	if KindClaim != "agentbus2_kind" {
		t.Errorf("KindClaim = %q", KindClaim)
	}
	if _, _, err := i.Mint(MintInput{Identity: "x", Kind: "robot"}); err == nil {
		t.Error("unknown kind minted")
	}
	if _, _, err := i.Mint(MintInput{Identity: "x", Kind: ""}); err == nil {
		t.Error("empty kind minted")
	}
	if _, _, err := i.Mint(MintInput{Identity: "x", Kind: KindAgent, Lifetime: -time.Second}); err == nil {
		t.Error("negative lifetime minted")
	}
	raw, _, err = i.Mint(MintInput{Identity: "x", Kind: KindHuman, ClientID: "tui-app", Lifetime: 900 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, c = decodeJWT(t, raw)
	if c["client_id"] != "tui-app" || c[KindClaim] != "human" || c["exp"].(float64)-c["iat"].(float64) != 900 {
		t.Errorf("human claims %v", c)
	}
}

func TestVerifyRequiresAccessTokenType(t *testing.T) {
	k := genKey(t, keys.EdDSA)
	i := newIssuer(t, keys.EdDSA, issURL, k)
	claims := jwt.Claims{
		Issuer: issURL, Subject: "alice", Audience: jwt.Audience{issURL}, ID: "j",
		IssuedAt: jwt.NewNumericDate(t0), Expiry: jwt.NewNumericDate(t0.Add(time.Hour)),
	}
	withType := func(typ string) string {
		s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: k},
			(&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("kid", i.KID()))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := jwt.Signed(s).Claims(claims).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, typ := range []string{"JWT", "client-authentication+jwt", "id_token"} {
		if _, err := i.Verify(withType(typ), t0); !errors.Is(err, ErrInvalid) {
			t.Errorf("typ %q: %v", typ, err)
		}
	}
	for _, typ := range []string{"at+jwt", "application/at+jwt", "AT+JWT"} {
		if id, err := i.Verify(withType(typ), t0); err != nil || id != "alice" {
			t.Errorf("typ %q: %q %v", typ, id, err)
		}
	}
}

func TestMetadataURI(t *testing.T) {
	i := newIssuer(t, keys.EdDSA, issURL, nil)
	if got := i.MetadataURI(); got != issURL+"/.well-known/oauth-authorization-server" {
		t.Errorf("MetadataURI = %q", got)
	}
}
