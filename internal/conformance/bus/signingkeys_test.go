package bus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
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

// Two administrator rotations with one Idempotency-Key, started together on
// two replicas, rotate once: both return the same result, exactly one is not a
// replay (OnChange runs once), and the set ends with one active and one
// retiring key.
func TestSigningKeysConcurrentIdempotentRotate(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		h.admin("root")
		a, b := keysOver(t, h), keysOver(t, h)
		if _, _, err := a.EnsureActive(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(2 * time.Hour) // past the cooldown
		var changed atomic.Int32
		a.OnChange, b.OnChange = func() { changed.Add(1) }, func() { changed.Add(1) }
		res := make([]ab2bus.SigningKeyRotation, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, s := range []signingkeys.Service{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				res[i], errs[i] = s.RotateSigningKey(context.Background(), "root", ab2bus.RotateSigningKeyInput{IdempotencyKey: "same"})
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("rotation %d: %v", i, err)
			}
		}
		if res[0] != res[1] {
			t.Errorf("results differ:\n%+v\n%+v", res[0], res[1])
		}
		if n := changed.Load(); n != 1 {
			t.Errorf("OnChange ran %d times, want 1 (one non-replay)", n)
		}
		rows := keyRows(h)
		if len(rows) != 2 || countState(rows, ab2store.SigningKeyActive) != 1 || countState(rows, ab2store.SigningKeyRetiring) != 1 || rows[0].KID != res[0].Active.KID {
			t.Fatalf("rows %+v, want one active (%s) and one retiring", rows, res[0].Active.KID)
		}
	})
}

// Both Bus transports of the suite answer the key operations unavailable: the
// local Bus has no signing keys, and the in-process server of the suite is
// built without a key service. The non-administrator check lives in
// internal/server (TestSigningKeysForbiddenForAgent); it needs a transport
// whose server has the service, so no TestACLMatrix row exists.
func TestSigningKeysUnavailable(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		b := h.admin("root")
		ctx := context.Background()
		check := func(name string, err error) {
			t.Helper()
			var be *ab2bus.Error
			if !errors.As(err, &be) || be.Code != ab2bus.CodeUnavailable || be.Retryable || be.Message != ab2bus.SigningKeysNotConfigured {
				t.Errorf("%s: err = %v, want unavailable, not retryable, %q", name, err, ab2bus.SigningKeysNotConfigured)
			}
		}
		_, err := b.ListSigningKeys(ctx)
		check("ListSigningKeys", err)
		_, err = b.RotateSigningKey(ctx, ab2bus.RotateSigningKeyInput{})
		check("RotateSigningKey", err)
		_, err = b.RotateSigningKey(ctx, ab2bus.RotateSigningKeyInput{RevokePrevious: true, IdempotencyKey: "k"})
		check("RotateSigningKey revoking", err)
	})
}
