package signingkeys

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/store"
)

func logged(t *testing.T, st store.Store, clk *clock, p keyprovider.Provider, buf *bytes.Buffer) Service {
	t.Helper()
	c, err := core.New(st, core.Defaults(), core.Options{Now: clk.now, Logger: slog.New(slog.NewTextHandler(buf, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return Service{Core: c, Provider: p, Settings: DefaultSettings()}
}

func TestKeyringLoadsSet(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	if _, _, err := s.EnsureActive(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		clk.advance(2 * time.Hour)
		if _, err := s.Rotate(ctx, RotateInput{}); err != nil {
			t.Fatal(err)
		}
	}
	rows := rowsOf(t, s)
	set, err := NewKeyring(s).LoadKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Keys) != 3 || set.Active.KID != rows[0].KID || set.Keys[0].KID != rows[0].KID || set.Keys[1].KID != rows[1].KID || set.Keys[2].KID != rows[2].KID {
		t.Fatalf("set %+v, rows %+v", set.Keys, rows)
	}
	if set.Signer == nil {
		t.Fatal("no signer")
	}
	if kid, _ := keys.KID(set.Signer.Public()); kid != rows[0].KID {
		t.Fatal("the signer is not the active key")
	}
	// The issuer takes the set: it mints with the active key and verifies.
	iss, err := token.NewIssuer(ctx, "http://inproc", NewKeyring(s), time.Hour, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := iss.Mint(ctx, token.MintInput{Identity: "pat", Kind: token.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := iss.Verify(ctx, raw, clk.now()); err != nil || id != "pat" {
		t.Fatalf("Verify = %q, %v", id, err)
	}
	if len(iss.JWKS(ctx).Keys) != 3 {
		t.Fatal("JWKS does not list every key")
	}
}

func TestKeyringEmptyAndCorrupt(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSvc(t)
	if _, err := NewKeyring(s).LoadKeys(ctx); err == nil {
		t.Fatal("an empty set loaded")
	}
	good, _ := keyprovider.StoreProvider{}.Next(ctx)
	bad := storeRow(t, good, "garbage")
	put(t, s, bad)
	_, err := NewKeyring(s).LoadKeys(ctx)
	if err == nil || !strings.Contains(err.Error(), "signing key row "+good.KID+" is corrupt") {
		t.Fatalf("err = %v", err)
	}
}

// open note: signing-keys 8 (Review Focus 4).
func TestKeyringProviderMismatch(t *testing.T) {
	ctx := context.Background()
	var buf bytes.Buffer
	clk := &clock{t: time.UnixMilli(startMs)}
	st := openSQLite(t)
	storeSvc := logged(t, st, clk, &fakeProvider{name: keyprovider.NameStore}, &buf)
	old, _, err := storeSvc.EnsureActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Hour)
	extSvc := logged(t, st, clk, &fakeProvider{name: "awskms"}, &buf)
	res, err := extSvc.Rotate(ctx, RotateInput{})
	if err != nil {
		t.Fatal(err)
	}

	// Another replica rotated to awskms; this one is configured for store.
	ring := NewKeyring(storeSvc)
	iss, err := token.NewIssuer(ctx, "http://inproc", ring, time.Hour, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	set, err := ring.LoadKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if set.Signer == nil || set.Active.KID != old.KID || len(set.Keys) != 2 || set.Keys[0].KID != res.Active.KID {
		t.Fatalf("set active %q, keys %d, signer %v", set.Active.KID, len(set.Keys), set.Signer)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "active_provider=awskms") || !strings.Contains(out, "configured_provider=store") {
		t.Fatalf("no warning naming both providers:\n%s", out)
	}
	if n := strings.Count(out, "the active signing key cannot sign here"); n != 1 {
		t.Fatalf("the warning was logged %d times across reloads, want once", n)
	}
	if _, _, err := iss.Mint(ctx, token.MintInput{Identity: "pat", Kind: token.KindAgent}); err != nil {
		t.Fatalf("Mint while the usable key's row exists: %v", err)
	}

	// Once the retiring row is removed no key is usable.
	if err := st.Tx(ctx, func(ctx context.Context, tx store.Tx) error { return tx.DeleteSigningKey(ctx, old.KID) }); err != nil {
		t.Fatal(err)
	}
	set, err = ring.LoadKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if set.Signer != nil || set.Active.KID != res.Active.KID || len(set.Keys) != 1 {
		t.Fatalf("set %+v", set)
	}
	iss.Invalidate()
	_, _, err = iss.Mint(ctx, token.MintInput{Identity: "pat", Kind: token.KindAgent})
	if !errors.Is(err, token.ErrSignerUnavailable) {
		t.Fatalf("Mint = %v, want ErrSignerUnavailable", err)
	}
}

func TestKeyringCachesExternalSigners(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.UnixMilli(startMs)}
	fp := &fakeProvider{name: "awskms"}
	s := svcOver(t, openSQLite(t), clk, fp)
	if _, _, err := s.EnsureActive(ctx); err != nil {
		t.Fatal(err)
	}
	ring := NewKeyring(s)
	for range 3 {
		if _, err := ring.LoadKeys(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if fp.signers != 1 {
		t.Fatalf("Signer built %d times for one key across 3 loads", fp.signers)
	}
	clk.advance(2 * time.Hour)
	if _, err := s.Rotate(ctx, RotateInput{}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := ring.LoadKeys(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if fp.signers != 2 {
		t.Fatalf("Signer built %d times for two keys, want 2", fp.signers)
	}
}
