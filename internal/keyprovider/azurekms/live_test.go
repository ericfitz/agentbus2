package azurekms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"os"
	"testing"
	"time"
)

// TestLive runs against a real Key Vault key. It is opt-in and never runs in
// CI: set AGENTBUS2_LIVE_AZUREKMS_URI to an azurekms://VAULT-HOST/KEY URI for
// an EC key on curve P-256 whose key operations include sign, with credentials
// the Azure default chain finds (the key permissions get and sign). It is the
// only test that runs sigstore's Azure signer, which builds its own credential.
func TestLive(t *testing.T) {
	uri := os.Getenv("AGENTBUS2_LIVE_AZUREKMS_URI")
	if uri == "" {
		t.Skip("AGENTBUS2_LIVE_AZUREKMS_URI is not set")
	}
	p, err := New(uri)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	k, err := p.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := p.Signer(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("agentbus2 live test"))
	sig, err := sg.SignContext(ctx, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.VerifyASN1(k.Public.(*ecdsa.PublicKey), digest[:], sig) {
		t.Fatal("signature does not verify")
	}
}
