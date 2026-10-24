package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

const humanEric = "eric@corp.example"

func mustHuman(t *testing.T, s ab2store.Store, email string) {
	t.Helper()
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
		return tx.CreateHuman(ctx, ab2store.HumanBinding{Identity: email, ClaimType: ab2store.ClaimEmail, Value: email}, fixedNow)
	})
}

func TestHumanCreateAndLookups(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.CreateHuman(ctx, ab2store.HumanBinding{Identity: humanEric, ClaimType: ab2store.ClaimEmail, Value: humanEric}, 1000)
		})
		mustIdentity(t, s, "pat")
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			id, err := tx.Identity(ctx, humanEric)
			if err != nil {
				t.Fatal(err)
			}
			if id.Kind != ab2store.IdentityHuman || id.Admin || id.CreatedAt != 1000 {
				t.Errorf("Identity = %+v, want human, not admin, created 1000", id)
			}
			want := ab2store.HumanBinding{Identity: humanEric, ClaimType: ab2store.ClaimEmail, Value: humanEric, CreatedAt: 1000}
			if got, err := tx.HumanBinding(ctx, humanEric); err != nil || got != want {
				t.Errorf("HumanBinding = %+v, %v; want %+v", got, err, want)
			}
			if got, err := tx.HumanBindingByValue(ctx, ab2store.ClaimEmail, humanEric); err != nil || got != want {
				t.Errorf("HumanBindingByValue = %+v, %v; want %+v", got, err, want)
			}
			if _, err := tx.HumanBinding(ctx, "pat"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("HumanBinding(agent) err = %v, want ErrNotFound", err)
			}
			if _, err := tx.HumanBinding(ctx, "a\x00b"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("HumanBinding(NUL) err = %v, want ErrNotFound", err)
			}
			if _, err := tx.HumanBindingByValue(ctx, ab2store.ClaimEmail, "a\x00b"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("HumanBindingByValue(NUL) err = %v, want ErrNotFound", err)
			}
			if _, err := tx.HumanBindingBySubject(ctx, "https://idp", "a\x00b"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("HumanBindingBySubject(NUL) err = %v, want ErrNotFound", err)
			}
			pat, err := tx.Identity(ctx, "pat")
			if err != nil || pat.Kind != ab2store.IdentityAgent {
				t.Errorf("Identity(pat) = %+v, %v; want agent", pat, err)
			}
			list, err := tx.ListIdentities(ctx)
			if err != nil || len(list) != 2 || list[0].Kind != ab2store.IdentityHuman || list[1].Kind != ab2store.IdentityAgent {
				t.Errorf("ListIdentities = %+v, %v; want [human agent]", list, err)
			}
			return nil
		})
	})
}

func TestHumanRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustHuman(t, s, humanEric)
		mustIdentity(t, s, "pat")
		create := func(b ab2store.HumanBinding) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				return tx.CreateHuman(ctx, b, fixedNow)
			})
		}
		ok := func(id, v string) ab2store.HumanBinding {
			return ab2store.HumanBinding{Identity: id, ClaimType: ab2store.ClaimEmail, Value: v}
		}
		invalid := map[string]ab2store.HumanBinding{
			"claim type user_id": {Identity: "kim@corp.example", ClaimType: "user_id", Value: "kim@corp.example"},
			"empty claim type":   {Identity: "kim@corp.example", Value: "kim@corp.example"},
			"empty value":        ok("kim@corp.example", ""),
			"NUL value":          ok("kim@corp.example", "k\x00m"),
			"everyone":           ok("everyone", "x@corp.example"),
			"admins":             ok("admins", "y@corp.example"),
			"slash name":         ok("a/b", "z@corp.example"),
			"subject no issuer":  {Identity: "kim@corp.example", ClaimType: ab2store.ClaimEmail, Value: "kim@corp.example", ProviderSubject: "g1"},
			"issuer no subject":  {Identity: "kim@corp.example", ClaimType: ab2store.ClaimEmail, Value: "kim@corp.example", Issuer: "https://idp"},
			"NUL subject":        {Identity: "kim@corp.example", ClaimType: ab2store.ClaimEmail, Value: "kim@corp.example", Issuer: "https://idp", ProviderSubject: "g\x00"},
		}
		for name, b := range invalid {
			if err := create(b); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
		conflicts := map[string]ab2store.HumanBinding{
			"same name twice": ok(humanEric, "other@corp.example"),
			"same value":      ok("kim@corp.example", humanEric),
			"over agent name": ok("pat", "pat@corp.example"),
		}
		for name, b := range conflicts {
			if err := create(b); !errors.Is(err, ab2store.ErrConflict) {
				t.Errorf("%s: err = %v, want ErrConflict", name, err)
			}
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			ids, err := tx.ListIdentities(ctx)
			if err != nil || len(ids) != 2 {
				t.Errorf("ListIdentities = %+v, %v; want only eric and pat", ids, err)
			}
			if _, err := tx.HumanBinding(ctx, "pat"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("agent pat gained a binding: %v", err)
			}
			return nil
		})
		// A subject bound at creation is unique too.
		first := ab2store.HumanBinding{Identity: "kim@corp.example", ClaimType: ab2store.ClaimEmail, Value: "kim@corp.example", Issuer: "https://idp", ProviderSubject: "g1"}
		if err := create(first); err != nil {
			t.Fatal(err)
		}
		dup := ab2store.HumanBinding{Identity: "lee@corp.example", ClaimType: ab2store.ClaimEmail, Value: "lee@corp.example", Issuer: "https://idp", ProviderSubject: "g1"}
		if err := create(dup); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("duplicate subject err = %v, want ErrConflict", err)
		}
	})
}

func TestHumanReadOnly(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustHuman(t, s, humanEric)
		mustIdentity(t, s, "pat")
		for name, f := range map[string]func(context.Context, ab2store.Tx) error{
			"CreateHuman": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.CreateHuman(ctx, ab2store.HumanBinding{Identity: "k@c.example", ClaimType: ab2store.ClaimEmail, Value: "k@c.example"}, 1)
			},
			"MarkHuman":       func(ctx context.Context, tx ab2store.Tx) error { return tx.MarkHuman(ctx, "pat") },
			"SetHumanSubject": func(ctx context.Context, tx ab2store.Tx) error { return tx.SetHumanSubject(ctx, humanEric, "i", "s") },
			"AddReference": func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.AddReference(ctx, ab2store.Reference{List: ab2store.ListUsers, Kind: ab2store.RefIdentity, Value: "pat"})
				return err
			},
			"RemoveReference": func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.RemoveReference(ctx, ab2store.Reference{List: ab2store.ListUsers, Kind: ab2store.RefIdentity, Value: "pat"})
				return err
			},
		} {
			if err := s.Read(context.Background(), f); !errors.Is(err, ab2store.ErrReadOnly) {
				t.Errorf("%s in Read err = %v, want ErrReadOnly", name, err)
			}
		}
	})
}

func TestHumanSubjectBinding(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustHuman(t, s, humanEric)
		mustHuman(t, s, "kim@corp.example")
		set := func(id, iss, sub string) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				return tx.SetHumanSubject(ctx, id, iss, sub)
			})
		}
		bySubject := func() (ab2store.HumanBinding, error) {
			var b ab2store.HumanBinding
			var err error
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				b, err = tx.HumanBindingBySubject(ctx, "https://idp", "guid-1")
				return nil
			})
			return b, err
		}
		if err := set(humanEric, "https://idp", "guid-1"); err != nil {
			t.Fatal(err)
		}
		if b, err := bySubject(); err != nil || b.Identity != humanEric || b.Issuer != "https://idp" || b.ProviderSubject != "guid-1" {
			t.Errorf("HumanBindingBySubject = %+v, %v; want eric", b, err)
		}
		if err := set("kim@corp.example", "https://idp", "guid-1"); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("same subject on second human err = %v, want ErrConflict", err)
		}
		if err := set(humanEric, "https://idp", ""); !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("issuer alone err = %v, want ErrInvalid", err)
		}
		if err := set(humanEric, "", "guid-1"); !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("subject alone err = %v, want ErrInvalid", err)
		}
		if err := set(humanEric, "https://idp", "g\x00"); !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("NUL subject err = %v, want ErrInvalid", err)
		}
		if err := set("nobody@corp.example", "https://idp", "guid-9"); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("unknown identity err = %v, want ErrNotFound", err)
		}
		if err := set("pat", "https://idp", "guid-9"); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("agent without binding err = %v, want ErrNotFound", err)
		}
		// Re-setting the same pair on the same identity is not a conflict.
		if err := set(humanEric, "https://idp", "guid-1"); err != nil {
			t.Errorf("same pair again: %v", err)
		}
		if err := set(humanEric, "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := bySubject(); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("after clear err = %v, want ErrNotFound", err)
		}
		// Two bindings with no subject do not collide on the partial index.
		if err := set("kim@corp.example", "", ""); err != nil {
			t.Errorf("clear on unbound human: %v", err)
		}
		// Cleared, the subject is free for the other human.
		if err := set("kim@corp.example", "https://idp", "guid-1"); err != nil {
			t.Errorf("subject after clear: %v", err)
		}
	})
}

func TestMarkHuman(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustIdentity(t, s, "keyed")
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutPublicKey(ctx, ab2store.PublicKey{Identity: "keyed", KID: "k", Algorithm: "EdDSA", Key: []byte{1}, CreatedAt: fixedNow})
		})
		mark := func(name string) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.MarkHuman(ctx, name) })
		}
		if err := mark("pat"); err != nil {
			t.Fatal(err)
		}
		if err := mark("pat"); err != nil {
			t.Errorf("second MarkHuman: %v", err)
		}
		if err := mark("keyed"); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("MarkHuman(keyed) err = %v, want ErrConflict", err)
		}
		if err := mark("nobody"); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("MarkHuman(nobody) err = %v, want ErrNotFound", err)
		}
		if err := mark("a\x00b"); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("MarkHuman(NUL) err = %v, want ErrNotFound", err)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for name, want := range map[string]ab2store.IdentityKind{"pat": ab2store.IdentityHuman, "keyed": ab2store.IdentityAgent} {
				if id, err := tx.Identity(ctx, name); err != nil || id.Kind != want {
					t.Errorf("Identity(%s) = %+v, %v; want kind %s", name, id, err, want)
				}
			}
			return nil
		})
	})
}

func TestValidIdentityKind(t *testing.T) {
	for k, want := range map[ab2store.IdentityKind]bool{ab2store.IdentityAgent: true, ab2store.IdentityHuman: true, "": false, "robot": false, "Agent": false} {
		if got := ab2store.ValidIdentityKind(k); got != want {
			t.Errorf("ValidIdentityKind(%q) = %v, want %v", k, got, want)
		}
	}
}
