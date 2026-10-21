package keys

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"
)

var both = []Algorithm{EdDSA, ES256}

func mustGen(t *testing.T, alg Algorithm) crypto.Signer {
	t.Helper()
	k, err := Generate(alg)
	if err != nil {
		t.Fatalf("Generate(%s): %v", alg, err)
	}
	return k
}

func TestGenerateAndAlgorithmOf(t *testing.T) {
	for _, alg := range both {
		k := mustGen(t, alg)
		got, err := AlgorithmOf(k.Public())
		if err != nil || got != alg {
			t.Errorf("AlgorithmOf(%s key) = %q, %v", alg, got, err)
		}
		if _, err := AlgorithmOf(k); err == nil {
			t.Errorf("AlgorithmOf(%s private key) must fail", alg)
		}
	}
	if _, err := Generate("RS256"); err == nil {
		t.Error("Generate of an unknown algorithm must fail")
	}
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AlgorithmOf(&rk.PublicKey); err == nil || !strings.Contains(err.Error(), "unsupported key type") {
		t.Errorf("RSA: %v", err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AlgorithmOf(&p384.PublicKey); err == nil || !strings.Contains(err.Error(), "unsupported key type") {
		t.Errorf("P-384: %v", err)
	}
}

func TestKIDIsThumbprint(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	pub := ed25519.NewKeyFromSeed(seed).Public()
	j := jose.JSONWebKey{Key: pub}
	sum, err := j.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	want := base64.RawURLEncoding.EncodeToString(sum)
	got, err := KID(pub)
	if err != nil || got != want {
		t.Fatalf("KID = %q, %v; want %q", got, err, want)
	}
	if strings.Contains(got, "=") {
		t.Errorf("KID has padding: %q", got)
	}
	// RFC 8037 appendix A.3 vector: the thumbprint of this OKP key.
	rfcPub, _ := base64.RawURLEncoding.DecodeString("11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo")
	if k, _ := KID(ed25519.PublicKey(rfcPub)); k != "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k" {
		t.Errorf("RFC 8037 vector: %q", k)
	}
}

func TestPrivatePEMRoundTrip(t *testing.T) {
	for _, alg := range both {
		k := mustGen(t, alg)
		b, err := EncodePrivatePEM(k)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(b), "-----BEGIN PRIVATE KEY-----") {
			t.Errorf("%s: not a PRIVATE KEY block", alg)
		}
		got, galg, err := ParsePrivatePEM(b)
		if err != nil || galg != alg {
			t.Fatalf("%s: ParsePrivatePEM = %v, %q", alg, err, galg)
		}
		a, _ := KID(got.Public())
		b2, _ := KID(k.Public())
		if a != b2 {
			t.Errorf("%s: round trip changed the key", alg)
		}
		// Whitespace after the block is fine.
		if _, _, err := ParsePrivatePEM(append(append([]byte{}, b...), '\n', '\n')); err != nil {
			t.Errorf("%s: trailing newline refused: %v", alg, err)
		}
		// Two blocks and trailing junk are refused.
		if _, _, err := ParsePrivatePEM(append(append([]byte{}, b...), b...)); err == nil {
			t.Errorf("%s: two blocks accepted", alg)
		}
		if _, _, err := ParsePrivatePEM(append(append([]byte{}, b...), []byte("junk")...)); err == nil {
			t.Errorf("%s: trailing bytes accepted", alg)
		}
	}
	ec := mustGen(t, ES256).(*ecdsa.PrivateKey)
	der, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	sec1 := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if _, _, err := ParsePrivatePEM(sec1); err == nil {
		t.Error("SEC 1 EC PRIVATE KEY accepted")
	}
	if _, _, err := ParsePrivatePEM([]byte("not pem")); err == nil {
		t.Error("non-PEM accepted")
	}
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	rder, _ := x509.MarshalPKCS8PrivateKey(rk)
	if _, _, err := ParsePrivatePEM(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rder})); err == nil {
		t.Error("RSA PKCS#8 accepted")
	}
}

func TestPublicDERRoundTrip(t *testing.T) {
	for _, alg := range both {
		k := mustGen(t, alg)
		der, err := MarshalPublic(k.Public())
		if err != nil {
			t.Fatal(err)
		}
		pub, galg, err := ParsePublic(der)
		if err != nil || galg != alg {
			t.Fatalf("%s: %v %q", alg, err, galg)
		}
		a, _ := KID(pub)
		b, _ := KID(k.Public())
		if a != b {
			t.Errorf("%s: key changed", alg)
		}
	}
	if _, _, err := ParsePublic([]byte("garbage")); err == nil {
		t.Error("garbage DER accepted")
	}
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	rder, _ := x509.MarshalPKIXPublicKey(&rk.PublicKey)
	if _, _, err := ParsePublic(rder); err == nil {
		t.Error("RSA DER accepted")
	}
}

func TestJWKRoundTrip(t *testing.T) {
	for _, alg := range both {
		k := mustGen(t, alg)
		kid, _ := KID(k.Public())
		j, err := ToJWK(k.Public(), kid)
		if err != nil {
			t.Fatal(err)
		}
		if j.Algorithm != string(alg) || j.Use != "sig" || j.KeyID != kid {
			t.Errorf("%s: JWK = %+v", alg, j)
		}
		raw, err := json.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		var back jose.JSONWebKey
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		pub, galg, gkid, err := FromJWK(back)
		if err != nil || galg != alg || gkid != kid {
			t.Fatalf("%s: FromJWK = %v %q %q", alg, err, galg, gkid)
		}
		if k2, _ := KID(pub); k2 != kid {
			t.Errorf("%s: key changed", alg)
		}
	}
}

func TestFromJWKRejects(t *testing.T) {
	ed := mustGen(t, EdDSA)
	ec := mustGen(t, ES256)
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	cases := []struct {
		name string
		j    jose.JSONWebKey
		want string
	}{
		{"private", jose.JSONWebKey{Key: ed}, "private key material"},
		{"rsa", jose.JSONWebKey{Key: &rk.PublicKey}, "unsupported key type"},
		{"p384", jose.JSONWebKey{Key: &p384.PublicKey}, "unsupported key type"},
		{"kid", jose.JSONWebKey{Key: ec.Public(), KeyID: "other"}, "kid does not match"},
		{"alg", jose.JSONWebKey{Key: ec.Public(), Algorithm: "EdDSA"}, "alg"},
		{"nokey", jose.JSONWebKey{}, "JWK"},
	}
	for _, c := range cases {
		_, _, _, err := FromJWK(c.j)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}
