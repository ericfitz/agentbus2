package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func identityNames(ids []ab2store.Identity) []string {
	var out []string
	for _, i := range ids {
		out = append(out, i.Name)
	}
	return out
}

func TestIdentityCreateGetList(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustIdentity(t, s, "kim")
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.Identity(ctx, "pat")
			if err != nil {
				t.Fatal(err)
			}
			want := ab2store.Identity{Name: "pat", Admin: false, TagsAckedSeq: 0, CreatedAt: fixedNow}
			if got != want {
				t.Errorf("Identity(pat) = %+v, want %+v", got, want)
			}
			list, err := tx.ListIdentities(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if n := identityNames(list); len(n) != 2 || n[0] != "kim" || n[1] != "pat" {
				t.Errorf("ListIdentities = %v, want [kim pat]", n)
			}
			if _, err := tx.Identity(ctx, "nobody"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("Identity(nobody) err = %v, want ErrNotFound", err)
			}
			return nil
		})
	})
}

func TestIdentityRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		create := func(name string) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				return tx.CreateIdentity(ctx, name, fixedNow)
			})
		}
		for _, name := range []string{"a/b", "", strings.Repeat("x", 129), "tab\there"} {
			if err := create(name); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("CreateIdentity(%q) err = %v, want ErrInvalid", name, err)
			}
		}
		if err := create("pat"); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("duplicate CreateIdentity err = %v, want ErrConflict", err)
		}
		// The failed duplicate must not have damaged the existing row.
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			ids, err := tx.ListIdentities(ctx)
			if err != nil || len(ids) != 1 {
				t.Errorf("ListIdentities = %v, %v; want only pat", ids, err)
			}
			return nil
		})
	})
}

// pending Eric: Gate 0 item 1
func TestIdentityRejectsReservedNames(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		for _, name := range []string{ab2store.Everyone, ab2store.Admins} {
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				return tx.CreateIdentity(ctx, name, fixedNow)
			})
			if !errors.Is(err, ab2store.ErrInvalid) || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "reserved") {
				t.Errorf("CreateIdentity(%q) err = %v, want ErrInvalid naming it reserved", name, err)
			}
		}
	})
}

func TestIdentitySetAdmin(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		admin := func() bool {
			var a bool
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				i, err := tx.Identity(ctx, "pat")
				a = i.Admin
				return err
			})
			return a
		}
		for _, want := range []bool{true, false} {
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.SetAdmin(ctx, "pat", want) })
			if got := admin(); got != want {
				t.Errorf("Admin = %v after SetAdmin(%v)", got, want)
			}
		}
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			return tx.SetAdmin(ctx, "nobody", true)
		})
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("SetAdmin(nobody) err = %v, want ErrNotFound", err)
		}
	})
}

func TestIdentityAdvanceTagAckedMark(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		for _, step := range []struct{ seq, want int64 }{{10, 10}, {5, 10}, {12, 12}} {
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				return tx.AdvanceTagAckedMark(ctx, "pat", step.seq)
			})
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				i, err := tx.Identity(ctx, "pat")
				if err != nil || i.TagsAckedSeq != step.want {
					t.Errorf("after Advance(%d): TagsAckedSeq = %d, %v; want %d", step.seq, i.TagsAckedSeq, err, step.want)
				}
				return nil
			})
		}
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			return tx.AdvanceTagAckedMark(ctx, "nobody", 1)
		})
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("Advance(nobody) err = %v, want ErrNotFound", err)
		}
	})
}

func TestIdentityPublicKeys(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustIdentity(t, s, "kim")
		put := func(k ab2store.PublicKey) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.PutPublicKey(ctx, k) })
		}
		keys := func(name string) []ab2store.PublicKey {
			var out []ab2store.PublicKey
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				out, err = tx.PublicKeys(ctx, name)
				return err
			})
			return out
		}
		k2 := ab2store.PublicKey{Identity: "pat", KID: "b", Algorithm: "ES256", Key: []byte{4, 5, 6}, CreatedAt: fixedNow + 1}
		k1 := ab2store.PublicKey{Identity: "pat", KID: "a", Algorithm: "EdDSA", Key: []byte{1, 2, 3}, CreatedAt: fixedNow}
		for _, k := range []ab2store.PublicKey{k2, k1} {
			if err := put(k); err != nil {
				t.Fatal(err)
			}
		}
		got := keys("pat")
		if len(got) != 2 || got[0].KID != "a" || got[1].KID != "b" {
			t.Fatalf("PublicKeys = %+v, want kids [a b]", got)
		}
		if string(got[0].Key) != string(k1.Key) || got[0].Algorithm != "EdDSA" || got[0].CreatedAt != fixedNow || got[0].Identity != "pat" {
			t.Errorf("key a = %+v, want %+v", got[0], k1)
		}
		// Replace: same (identity, kid), new Key.
		k1.Key = []byte{9, 9}
		if err := put(k1); err != nil {
			t.Fatal(err)
		}
		got = keys("pat")
		if len(got) != 2 || string(got[0].Key) != string([]byte{9, 9}) {
			t.Errorf("after replace: %+v", got)
		}
		// Other identity's keys are separate.
		if n := len(keys("kim")); n != 0 {
			t.Errorf("kim has %d keys, want 0", n)
		}
		bad := ab2store.PublicKey{Identity: "pat", KID: "c", Algorithm: "RS256", Key: []byte{1}, CreatedAt: fixedNow}
		if err := put(bad); !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("RS256 err = %v, want ErrInvalid", err)
		}
		bad.Algorithm = ""
		if err := put(bad); !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("empty algorithm err = %v, want ErrInvalid", err)
		}
		ghost := ab2store.PublicKey{Identity: "ghost", KID: "a", Algorithm: "EdDSA", Key: []byte{1}, CreatedAt: fixedNow}
		if err := put(ghost); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("unknown identity err = %v, want ErrNotFound", err)
		}
		// The failed puts left the keys intact.
		if n := len(keys("pat")); n != 2 {
			t.Errorf("pat has %d keys after rejected puts, want 2", n)
		}
		var n int
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			n, err = tx.DeletePublicKeys(ctx, "pat")
			return err
		})
		if n != 2 || len(keys("pat")) != 0 {
			t.Errorf("DeletePublicKeys = %d, remaining %d; want 2, 0", n, len(keys("pat")))
		}
		// Second run: nothing left.
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			n, err = tx.DeletePublicKeys(ctx, "pat")
			return err
		})
		if n != 0 {
			t.Errorf("second DeletePublicKeys = %d, want 0", n)
		}
	})
}
