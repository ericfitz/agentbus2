package gcpkms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"os"
	"testing"
	"time"
)

// TestLive runs against a real Cloud KMS key. It is opt-in and never runs in
// CI: set AGENTBUS2_LIVE_GCPKMS_URI to a gcpkms:// URI for an EC_SIGN_P256_SHA256
// key (purpose ASYMMETRIC_SIGN), with Application Default Credentials that may
// view the key and sign (cloudkms.cryptoKeyVersions.list, .get,
// .viewPublicKey and .useToSign).
func TestLive(t *testing.T) {
	uri := os.Getenv("AGENTBUS2_LIVE_GCPKMS_URI")
	if uri == "" {
		t.Skip("AGENTBUS2_LIVE_GCPKMS_URI is not set")
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
