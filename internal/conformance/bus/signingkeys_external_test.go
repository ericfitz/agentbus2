package bus

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/token"
	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/signingkeys"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/keyprovider/kmsfake"
)

// extKeysOver is a signing key service over a fresh handle of h's database
// whose provider is f and whose cooldown is off.
func extKeysOver(t *testing.T, h *harness, f *kmsfake.Fake) signingkeys.Service {
	t.Helper()
	s, err := h.reopen()
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	c, err := core.New(s, h.cfg, core.Options{Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return signingkeys.Service{Core: c, Provider: f, Settings: signingkeys.Settings{Interval: 48 * time.Hour}}
}

func extIssuer(t *testing.T, h *harness, svc signingkeys.Service) *token.Issuer {
	t.Helper()
	iss, err := token.NewIssuer(context.Background(), "http://agentbus2.test", signingkeys.NewKeyring(svc), time.Hour, h.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	return iss
}

// Review Focus 3: an active external row is served without any call to the
// key service; a first start with no rows calls Next once.
func TestEnsureActiveMakesNoExternalCall(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		f := kmsfake.New("awskms")
		svc := extKeysOver(t, h, f)
		row, created, err := svc.EnsureActive(context.Background())
		if err != nil || !created {
			t.Fatalf("first start: %v created=%v", err, created)
		}
		if f.Calls() != 1 {
			t.Fatalf("first start made %d calls, want 1 (Next)", f.Calls())
		}
		if row.Provider != "awskms" || row.ProviderRef == "" {
			t.Fatalf("row %+v", row)
		}
		// A restart while the service is down: no call, and it serves.
		f.SetDown(true)
		before := f.Calls()
		svc2 := extKeysOver(t, h, f)
		again, created, err := svc2.EnsureActive(context.Background())
		if err != nil || created || again.KID != row.KID {
			t.Fatalf("restart: %v created=%v kid=%s", err, created, again.KID)
		}
		iss := extIssuer(t, h, svc2)
		if got := iss.JWKS(context.Background()); len(got.Keys) != 1 {
			t.Fatalf("jwks keys %d", len(got.Keys))
		}
		if f.Calls() != before {
			t.Fatalf("restart made %d external calls, want 0", f.Calls()-before)
		}
	})
}

// Review Focus 1: a rotation whose key URI still resolves to the active key.
func TestExternalRotateWithoutRepoint(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		f := kmsfake.New("awskms")
		svc := extKeysOver(t, h, f)
		if _, _, err := svc.EnsureActive(context.Background()); err != nil {
			t.Fatal(err)
		}
		before := keyRows(h)
		_, err := svc.Rotate(context.Background(), signingkeys.RotateInput{})
		var be *ab2bus.Error
		if !errors.As(err, &be) || be.Code != ab2bus.CodeConflict || !strings.Contains(be.Message, "resolves to the active signing key") ||
			!errors.Is(err, signingkeys.ErrRotateNoChange) {
			t.Fatalf("err = %v", err)
		}
		after := keyRows(h)
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("rows changed: %+v -> %+v", before, after)
		}
	})
}

func TestExternalRotateAfterRepoint(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		f := kmsfake.New("awskms")
		svc := extKeysOver(t, h, f)
		first, _, err := svc.EnsureActive(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		next := f.Repoint()
		res, err := svc.Rotate(context.Background(), signingkeys.RotateInput{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Active.KID != next.KID || res.Active.Provider != "awskms" || res.Active.ProviderRef != next.Ref || res.Active.PrivateKeyPEM != "" {
			t.Fatalf("new row %+v, want kid %s ref %s", res.Active, next.KID, next.Ref)
		}
		rows := keyRows(h)
		if len(rows) != 2 || rows[0].KID != next.KID || rows[1].KID != first.KID || rows[1].State != "retiring" || rows[1].RemoveAfter == 0 {
			t.Fatalf("rows %+v", rows)
		}
		// The issuer signs with the new key and still verifies the old key's set.
		iss := extIssuer(t, h, svc)
		raw, _, err := iss.Mint(context.Background(), token.MintInput{Identity: "pat", Kind: token.KindAgent})
		if err != nil {
			t.Fatal(err)
		}
		if id, err := iss.Verify(context.Background(), raw, h.clock.Now()); err != nil || id != "pat" {
			t.Fatalf("verify: %q %v", id, err)
		}
	})
}

// Review Focus 2 and controller ruling 1: with the key service down, minting
// answers ErrSignerUnavailable; verification and JWKS are unaffected; a
// scheduled rotation and a manual one report a retryable unavailable and
// change nothing.
func TestExternalServiceDown(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		f := kmsfake.New("awskms")
		svc := extKeysOver(t, h, f)
		if _, _, err := svc.EnsureActive(ctx); err != nil {
			t.Fatal(err)
		}
		iss := extIssuer(t, h, svc)
		good, _, err := iss.Mint(ctx, token.MintInput{Identity: "pat", Kind: token.KindAgent})
		if err != nil {
			t.Fatal(err)
		}
		before := keyRows(h)

		f.SetDown(true)
		if _, _, err := iss.Mint(ctx, token.MintInput{Identity: "pat", Kind: token.KindAgent}); !errors.Is(err, token.ErrSignerUnavailable) {
			t.Fatalf("Mint while down: %v", err)
		}
		if id, err := iss.Verify(ctx, good, h.clock.Now()); err != nil || id != "pat" {
			t.Fatalf("Verify while down: %q %v", id, err)
		}
		if got := iss.JWKS(ctx); len(got.Keys) != 1 {
			t.Fatalf("JWKS while down: %d keys", len(got.Keys))
		}
		h.clock.Advance(100 * time.Hour) // past the 48 h interval
		unavailable := func(name string, err error) {
			t.Helper()
			var be *ab2bus.Error
			if !errors.As(err, &be) || be.Code != ab2bus.CodeUnavailable || !be.Retryable {
				t.Errorf("%s: err = %v, want retryable unavailable", name, err)
			}
		}
		_, err = svc.RotateDue(ctx, h.clock.Now().UnixMilli())
		unavailable("RotateDue", err)
		_, err = svc.Rotate(ctx, signingkeys.RotateInput{})
		unavailable("Rotate", err)
		after := keyRows(h)
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("rows changed: %+v -> %+v", before, after)
		}

		// The service returns: minting works again with no restart.
		f.SetDown(false)
		if _, _, err := iss.Mint(ctx, token.MintInput{Identity: "pat", Kind: token.KindAgent}); err != nil {
			t.Fatalf("Mint after recovery: %v", err)
		}
	})
}

// Removal deletes the row only: it never calls the key service, and the key
// stays in the service for the operator to retire.
func TestExternalRemovalDeletesOnlyTheRow(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		f := kmsfake.New("awskms")
		svc := extKeysOver(t, h, f)
		if _, _, err := svc.EnsureActive(ctx); err != nil {
			t.Fatal(err)
		}
		f.Repoint()
		if _, err := svc.Rotate(ctx, signingkeys.RotateInput{}); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(time.Duration(signingkeys.RemoveAfter(0))*time.Millisecond + time.Second)
		before := f.Calls()
		n, err := svc.RemoveExpired(ctx, h.clock.Now().UnixMilli())
		if err != nil || n != 1 {
			t.Fatalf("RemoveExpired = %d, %v", n, err)
		}
		if f.Calls() != before {
			t.Fatalf("removal made %d calls to the key service", f.Calls()-before)
		}
		if rows := keyRows(h); len(rows) != 1 || rows[0].State != "active" {
			t.Fatalf("rows %+v", rows)
		}
	})
}
