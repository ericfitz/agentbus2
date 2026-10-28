package keyprovider

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

func TestStoreProviderRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := StoreProvider{}
	if p.Name() != NameStore || NameStore != "store" {
		t.Fatalf("name %q", p.Name())
	}
	k, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if k.Algorithm != keys.EdDSA || k.Ref != "" || len(k.PrivatePEM) == 0 {
		t.Fatalf("key %+v", k)
	}
	if kid, _ := keys.KID(k.Public); kid != k.KID {
		t.Fatalf("kid %q is not the thumbprint %q", k.KID, kid)
	}
	sg, err := p.Signer(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("a digest")
	sig, err := sg.SignContext(ctx, msg, crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(sg.Public().(ed25519.PublicKey), msg, sig) {
		t.Fatal("signature does not verify with Public")
	}
	if kid, _ := keys.KID(sg.Public()); kid != k.KID {
		t.Fatal("signer holds another key")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := sg.SignContext(canceled, msg, crypto.Hash(0)); err == nil {
		t.Fatal("a canceled context signed")
	}
	k2, _ := p.Next(ctx)
	if k2.KID == k.KID {
		t.Fatal("Next returned the same key twice")
	}
}

func TestStoreProviderRefusesMismatch(t *testing.T) {
	ctx := context.Background()
	p := StoreProvider{}
	k, _ := p.Next(ctx)
	other, _ := p.Next(ctx)
	es, _ := keys.Generate(keys.ES256)
	esPEM, _ := keys.EncodePrivatePEM(es)
	esKID, _ := keys.KID(es.Public())
	cases := map[string]Key{
		"kid differs":       {KID: other.KID, Algorithm: keys.EdDSA, PrivatePEM: k.PrivatePEM},
		"algorithm differs": {KID: esKID, Algorithm: keys.EdDSA, PrivatePEM: esPEM},
		"garbage PEM":       {KID: k.KID, Algorithm: keys.EdDSA, PrivatePEM: []byte("garbage")},
		"no PEM":            {KID: k.KID, Algorithm: keys.EdDSA},
		"unknown algorithm": {KID: k.KID, Algorithm: "RS256", PrivatePEM: k.PrivatePEM},
	}
	for name, key := range cases {
		if sg, err := p.Signer(ctx, key); err == nil || sg != nil {
			t.Errorf("%s: Signer = %v, %v, want an error", name, sg, err)
		}
	}
}

func TestSchemeOf(t *testing.T) {
	if got := Schemes(); strings.Join(got, ",") != "awskms,gcpkms,azurekms,hashivault" {
		t.Fatalf("Schemes = %v", got)
	}
	for _, s := range Schemes() {
		if got, err := SchemeOf(s + "://some/key"); err != nil || got != s {
			t.Errorf("SchemeOf(%s://) = %q, %v", s, got, err)
		}
	}
	if got, err := SchemeOf(""); err != nil || got != "" {
		t.Errorf("SchemeOf(\"\") = %q, %v", got, err)
	}
	for _, bad := range []string{"file://x", "awskms:/x", "AWSKMS://x", "awskms", "://x", "awskms://", "http://x"} {
		_, err := SchemeOf(bad)
		if err == nil {
			t.Errorf("SchemeOf(%q) accepted", bad)
			continue
		}
		if bad != "awskms://" && !strings.Contains(err.Error(), "awskms, gcpkms, azurekms or hashivault") {
			t.Errorf("SchemeOf(%q) error %q does not name the four", bad, err)
		}
	}
}

func TestExternalKey(t *testing.T) {
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	k, err := ExternalKey(p256.Public(), "awskms://k1")
	if err != nil {
		t.Fatal(err)
	}
	kid, _ := keys.KID(p256.Public())
	if k.KID != kid || k.Algorithm != keys.ES256 || k.Ref != "awskms://k1" || k.PrivatePEM != nil {
		t.Fatalf("key %+v", k)
	}
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	for name, c := range map[string]struct {
		pub  crypto.PublicKey
		want string
	}{
		"ed25519": {edPub, "external signing key is Ed25519; only ECDSA P-256 (ES256) is supported"},
		"p384":    {p384.Public(), "external signing key is ECDSA P-384; only ECDSA P-256 (ES256) is supported"},
		"rsa":     {rsaKey.Public(), "external signing key is RSA; only ECDSA P-256 (ES256) is supported"},
	} {
		if _, err := ExternalKey(c.pub, "awskms://k"); err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if _, err := ExternalKey(p256.Public(), ""); err == nil {
		t.Error("an empty ref was accepted")
	}
}

func TestKeyNameDropsCredentialsAndQuery(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"awskms:///alias/agentbus2", "/alias/agentbus2"},
		{"awskms://alias/agentbus2", "alias/agentbus2"},
		{"hashivault://transit-key?address=https://v:8200&token=s3cret", "transit-key"},
		{"hashivault://user:pw@vault.example/key#frag", "vault.example/key"},
		{"azurekms://v.vault.azure.net/k?x=1", "v.vault.azure.net/k"},
		{"not-a-uri", ""},
		{"", ""},
	} {
		if got := KeyName(tc.in); got != tc.want {
			t.Errorf("KeyName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestErrUnavailableIsASentinel(t *testing.T) {
	if !errors.Is(fmt.Errorf("x: %w", ErrUnavailable), ErrUnavailable) {
		t.Fatal("not matchable")
	}
}
