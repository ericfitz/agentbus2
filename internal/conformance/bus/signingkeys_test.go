package bus

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/signingkeys"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// keysOver is a signing key service over a fresh handle of h's database.
func keysOver(t *testing.T, h *harness) signingkeys.Service {
	t.Helper()
	s, err := h.reopen()
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	c, err := core.New(s, h.cfg, core.Options{Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return signingkeys.Service{Core: c, Provider: keyprovider.StoreProvider{}, Settings: signingkeys.DefaultSettings()}
}

func keyRows(h *harness) []ab2store.SigningKey {
	h.t.Helper()
	var rows []ab2store.SigningKey
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		rows, err = tx.ListSigningKeys(ctx)
		return err
	})
	return rows
}

func countState(rows []ab2store.SigningKey, state string) int {
	n := 0
	for _, r := range rows {
		if r.State == state {
			n++
		}
	}
	return n
}

func TestSigningKeysConcurrentEnsure(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		const n = 8
		svcs := make([]signingkeys.Service, n)
		for i := range svcs {
			svcs[i] = keysOver(t, h)
		}
		type out struct {
			row     ab2store.SigningKey
			created bool
			err     error
		}
		outs := make([]out, n)
		var wg sync.WaitGroup
		for i := range svcs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				row, created, err := svcs[i].EnsureActive(context.Background())
				outs[i] = out{row, created, err}
			}()
		}
		wg.Wait()
		created := 0
		for i, o := range outs {
			if o.err != nil {
				t.Fatalf("call %d: %v", i, o.err)
			}
			if o.row.KID != outs[0].row.KID {
				t.Fatalf("call %d returned another key", i)
			}
			if o.created {
				created++
			}
		}
		if created != 1 {
			t.Fatalf("created = %d, want 1", created)
		}
		rows := keyRows(h)
		if len(rows) != 1 || rows[0].KID != outs[0].row.KID || rows[0].State != ab2store.SigningKeyActive {
			t.Fatalf("rows %+v, returned %s", rows, outs[0].row.KID)
		}
	})
}

func TestSigningKeysConcurrentRotate(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		const n = 8
		svcs := make([]signingkeys.Service, n)
		for i := range svcs {
			svcs[i] = keysOver(t, h)
		}
		if _, _, err := svcs[0].EnsureActive(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(2 * time.Hour)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range svcs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = svcs[i].Rotate(context.Background(), signingkeys.RotateInput{})
			}()
		}
		wg.Wait()
		ok := 0
		for i, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, signingkeys.ErrCooldown):
			default:
				t.Errorf("rotation %d: %v", i, err)
			}
		}
		if ok != 1 {
			t.Fatalf("%d rotations succeeded, want exactly 1", ok)
		}
		rows := keyRows(h)
		if len(rows) != 2 || countState(rows, ab2store.SigningKeyActive) != 1 || countState(rows, ab2store.SigningKeyRetiring) != 1 {
			t.Fatalf("rows %+v, want one active and one retiring", rows)
		}
	})
}

func TestSigningKeysConcurrentRevoke(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		a, b := keysOver(t, h), keysOver(t, h)
		if _, _, err := a.EnsureActive(context.Background()); err != nil {
			t.Fatal(err)
		}
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, s := range []signingkeys.Service{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Inside the cooldown: a revoking rotation is exempt.
				_, errs[i] = s.Rotate(context.Background(), signingkeys.RotateInput{RevokePrevious: true})
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("revoking rotation %d: %v (both succeed by design)", i, err)
			}
		}
		rows := keyRows(h)
		if len(rows) != 1 || rows[0].State != ab2store.SigningKeyActive {
			t.Fatalf("rows %+v, want one active row", rows)
		}
	})
}
