package kmsfake

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

var _ keyprovider.Provider = (*Fake)(nil)

func TestNameDefaultsAndIsSettable(t *testing.T) {
	if got := New("").Name(); got != "awskms" {
		t.Fatalf("default name %q", got)
	}
	if got := New("gcpkms").Name(); got != "gcpkms" {
		t.Fatalf("name %q", got)
	}
}

func TestNextIsStableUntilRepoint(t *testing.T) {
	ctx := context.Background()
	f := New("awskms")
	a, err := f.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Next(ctx)
	if err != nil || a.KID != b.KID || a.Ref != b.Ref || a.Ref == "" {
		t.Fatalf("second Next = %+v, %v; first %+v", b, err, a)
	}
	if a.PrivatePEM != nil {
		t.Fatal("an external key carries no private PEM")
	}
	c := f.Repoint()
	if c.KID == a.KID || c.Ref == a.Ref {
		t.Fatalf("Repoint returned the same key: %+v", c)
	}
	d, err := f.Next(ctx)
	if err != nil || d.KID != c.KID || d.Ref != c.Ref {
		t.Fatalf("Next after Repoint = %+v, %v; want %+v", d, err, c)
	}
}

func TestSignatureVerifiesAndOldRefStillSigns(t *testing.T) {
	ctx := context.Background()
	f := New("awskms")
	old, _ := f.Next(ctx)
	f.Repoint()
	sg, err := f.Signer(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("hello"))
	sig, err := sg.SignContext(ctx, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := old.Public.(*ecdsa.PublicKey)
	if !ok || !ecdsa.VerifyASN1(pub, digest[:], sig) {
		t.Fatal("signature does not verify under the key it was opened for")
	}
}

func TestSignerRefusesUnknownRef(t *testing.T) {
	f := New("awskms")
	k, _ := f.Next(context.Background())
	k.Ref = "awskms://kmsfake/other"
	if _, err := f.Signer(context.Background(), k); err == nil {
		t.Fatal("Signer accepted a ref the fake does not hold")
	}
}

func TestCallsCountNextAndSignOnly(t *testing.T) {
	ctx := context.Background()
	f := New("awskms")
	if f.Calls() != 0 {
		t.Fatalf("calls %d", f.Calls())
	}
	k, _ := f.Next(ctx)
	sg, err := f.Signer(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if f.Calls() != 1 {
		t.Fatalf("after Next and Signer: %d calls, want 1 (Signer makes none)", f.Calls())
	}
	d := sha256.Sum256([]byte("x"))
	if _, err := sg.SignContext(ctx, d[:], crypto.SHA256); err != nil {
		t.Fatal(err)
	}
	if f.Calls() != 2 {
		t.Fatalf("after a sign: %d calls, want 2", f.Calls())
	}
}

func TestSetDownFailsNextAndSign(t *testing.T) {
	ctx := context.Background()
	f := New("awskms")
	k, _ := f.Next(ctx)
	sg, _ := f.Signer(ctx, k)
	f.SetDown(true)
	if _, err := f.Next(ctx); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("Next while down: %v", err)
	}
	d := sha256.Sum256([]byte("x"))
	if _, err := sg.SignContext(ctx, d[:], crypto.SHA256); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("SignContext while down: %v", err)
	}
	f.SetDown(false)
	if _, err := f.Next(ctx); err != nil {
		t.Fatalf("Next after the service returned: %v", err)
	}
}

func TestCanceledContextFails(t *testing.T) {
	f := New("awskms")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next with a canceled context: %v", err)
	}
}
