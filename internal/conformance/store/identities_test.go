package store

import (
	"context"
	"errors"
	"reflect"
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
		// "p\xff" is not UTF-8: SQLite stored it, Postgres failed raw (22021).
		for _, name := range []string{"a/b", "", strings.Repeat("x", 129), "tab\there", "p\xff", "p\x00"} {
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

// Decision (Eric, 2026-10-21): Gate 0 item 1
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
		// Replace: same (identity, kid), new Key, Algorithm and CreatedAt.
		k1.Key = []byte{9, 9}
		k1.Algorithm = "ES256"
		k1.CreatedAt = fixedNow + 50
		if err := put(k1); err != nil {
			t.Fatal(err)
		}
		got = keys("pat")
		if len(got) != 2 || string(got[0].Key) != string([]byte{9, 9}) || got[0].Algorithm != "ES256" || got[0].CreatedAt != fixedNow+50 {
			t.Errorf("after replace: %+v, want key {9 9}, ES256, CreatedAt %d", got, fixedNow+50)
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
		// The key is PKIX DER and the kid names it: neither may be empty. A nil Key
		// used to leak the driver's NOT NULL error; an empty Key and an empty KID
		// used to be stored silently.
		for name, k := range map[string]ab2store.PublicKey{
			"nil key":   {Identity: "pat", KID: "c", Algorithm: "EdDSA", Key: nil, CreatedAt: fixedNow},
			"empty key": {Identity: "pat", KID: "c", Algorithm: "EdDSA", Key: []byte{}, CreatedAt: fixedNow},
			"empty kid": {Identity: "pat", KID: "", Algorithm: "EdDSA", Key: []byte{1}, CreatedAt: fixedNow},
			// The kid is TEXT: SQLite stored invalid UTF-8, Postgres failed raw (22021).
			"non-UTF-8 kid": {Identity: "pat", KID: "c\xff", Algorithm: "EdDSA", Key: []byte{1}, CreatedAt: fixedNow},
			// NUL is valid UTF-8, but Postgres refuses it in TEXT (22021).
			"NUL kid": {Identity: "pat", KID: "a\x00b", Algorithm: "EdDSA", Key: []byte{1}, CreatedAt: fixedNow},
		} {
			if err := put(k); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
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

// byteOrderNames mix case, punctuation and a non-ASCII letter, so SQLite's
// BINARY collation and Postgres's database collation (en_US.utf8) disagree
// about their order; every list "by name" or "by kid" is in byte order on both
// engines (Decision 54).
var (
	byteOrderNames = []string{"kim", "Pat", "\u00c9mile", "zed", "alpha-2", "alpha_1"}
	byteOrderWant  = []string{"Pat", "alpha-2", "alpha_1", "kim", "zed", "\u00c9mile"}
)

func TestIdentityListIsByteOrder(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		for _, n := range byteOrderNames {
			mustIdentity(t, s, n)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			list, err := tx.ListIdentities(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := identityNames(list); !reflect.DeepEqual(got, byteOrderWant) {
				t.Errorf("ListIdentities = %q, want byte order %q", got, byteOrderWant)
			}
			return nil
		})
	})
}

func TestIdentityPublicKeysAreByteOrdered(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		for _, kid := range byteOrderNames {
			k := ab2store.PublicKey{Identity: "pat", KID: kid, Algorithm: "EdDSA", Key: []byte{1}, CreatedAt: fixedNow}
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.PutPublicKey(ctx, k) })
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			keys, err := tx.PublicKeys(ctx, "pat")
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, k := range keys {
				got = append(got, k.KID)
			}
			if !reflect.DeepEqual(got, byteOrderWant) {
				t.Errorf("PublicKeys kids = %q, want byte order %q", got, byteOrderWant)
			}
			return nil
		})
	})
}

// An unknown identity has no keys: PublicKeys lists none and DeletePublicKeys
// removes none, both without error, so reset-credential (Decision 73) is
// idempotent. Writes that need the identity to exist (SetAdmin,
// AdvanceTagAckedMark, PutPublicKey) return ErrNotFound instead.
func TestIdentityKeysOfUnknownIdentity(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			keys, err := tx.PublicKeys(ctx, "ghost")
			if err != nil || len(keys) != 0 {
				t.Errorf("PublicKeys(ghost) = %v, %v; want none, nil", keys, err)
			}
			return nil
		})
		var n int
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			n, err = tx.DeletePublicKeys(ctx, "ghost")
			return err
		})
		if n != 0 {
			t.Errorf("DeletePublicKeys(ghost) = %d, want 0", n)
		}
	})
}

// Every identity write fails with ErrReadOnly inside Store.Read, with valid
// input so the guard (not validation) is what rejects it, and leaves no trace.
func TestIdentityRejectsWritesInRead(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutPublicKey(ctx, ab2store.PublicKey{Identity: "pat", KID: "a", Algorithm: "EdDSA", Key: []byte{1}, CreatedAt: fixedNow})
		})
		for name, write := range map[string]func(context.Context, ab2store.Tx) error{
			"CreateIdentity": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.CreateIdentity(ctx, "kim", fixedNow)
			},
			"SetAdmin": func(ctx context.Context, tx ab2store.Tx) error { return tx.SetAdmin(ctx, "pat", true) },
			"AdvanceTagAckedMark": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.AdvanceTagAckedMark(ctx, "pat", 9)
			},
			"PutPublicKey": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.PutPublicKey(ctx, ab2store.PublicKey{Identity: "pat", KID: "b", Algorithm: "ES256", Key: []byte{2}, CreatedAt: fixedNow})
			},
			"DeletePublicKeys": func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.DeletePublicKeys(ctx, "pat")
				return err
			},
		} {
			err := s.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error { return write(ctx, tx) })
			if !errors.Is(err, ab2store.ErrReadOnly) {
				t.Errorf("%s inside Read: err = %v, want ErrReadOnly", name, err)
			}
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			ids, err := tx.ListIdentities(ctx)
			if err != nil || len(ids) != 1 || ids[0] != (ab2store.Identity{Name: "pat", CreatedAt: fixedNow}) {
				t.Errorf("identities after rejected writes = %+v, %v; want only the untouched pat", ids, err)
			}
			keys, err := tx.PublicKeys(ctx, "pat")
			if err != nil || len(keys) != 1 || keys[0].KID != "a" {
				t.Errorf("keys after rejected writes = %+v, %v; want only kid a", keys, err)
			}
			return nil
		})
	})
}

// A name that is not valid UTF-8 or holds a NUL cannot be stored
// (CreateIdentity refuses it), so lookups by such a name answer as for any
// unknown identity on both engines; Postgres used to fail each with a raw
// SQLSTATE 22021 while SQLite answered not found. Each call runs in its own
// transaction because an error ends the transaction.
func TestIdentityLookupsOfNonUTF8Name(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, _ := openStore(t, e, sqlstore.Options{})
		// "p\x00" is valid UTF-8, but Postgres refuses NUL in TEXT the same way.
		for _, bad := range []string{"p\xff", "p\x00"} {
			if err := s.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.Identity(ctx, bad)
				return err
			}); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("Identity(%q) err = %v, want ErrNotFound", bad, err)
			}
			if err := s.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error { return tx.SetAdmin(ctx, bad, true) }); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("SetAdmin(%q) err = %v, want ErrNotFound", bad, err)
			}
			if err := s.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error { return tx.AdvanceTagAckedMark(ctx, bad, 5) }); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("AdvanceTagAckedMark(%q) err = %v, want ErrNotFound", bad, err)
			}
			var keys []ab2store.PublicKey
			if err := s.Read(ctx, func(ctx context.Context, tx ab2store.Tx) (err error) {
				keys, err = tx.PublicKeys(ctx, bad)
				return err
			}); err != nil || len(keys) != 0 {
				t.Errorf("PublicKeys(%q) = %v, %v; want none, nil", bad, keys, err)
			}
			var n int
			if err := s.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) (err error) {
				n, err = tx.DeletePublicKeys(ctx, bad)
				return err
			}); err != nil || n != 0 {
				t.Errorf("DeletePublicKeys(%q) = %d, %v; want 0, nil", bad, n, err)
			}
			// PutPublicKey for such an identity: the identity cannot exist.
			k := ab2store.PublicKey{Identity: bad, KID: "a", Algorithm: "EdDSA", Key: []byte{1}, CreatedAt: fixedNow}
			if err := s.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error { return tx.PutPublicKey(ctx, k) }); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("PutPublicKey(identity %q) err = %v, want ErrNotFound", bad, err)
			}
			// Validation of the key still comes before the identity's existence.
			k.Algorithm = "RS256"
			if err := s.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error { return tx.PutPublicKey(ctx, k) }); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("PutPublicKey(identity %q, RS256) err = %v, want ErrInvalid", bad, err)
			}
		}
	})
}
