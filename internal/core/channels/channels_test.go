package channels

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

var errBoom = errors.New("boom: store failure")

// flakyStore makes the nth Identity lookup of each transaction fail with
// errBoom (failOn 0 never fails).
type flakyStore struct {
	store.Store
	failOn *int
}

func (f flakyStore) Tx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	n := 0
	return f.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return fn(ctx, &flakyTx{Tx: tx, n: &n, failOn: *f.failOn})
	})
}

type flakyTx struct {
	store.Tx
	n      *int
	failOn int
}

func (t *flakyTx) Identity(ctx context.Context, name string) (store.Identity, error) {
	*t.n++
	if t.failOn != 0 && *t.n == t.failOn {
		return store.Identity{}, errBoom
	}
	return t.Tx.Identity(ctx, name)
}

func newService(t *testing.T, failOn *int) Service {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "agentbus2.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c, err := core.New(flakyStore{Store: s, failOn: failOn}, core.Defaults(), core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return Service{c}
}

// A store error while checking the administrator flag for a named scope must
// surface as itself, not as "only an administrator may name a scope".
func TestCreateNamedScopeAdminCheckStoreErrorIsNotForbidden(t *testing.T) {
	ctx := context.Background()
	failOn := 0
	svc := newService(t, &failOn)
	seed(t, svc)
	failOn = 3 // Identity lookups in Create: 1 = mutate, 2 = creator, 3 = the admin check.
	_, err := svc.Create(ctx, "bob", bus.CreateChannelInput{Name: "c1", Scope: "job1"})
	var be *bus.Error
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.As(err, &be) && be.Code == bus.CodeForbidden {
		t.Fatalf("a store failure was reported as forbidden: %v", err)
	}
}

func TestCreateNamedScopeNonAdminIsForbidden(t *testing.T) {
	ctx := context.Background()
	failOn := 0
	svc := newService(t, &failOn)
	seed(t, svc)
	_, err := svc.Create(ctx, "bob", bus.CreateChannelInput{Name: "c1", Scope: "job1"})
	var be *bus.Error
	if !errors.As(err, &be) || be.Code != bus.CodeForbidden {
		t.Fatalf("err = %v, want forbidden", err)
	}
}

func seed(t *testing.T, svc Service) {
	t.Helper()
	if err := svc.Store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.CreateIdentity(ctx, "bob", 1)
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}
