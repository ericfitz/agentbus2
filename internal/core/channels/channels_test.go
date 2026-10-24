package channels

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

var errBoom = errors.New("boom: store failure")

// flakyStore makes the failOn-th Identity lookup of each transaction fail with
// errBoom (failOn 0 never fails). It counts the lookups it saw in lookups and
// records in fired whether a fault was injected.
type flakyStore struct {
	store.Store
	st *flakyState
}

type flakyState struct {
	failOn  int
	lookups int
	fired   bool
}

func (f flakyStore) Tx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	n := 0
	return f.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return fn(ctx, &flakyTx{Tx: tx, n: &n, st: f.st})
	})
}

type flakyTx struct {
	store.Tx
	n  *int
	st *flakyState
}

func (t *flakyTx) Identity(ctx context.Context, name string) (store.Identity, error) {
	*t.n++
	t.st.lookups++
	if t.st.failOn != 0 && *t.n == t.st.failOn {
		t.st.fired = true
		return store.Identity{}, errBoom
	}
	return t.Tx.Identity(ctx, name)
}

func newService(t *testing.T, st *flakyState) Service {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "agentbus2.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c, err := core.New(flakyStore{Store: s, st: st}, core.Defaults(), core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return Service{c}
}

// A store error on any Identity lookup of a named-scope Create by a
// non-administrator must surface as itself, never as "only an administrator
// may name a scope". The test counts the lookups and fails each in turn, so it
// does not depend on where the admin check sits.
func TestCreateNamedScopeStoreErrorIsNotForbidden(t *testing.T) {
	ctx := context.Background()
	in := bus.CreateChannelInput{Name: "c1", Scope: "job1"}
	count := &flakyState{}
	svc := newService(t, count)
	seed(t, svc)
	count.lookups = 0
	_, _ = svc.Create(ctx, "bob", in)
	total := count.lookups
	if total < 1 {
		t.Fatalf("Create made %d Identity lookups, want at least 1", total)
	}
	for k := 1; k <= total; k++ {
		st := &flakyState{}
		svc := newService(t, st)
		seed(t, svc)
		st.lookups, st.failOn = 0, k
		_, err := svc.Create(ctx, "bob", in)
		if !st.fired {
			t.Fatalf("lookup %d of %d: the fault never fired", k, total)
		}
		if err == nil {
			t.Fatalf("lookup %d of %d: want an error", k, total)
		}
		var be *bus.Error
		if errors.As(err, &be) && be.Code == bus.CodeForbidden {
			t.Fatalf("lookup %d of %d: a store failure was reported as forbidden: %v", k, total, err)
		}
		if strings.Contains(err.Error(), "only an administrator may name a scope") {
			t.Fatalf("lookup %d of %d: admin-check message for a store failure: %v", k, total, err)
		}
	}
}

func TestCreateNamedScopeNonAdminIsForbidden(t *testing.T) {
	ctx := context.Background()
	svc := newService(t, &flakyState{})
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
