package hashivault

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"os"
	"testing"
	"time"
)

// TestLive runs against a real Vault (or OpenBao) transit key. It is opt-in and
// never runs in CI: set AGENTBUS2_LIVE_HASHIVAULT_URI to a hashivault://NAME
// URI for an ecdsa-p256 transit key, and VAULT_ADDR and VAULT_TOKEN for a token
// that may read transit/keys/NAME and update transit/sign/NAME/sha2-256.
func TestLive(t *testing.T) {
	uri := os.Getenv("AGENTBUS2_LIVE_HASHIVAULT_URI")
	if uri == "" {
		t.Skip("AGENTBUS2_LIVE_HASHIVAULT_URI is not set")
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
