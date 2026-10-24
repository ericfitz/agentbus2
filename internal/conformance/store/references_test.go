package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func addRef(s ab2store.Store, r ab2store.Reference) (bool, error) {
	var did bool
	err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) (err error) {
		did, err = tx.AddReference(ctx, r)
		return err
	})
	return did, err
}

func removeRef(s ab2store.Store, r ab2store.Reference) (bool, error) {
	var did bool
	err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) (err error) {
		did, err = tx.RemoveReference(ctx, r)
		return err
	})
	return did, err
}

func listRefs(t *testing.T, s ab2store.Store, l ab2store.RefList) []ab2store.Reference {
	t.Helper()
	var out []ab2store.Reference
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		out, err = tx.ListReferences(ctx, l)
		return err
	})
	return out
}

func TestReferencesAddRemoveList(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		grp := ab2store.Reference{List: ab2store.ListUsers, Kind: ab2store.RefGroup, Issuer: "corp", Value: "pai-users", CreatedAt: 5}
		if did, err := addRef(s, grp); err != nil || !did {
			t.Fatalf("add = %v, %v; want true", did, err)
		}
		if did, err := addRef(s, grp); err != nil || did {
			t.Fatalf("second add = %v, %v; want false", did, err)
		}
		if did, err := addRef(s, ab2store.Reference{List: ab2store.ListAdmins, Kind: ab2store.RefIdentity, Value: "pat", CreatedAt: 6}); err != nil || !did {
			t.Fatalf("add admins identity = %v, %v", did, err)
		}
		got := listRefs(t, s, ab2store.ListUsers)
		if len(got) != 1 || got[0] != grp {
			t.Errorf("users = %+v, want [%+v]", got, grp)
		}
		if got := listRefs(t, s, ab2store.ListAdmins); len(got) != 1 || got[0].Kind != ab2store.RefIdentity {
			t.Errorf("admins = %+v, want one identity ref", got)
		}
		if did, err := removeRef(s, grp); err != nil || !did {
			t.Errorf("remove = %v, %v; want true", did, err)
		}
		if did, err := removeRef(s, grp); err != nil || did {
			t.Errorf("second remove = %v, %v; want false", did, err)
		}
		if got := listRefs(t, s, ab2store.ListUsers); len(got) != 0 {
			t.Errorf("users after remove = %+v, want none", got)
		}
	})
}

func TestReferencesOrder(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		in := []ab2store.Reference{
			{List: ab2store.ListUsers, Kind: ab2store.RefSubject, Issuer: "b", Value: "z"},
			{List: ab2store.ListUsers, Kind: ab2store.RefGroup, Issuer: "b", Value: "B"},
			{List: ab2store.ListUsers, Kind: ab2store.RefGroup, Issuer: "a", Value: "z"},
			{List: ab2store.ListUsers, Kind: ab2store.RefGroup, Issuer: "b", Value: "a"},
			{List: ab2store.ListUsers, Kind: ab2store.RefEmail, Issuer: "c", Value: "x@y"},
			{List: ab2store.ListUsers, Kind: ab2store.RefIdentity, Value: "pat"},
		}
		for _, r := range in {
			if _, err := addRef(s, r); err != nil {
				t.Fatalf("add %+v: %v", r, err)
			}
		}
		// Byte order by kind, issuer, value; "B" sorts before "a".
		want := []ab2store.Reference{in[4], in[2], in[1], in[3], in[5], in[0]}
		got := listRefs(t, s, ab2store.ListUsers)
		if len(got) != len(want) {
			t.Fatalf("users = %+v", got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("users[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
	})
}

func TestReferencesReject(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		bad := map[string]ab2store.Reference{
			"list owners":          {List: "owners", Kind: ab2store.RefEmail, Issuer: "i", Value: "x"},
			"empty list":           {Kind: ab2store.RefEmail, Issuer: "i", Value: "x"},
			"kind role":            {List: ab2store.ListUsers, Kind: "role", Issuer: "i", Value: "x"},
			"identity with issuer": {List: ab2store.ListUsers, Kind: ab2store.RefIdentity, Issuer: "corp", Value: "x"},
			"email no issuer":      {List: ab2store.ListUsers, Kind: ab2store.RefEmail, Value: "x"},
			"group no issuer":      {List: ab2store.ListUsers, Kind: ab2store.RefGroup, Value: "x"},
			"subject no issuer":    {List: ab2store.ListUsers, Kind: ab2store.RefSubject, Value: "x"},
			"empty value":          {List: ab2store.ListUsers, Kind: ab2store.RefGroup, Issuer: "corp"},
			"NUL value":            {List: ab2store.ListUsers, Kind: ab2store.RefGroup, Issuer: "corp", Value: "a\x00b"},
			"NUL issuer":           {List: ab2store.ListUsers, Kind: ab2store.RefGroup, Issuer: "c\x00", Value: "x"},
		}
		for name, r := range bad {
			if _, err := addRef(s, r); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("add %s: err = %v, want ErrInvalid", name, err)
			}
			if _, err := removeRef(s, r); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("remove %s: err = %v, want ErrInvalid", name, err)
			}
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.ListReferences(ctx, "owners"); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("ListReferences(owners) err = %v, want ErrInvalid", err)
			}
			return nil
		})
		if n := len(listRefs(t, s, ab2store.ListUsers)) + len(listRefs(t, s, ab2store.ListAdmins)); n != 0 {
			t.Errorf("%d rows stored from rejected input", n)
		}
	})
}
