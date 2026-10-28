package bus

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func idKey(t *testing.T) crypto.PublicKey {
	t.Helper()
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	return k.Public()
}

// coreOver builds another Core over a fresh handle on h's database.
func coreOver(t *testing.T, h *harness) identities.Service {
	t.Helper()
	s, err := h.reopen()
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	c, err := core.New(s, h.cfg, core.Options{Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return identities.Service{Core: c}
}

func TestIdentitiesRegisterConcurrent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		const n = 8
		svcs := make([]identities.Service, n)
		svcs[0] = identities.Service{Core: h.core}
		for i := 1; i < n; i++ {
			svcs[i] = coreOver(t, h)
		}
		run := func(in func(i int) (identities.Mode, identities.RegisterInput)) ([]identities.RegisterResult, []error) {
			res := make([]identities.RegisterResult, n)
			errs := make([]error, n)
			var wg sync.WaitGroup
			for i := range svcs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					m, ri := in(i)
					res[i], errs[i] = svcs[i].Register(context.Background(), m, ri)
				}()
			}
			wg.Wait()
			return res, errs
		}
		keyCount := func(name string) int {
			var c int
			h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				ks, err := tx.PublicKeys(ctx, name)
				c = len(ks)
				return err
			})
			return c
		}

		// distinct keys, one name
		distinct := make([]crypto.PublicKey, n)
		for i := range distinct {
			distinct[i] = idKey(t)
		}
		_, errs := run(func(i int) (identities.Mode, identities.RegisterInput) {
			return identities.ModeOpen, identities.RegisterInput{Identity: "pat", Key: distinct[i]}
		})
		wins := 0
		for _, err := range errs {
			if err == nil {
				wins++
				continue
			}
			codeOf(t, err, ab2bus.CodeConflict)
			var be *ab2bus.Error
			if !errors.As(err, &be) || be.Message != `identity "pat" already has a credential` {
				t.Errorf("loser detail = %v", err)
			}
		}
		if wins != 1 || keyCount("pat") != 1 {
			t.Fatalf("wins = %d, keys = %d", wins, keyCount("pat"))
		}

		// one key, one name
		same := idKey(t)
		res, errs := run(func(int) (identities.Mode, identities.RegisterInput) {
			return identities.ModeOpen, identities.RegisterInput{Identity: "kim", Key: same}
		})
		created := 0
		for i, err := range errs {
			if err != nil {
				t.Fatalf("same-key register %d: %v", i, err)
			}
			if res[i].Created {
				created++
			}
		}
		if created != 1 || keyCount("kim") != 1 {
			t.Fatalf("created = %d, keys = %d", created, keyCount("kim"))
		}

		// a single-use token, distinct free names
		tok, row, err := svcs[0].CreateEnrollmentToken(context.Background(), identities.TokenInput{Uses: 1})
		if err != nil {
			t.Fatal(err)
		}
		_, errs = run(func(i int) (identities.Mode, identities.RegisterInput) {
			return identities.ModeEnrollment, identities.RegisterInput{Identity: fmt.Sprintf("agent%d", i), Key: idKey(t), EnrollmentToken: tok}
		})
		wins = 0
		for _, err := range errs {
			if err == nil {
				wins++
				continue
			}
			codeOf(t, err, ab2bus.CodeForbidden)
		}
		if wins != 1 {
			t.Fatalf("token wins = %d, want 1", wins)
		}
		rows, err := svcs[0].ListEnrollmentTokens(context.Background())
		if err != nil || len(rows) != 1 || rows[0].ID != row.ID || rows[0].UsesLeft != 0 {
			t.Fatalf("token rows = %+v, %v", rows, err)
		}
	})
}

// TestIdentitiesUnboundTokenKeylessAdmin is post-build decision 1 on both
// engines: an unbound token presented for a keyless administrator row is
// refused forbidden, binds no key and is not consumed; a token bound to the
// name claims the row.
func TestIdentitiesUnboundTokenKeylessAdmin(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		ctx := context.Background()
		if err := svc.RegisterLocal(ctx, "root"); err != nil {
			t.Fatal(err)
		}
		if err := svc.MakeAdmin(ctx, "root", true); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ResetCredential(ctx, "root"); err != nil {
			t.Fatal(err)
		}
		usesLeft := func(id string) int {
			rows, err := svc.ListEnrollmentTokens(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				if r.ID == id {
					return r.UsesLeft
				}
			}
			t.Fatalf("token %s not listed", id)
			return 0
		}
		keyCount := func() int {
			var c int
			h.tx(func(ctx context.Context, tx ab2store.Tx) error {
				ks, err := tx.PublicKeys(ctx, "root")
				c = len(ks)
				return err
			})
			return c
		}
		unbound, row, err := svc.CreateEnrollmentToken(ctx, identities.TokenInput{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = svc.Register(ctx, identities.ModeEnrollment, identities.RegisterInput{Identity: "root", Key: idKey(t), EnrollmentToken: unbound})
		codeOf(t, err, ab2bus.CodeForbidden)
		if keyCount() != 0 || usesLeft(row.ID) != 1 {
			t.Fatalf("refused register: keys = %d, UsesLeft = %d", keyCount(), usesLeft(row.ID))
		}
		bound, brow, err := svc.CreateEnrollmentToken(ctx, identities.TokenInput{Identity: "root"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Register(ctx, identities.ModeEnrollment, identities.RegisterInput{Identity: "root", Key: idKey(t), EnrollmentToken: bound}); err != nil {
			t.Fatalf("bound token: %v", err)
		}
		if keyCount() != 1 || usesLeft(brow.ID) != 0 || usesLeft(row.ID) != 1 {
			t.Fatalf("bound register: keys = %d, bound UsesLeft = %d, unbound UsesLeft = %d", keyCount(), usesLeft(brow.ID), usesLeft(row.ID))
		}
	})
}

func TestIdentitiesRegisterThenBus(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		for _, n := range []string{"pat", "kim"} {
			if _, err := svc.Register(context.Background(), identities.ModeOpen, identities.RegisterInput{Identity: n, Key: idKey(t)}); err != nil {
				t.Fatal(err)
			}
		}
		ctx := context.Background()
		pat, kim := newBus(h, "pat"), newBus(h, "kim")
		s, err := pat.Open(ctx, ab2bus.OpenSessionInput{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kim.Send(ctx, ab2bus.SendInput{Channel: "general", Content: "hello"}); err != nil {
			t.Fatalf("Send on general: %v", err)
		}
		r := recv(t, pat, ab2bus.ReceiveInput{Session: s.ID})
		if len(r.Messages) != 1 || r.Messages[0].Content != "hello" || r.Messages[0].Channel != "general" {
			t.Fatalf("received %+v", r.Messages)
		}
	})
}

func TestIdentitiesResetWakesWatch(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, watchOpts(nil))
		b, s := recvOpen(h, "pat")
		h.admin("root")
		w := startWatch(context.Background(), b, ab2bus.WatchInput{Session: s.ID, Timeout: 5 * time.Second})
		time.Sleep(200 * time.Millisecond)
		if err := (identities.Service{Core: h.core}).Reset(context.Background(), "root", "", ab2bus.ResetData); err != nil {
			t.Fatal(err)
		}
		o := awaitWatch(t, w, time.Second)
		if o.err == nil {
			t.Fatalf("the watch ended without an error: %+v", o.res)
		}
		codeOf(t, o.err, ab2bus.CodeNotFound)
	})
}
