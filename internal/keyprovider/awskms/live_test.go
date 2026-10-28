package awskms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"os"
	"testing"
	"time"
)

// TestLive runs against a real AWS KMS key. It is opt-in and never runs in CI:
// set AGENTBUS2_LIVE_AWSKMS_URI to an awskms:// key URI (an ECC_NIST_P256
// SIGN_VERIFY key or alias) and give the default AWS credential chain
// kms:DescribeKey, kms:GetPublicKey and kms:Sign on it.
func TestLive(t *testing.T) {
	uri := os.Getenv("AGENTBUS2_LIVE_AWSKMS_URI")
	if uri == "" {
		t.Skip("AGENTBUS2_LIVE_AWSKMS_URI is not set")
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
