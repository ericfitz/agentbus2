package token

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

// fakeSource is a counting KeySource whose set a test replaces.
type fakeSource struct {
	mu    sync.Mutex
	set   KeySet
	err   error
	loads int
}

func (f *fakeSource) LoadKeys(context.Context) (KeySet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.err != nil {
		return KeySet{}, f.err
	}
	return f.set, nil
}

func (f *fakeSource) put(set KeySet, err error) {
	f.mu.Lock()
	f.set, f.err = set, err
	f.mu.Unlock()
}

func (f *fakeSource) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loads
}

// ksKey makes a generated key and its Key record.
func ksKey(t *testing.T, alg keys.Algorithm) (crypto.Signer, Key) {
	t.Helper()
	s := genKey(t, alg)
	k, err := keyOf(s.Public())
	if err != nil {
		t.Fatal(err)
	}
	return s, k
}

// setOf is a valid set: active signs, the others are retiring; Keys is
// active first.
func setOf(active crypto.Signer, activeKey Key, retiring ...Key) KeySet {
	return KeySet{Active: activeKey, Signer: cryptoSigner{active}, Keys: append([]Key{activeKey}, retiring...)}
}

func ksIssuer(t *testing.T, src KeySource, clk *clock) *Issuer {
	t.Helper()
	i, err := NewIssuer(context.Background(), issURL, src, time.Hour, clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func goodClaims(clk *clock) jwt.Claims {
	return jwt.Claims{
		Issuer: issURL, Subject: "alice", Audience: jwt.Audience{issURL}, ID: "j",
		IssuedAt: jwt.NewNumericDate(clk.Now()), Expiry: jwt.NewNumericDate(clk.Now().Add(time.Hour)),
	}
}

func headerKID(t *testing.T, raw string) string {
	t.Helper()
	tok, err := jwt.ParseSigned(raw, serverAlgorithms)
	if err != nil {
		t.Fatal(err)
	}
	return tok.Headers[0].KeyID
}

func TestIssuerVerifiesEveryKey(t *testing.T) {
	clk := &clock{t: t0}
	s1, k1 := ksKey(t, keys.EdDSA)
	s2, k2 := ksKey(t, keys.ES256)
	src := &fakeSource{}
	src.put(setOf(s1, k1), nil)
	i := ksIssuer(t, src, clk)
	old, _, err := i.Mint(context.Background(), MintInput{Identity: "alice", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	src.put(setOf(s2, k2, k1), nil)
	i.Invalidate()
	ctx := context.Background()
	if id, err := i.Verify(ctx, old, clk.Now()); err != nil || id != "alice" {
		t.Fatalf("bearer of the retiring key: %q %v", id, err)
	}
	fresh, _, err := i.Mint(ctx, MintInput{Identity: "bob", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if headerKID(t, fresh) != k2.KID {
		t.Errorf("minted with kid %q, want the active %q", headerKID(t, fresh), k2.KID)
	}
	if id, err := i.Verify(ctx, fresh, clk.Now()); err != nil || id != "bob" {
		t.Fatalf("bearer of the active key: %q %v", id, err)
	}
	set := i.JWKS(ctx)
	if len(set.Keys) != 2 || set.Keys[0].KeyID != k2.KID || set.Keys[1].KeyID != k1.KID {
		t.Errorf("jwks kids %v, want [%s %s]", kidsOf(set), k2.KID, k1.KID)
	}
	if i.KID() != k2.KID || i.Algorithm() != keys.ES256 {
		t.Errorf("KID %q Algorithm %q", i.KID(), i.Algorithm())
	}
}

func kidsOf(set jose.JSONWebKeySet) []string {
	var out []string
	for _, k := range set.Keys {
		out = append(out, k.KeyID)
	}
	return out
}

func TestIssuerReloadsOnUnknownKID(t *testing.T) {
	clk := &clock{t: t0}
	s1, k1 := ksKey(t, keys.EdDSA)
	s3, k3 := ksKey(t, keys.EdDSA)
	src := &fakeSource{}
	src.put(setOf(s1, k1), nil)
	i := ksIssuer(t, src, clk)
	src.put(setOf(s1, k1, k3), nil)
	raw := signRaw(t, s3, k3.KID, goodClaims(clk))
	before := src.count()
	if _, err := i.Verify(context.Background(), raw, clk.Now()); err != nil {
		t.Fatalf("bearer of the new key: %v", err)
	}
	if got := src.count() - before; got != 1 {
		t.Errorf("loads for one unknown kid = %d, want 1", got)
	}
}

func TestIssuerUnknownKIDRateLimited(t *testing.T) {
	clk := &clock{t: t0}
	s1, k1 := ksKey(t, keys.EdDSA)
	src := &fakeSource{}
	src.put(setOf(s1, k1), nil)
	i := ksIssuer(t, src, clk)
	other, _ := ksKey(t, keys.EdDSA)
	ctx := context.Background()
	before := src.count()
	for n := 0; n < 100; n++ {
		raw := signRaw(t, other, fmt.Sprintf("random-%d", n), goodClaims(clk))
		if _, err := i.Verify(ctx, raw, clk.Now()); !errors.Is(err, ErrUnknownKey) {
			t.Fatalf("bearer %d: %v", n, err)
		}
	}
	if got := src.count() - before; got != 1 {
		t.Errorf("100 unknown kids within 5 s caused %d loads, want 1", got)
	}
	clk.Add(UnknownKeyReloadInterval)
	_, _ = i.Verify(ctx, signRaw(t, other, "later", goodClaims(clk)), clk.Now())
	if got := src.count() - before; got != 2 {
		t.Errorf("after 5 s: %d loads, want 2", got)
	}

	// 50 concurrent verifies of one unknown kid: one load, however they race.
	clk.Add(UnknownKeyReloadInterval)
	before = src.count()
	raw := signRaw(t, other, "concurrent", goodClaims(clk))
	var wg sync.WaitGroup
	for n := 0; n < 50; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := i.Verify(ctx, raw, clk.Now()); !errors.Is(err, ErrUnknownKey) {
				t.Errorf("concurrent: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := src.count() - before; got != 1 {
		t.Errorf("50 concurrent verifies caused %d loads, want 1", got)
	}
}

func TestIssuerPeriodicReload(t *testing.T) {
	for _, op := range []string{"mint", "verify", "jwks"} {
		t.Run(op, func(t *testing.T) {
			clk := &clock{t: t0}
			s1, k1 := ksKey(t, keys.EdDSA)
			s2, k2 := ksKey(t, keys.EdDSA)
			src := &fakeSource{}
			src.put(setOf(s1, k1), nil)
			i := ksIssuer(t, src, clk)
			ctx := context.Background()
			old, _, _ := i.Mint(ctx, MintInput{Identity: "alice", Kind: KindAgent})
			src.put(setOf(s2, k2), nil) // k1 is removed
			clk.Add(ReloadInterval - time.Second)
			_, _, _ = i.Mint(ctx, MintInput{Identity: "alice", Kind: KindAgent})
			if src.count() != 1 {
				t.Fatalf("reloaded before the interval: %d loads", src.count())
			}
			clk.Add(time.Second)
			switch op {
			case "mint":
				raw, _, err := i.Mint(ctx, MintInput{Identity: "alice", Kind: KindAgent})
				if err != nil || headerKID(t, raw) != k2.KID {
					t.Errorf("mint after reload: kid %q, %v; want %q", headerKID(t, raw), err, k2.KID)
				}
			case "verify":
				_, _ = i.Verify(ctx, old, clk.Now())
			case "jwks":
				if got := kidsOf(i.JWKS(ctx)); len(got) != 1 || got[0] != k2.KID {
					t.Errorf("jwks %v", got)
				}
			}
			want := 2
			if op == "verify" {
				want = 3 // the periodic reload, then the forced one for the removed key's kid
			}
			if src.count() != want {
				t.Errorf("loads = %d, want %d", src.count(), want)
			}
			if _, err := i.Verify(ctx, old, clk.Now()); !errors.Is(err, ErrUnknownKey) {
				t.Errorf("removed key's bearer: %v", err)
			}
		})
	}
}

func TestIssuerReloadFailureKeepsSet(t *testing.T) {
	clk := &clock{t: t0}
	s1, k1 := ksKey(t, keys.EdDSA)
	src := &fakeSource{}
	src.put(setOf(s1, k1), nil)
	i := ksIssuer(t, src, clk)
	ctx := context.Background()
	raw, _, _ := i.Mint(ctx, MintInput{Identity: "alice", Kind: KindAgent})
	src.put(KeySet{}, errors.New("store down"))
	clk.Add(ReloadInterval)
	if id, err := i.Verify(ctx, raw, clk.Now()); err != nil || id != "alice" {
		t.Fatalf("after a failed reload: %q %v", id, err)
	}
	if _, _, err := i.Mint(ctx, MintInput{Identity: "alice", Kind: KindAgent}); err != nil {
		t.Errorf("mint after a failed reload: %v", err)
	}
	if src.count() != 2 {
		t.Errorf("loads = %d, want 2 (a failed reload waits a full interval)", src.count())
	}
	if _, err := NewIssuer(ctx, issURL, src, time.Hour, clk.Now); err == nil {
		t.Error("NewIssuer over a failing source succeeded")
	}
}

func TestIssuerInvalidate(t *testing.T) {
	clk := &clock{t: t0}
	s1, k1 := ksKey(t, keys.EdDSA)
	s2, k2 := ksKey(t, keys.EdDSA)
	src := &fakeSource{}
	src.put(setOf(s1, k1), nil)
	i := ksIssuer(t, src, clk)
	src.put(setOf(s2, k2), nil)
	ctx := context.Background()
	_ = i.JWKS(ctx)
	if src.count() != 1 {
		t.Fatalf("loads before Invalidate = %d", src.count())
	}
	i.Invalidate()
	if got := kidsOf(i.JWKS(ctx)); len(got) != 1 || got[0] != k2.KID {
		t.Errorf("jwks after Invalidate %v", got)
	}
	if src.count() != 2 {
		t.Errorf("loads = %d, want 2", src.count())
	}
	i.JWKS(ctx)
	if src.count() != 2 {
		t.Errorf("a second access reloaded again: %d", src.count())
	}
}

// TestIssuerRejectsBadKeySet: an invalid set is refused at NewIssuer and, on a
// later reload, leaves the cached set in place.
func TestIssuerRejectsBadKeySet(t *testing.T) {
	s1, k1 := ksKey(t, keys.EdDSA)
	_, k2 := ksKey(t, keys.EdDSA)
	s3, k3 := ksKey(t, keys.ES256)
	wrongAlg := k1
	wrongAlg.Algorithm = keys.ES256
	badKID := k1
	badKID.KID = "not-the-thumbprint"
	cases := map[string]KeySet{
		"empty":                  {},
		"active not in keys":     {Active: k1, Signer: cryptoSigner{s1}, Keys: []Key{k2}},
		"kid not the thumbprint": {Active: badKID, Signer: cryptoSigner{s1}, Keys: []Key{badKID}},
		"algorithm disagrees":    {Active: wrongAlg, Signer: cryptoSigner{s1}, Keys: []Key{wrongAlg}},
		"signer is another key":  {Active: k1, Signer: cryptoSigner{s3}, Keys: []Key{k1, k3}},
		"duplicate kid":          {Active: k1, Signer: cryptoSigner{s1}, Keys: []Key{k1, k1}},
		"listed key is bad":      {Active: k1, Signer: cryptoSigner{s1}, Keys: []Key{k1, badKID}},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			src := &fakeSource{}
			src.put(set, nil)
			if _, err := NewIssuer(context.Background(), issURL, src, time.Hour, (&clock{t: t0}).Now); err == nil {
				t.Fatal("NewIssuer accepted the set")
			}
			clk := &clock{t: t0}
			good := &fakeSource{}
			good.put(setOf(s1, k1), nil)
			i := ksIssuer(t, good, clk)
			good.put(set, nil)
			clk.Add(ReloadInterval)
			if got := kidsOf(i.JWKS(context.Background())); len(got) != 1 || got[0] != k1.KID {
				t.Errorf("a bad reload replaced the cached set: %v", got)
			}
		})
	}
}

type recordingSigner struct {
	pub      crypto.PublicKey
	err      error
	deadline time.Time
	hasDL    bool
	block    bool
}

func (r *recordingSigner) Public() crypto.PublicKey { return r.pub }

func (r *recordingSigner) SignContext(ctx context.Context, _ []byte, _ crypto.SignerOpts) ([]byte, error) {
	r.deadline, r.hasDL = ctx.Deadline()
	if r.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, r.err
}

func TestMintSignerFailureIs503(t *testing.T) {
	clk := &clock{t: t0}
	s1, k1 := ksKey(t, keys.EdDSA)
	in := MintInput{Identity: "alice", Kind: KindAgent}
	for name, signer := range map[string]ContextSigner{
		"no signer":   nil,
		"signer errs": &recordingSigner{pub: s1.Public(), err: errors.New("kms down")},
	} {
		t.Run(name, func(t *testing.T) {
			src := &fakeSource{}
			src.put(KeySet{Active: k1, Signer: signer, Keys: []Key{k1}}, nil)
			i := ksIssuer(t, src, clk)
			_, _, err := i.Mint(context.Background(), in)
			if !errors.Is(err, ErrSignerUnavailable) {
				t.Fatalf("Mint = %v, want ErrSignerUnavailable", err)
			}
			if got := kidsOf(i.JWKS(context.Background())); len(got) != 1 {
				t.Errorf("JWKS with no usable signer: %v", got)
			}
		})
	}
}

// open note: signing-keys 7
func TestMintSignTimeout(t *testing.T) {
	clk := &clock{t: t0}
	s1, k1 := ksKey(t, keys.EdDSA)
	rs := &recordingSigner{pub: s1.Public(), err: errors.New("kms down")}
	src := &fakeSource{}
	src.put(KeySet{Active: k1, Signer: rs, Keys: []Key{k1}}, nil)
	i := ksIssuer(t, src, clk)
	start := time.Now()
	_, _, err := i.Mint(context.Background(), MintInput{Identity: "alice", Kind: KindAgent})
	if !errors.Is(err, ErrSignerUnavailable) {
		t.Fatal(err)
	}
	if !rs.hasDL {
		t.Fatal("the signer saw no deadline")
	}
	if end := time.Now(); rs.deadline.After(end.Add(SignTimeout)) || rs.deadline.Before(start.Add(SignTimeout)) {
		t.Errorf("deadline %v, want within [%v, %v]", rs.deadline, start.Add(SignTimeout), end.Add(SignTimeout))
	}

	// A signer that blocks until its context ends returns once the caller's does.
	rs = &recordingSigner{pub: s1.Public(), block: true}
	src.put(KeySet{Active: k1, Signer: rs, Keys: []Key{k1}}, nil)
	i.Invalidate()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := i.Mint(ctx, MintInput{Identity: "alice", Kind: KindAgent}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrSignerUnavailable) {
			t.Errorf("blocked signer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Mint did not return after its context ended")
	}
}

// TestMintES256DER: a context signer that returns DER for ES256 (the
// crypto.Signer contract) yields a JWS the public key verifies.
func TestMintES256DER(t *testing.T) {
	clk := &clock{t: t0}
	s, _ := ksKey(t, keys.ES256)
	i := ksIssuer(t, StaticKeys(s), clk)
	raw, _, err := i.Mint(context.Background(), MintInput{Identity: "alice", Kind: KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	var c jwt.Claims
	if err := tok.Claims(s.Public(), &c); err != nil || c.Subject != "alice" {
		t.Fatalf("claims: %+v %v", c, err)
	}
}
