package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

const testPEM = "-----BEGIN PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIA==\n-----END PRIVATE KEY-----\n"

func signingKey(kid string) ab2store.SigningKey {
	return ab2store.SigningKey{KID: kid, Algorithm: "EdDSA", PrivateKeyPEM: testPEM, CreatedAt: fixedNow}
}

func TestSigningKeyPutGet(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.SigningKey(ctx); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("empty store: %v, want ErrNotFound", err)
			}
			return nil
		})
		want := signingKey("k1")
		want.Algorithm = "ES256"
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutSigningKey(ctx, want); err != nil {
				return err
			}
			got, err := tx.SigningKey(ctx) // visible inside the writing Tx
			if err != nil || got != want {
				t.Errorf("in Tx: %+v, %v; want %+v", got, err, want)
			}
			return nil
		})
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.SigningKey(ctx)
			if err != nil || got != want {
				t.Errorf("in Read: %+v, %v; want %+v", got, err, want)
			}
			return nil
		})
		err := s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutSigningKey(ctx, signingKey("k2"))
		})
		if !errors.Is(err, ab2store.ErrReadOnly) {
			t.Errorf("Read put: %v, want ErrReadOnly", err)
		}
	})
}

func TestSigningKeyRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		put := func(k ab2store.SigningKey) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.PutSigningKey(ctx, k) })
		}
		mod := func(f func(*ab2store.SigningKey)) ab2store.SigningKey {
			k := signingKey("k1")
			f(&k)
			return k
		}
		for name, k := range map[string]ab2store.SigningKey{
			"empty KID":   mod(func(k *ab2store.SigningKey) { k.KID = "" }),
			"empty PEM":   mod(func(k *ab2store.SigningKey) { k.PrivateKeyPEM = "" }),
			"RS256":       mod(func(k *ab2store.SigningKey) { k.Algorithm = "RS256" }),
			"no created":  mod(func(k *ab2store.SigningKey) { k.CreatedAt = 0 }),
			"NUL in PEM":  mod(func(k *ab2store.SigningKey) { k.PrivateKeyPEM = "a\x00b" }),
			"empty algo":  mod(func(k *ab2store.SigningKey) { k.Algorithm = "" }),
			"lower eddsa": mod(func(k *ab2store.SigningKey) { k.Algorithm = "eddsa" }),
		} {
			if err := put(k); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
		first := signingKey("first")
		if err := put(first); err != nil {
			t.Fatalf("first put: %v", err)
		}
		if err := put(signingKey("second")); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("second put: %v, want ErrConflict", err)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.SigningKey(ctx)
			if err != nil || got != first {
				t.Errorf("after conflict: %+v, %v; want the first row unchanged", got, err)
			}
			return nil
		})
	})
}

func TestSigningKeyPutConcurrent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		const n = 8
		var wg sync.WaitGroup
		errs := make([]error, n)
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
					return tx.PutSigningKey(ctx, signingKey(fmt.Sprintf("kid-%d", i)))
				})
			}()
		}
		close(start)
		wg.Wait()
		winner := -1
		for i, err := range errs {
			switch {
			case err == nil:
				if winner >= 0 {
					t.Fatalf("two puts succeeded: %d and %d", winner, i)
				}
				winner = i
			case errors.Is(err, ab2store.ErrConflict), errors.Is(err, ab2store.ErrRetryable):
			default:
				t.Errorf("put %d: %v", i, err)
			}
		}
		if winner < 0 {
			t.Fatal("no put succeeded")
		}
		db := rawDB(t, e, rawDSN)
		if got := scalar[int](t, db, "SELECT count(*) FROM signing_keys"); got != 1 {
			t.Fatalf("signing_keys rows = %d, want 1", got)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.SigningKey(ctx)
			if want := fmt.Sprintf("kid-%d", winner); err != nil || got.KID != want {
				t.Errorf("row = %+v, %v; want kid %s", got, err, want)
			}
			return nil
		})
	})
}
