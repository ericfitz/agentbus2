package identities

import (
	"context"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

func TestNormalizeEmail(t *testing.T) {
	got, err := NormalizeEmail(" Eric@Corp.Example ")
	if err != nil || got != "eric@corp.example" {
		t.Fatalf("NormalizeEmail = %q, %v", got, err)
	}
	for _, bad := range []string{"everyone", "admins", "no-at", "@corp", "eric@", "a/b@c", "a\nb@c", strings.Repeat("a", 127) + "@c", "", "a@b@c"} {
		_, err := NormalizeEmail(bad)
		if err == nil {
			t.Errorf("NormalizeEmail(%q) accepted", bad)
			continue
		}
		wantCode(t, err, bus.CodeValidation)
	}
}

func TestParseReferenceRejects(t *testing.T) {
	for name, a := range map[string][6]string{
		"unknown list":       {"owners", "corp", "a@b", "", "", ""},
		"two kinds":          {"users", "corp", "a@b", "g", "", ""},
		"none":               {"users", "corp", "", "", "", ""},
		"identity w/ issuer": {"admins", "corp", "", "", "", "pat"},
		"group no issuer":    {"users", "", "", "g", "", ""},
		"bad email":          {"users", "corp", "nope", "", "", ""},
	} {
		_, err := ParseReference(a[0], a[1], a[2], a[3], a[4], a[5])
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		wantCode(t, err, bus.CodeValidation)
	}
	r, err := ParseReference("users", "corp", " A@B.c ", "", "", "")
	if err != nil || r.Value != "a@b.c" || r.Kind != store.RefEmail || r.Issuer != "corp" {
		t.Fatalf("email = %+v, %v", r, err)
	}
}

func TestMatchReferences(t *testing.T) {
	c := HumanClaims{IssuerName: "corp", Email: "a@b.c", Groups: []string{"g1", "g2"}, Subject: "s1"}
	ref := func(k store.RefKind, issuer, v string) []store.Reference {
		return []store.Reference{{List: store.ListUsers, Kind: k, Issuer: issuer, Value: v}}
	}
	for name, tc := range map[string]struct {
		refs []store.Reference
		want bool
	}{
		"identity":           {ref(store.RefIdentity, "", "pat"), true},
		"identity other":     {ref(store.RefIdentity, "", "sam"), false},
		"email":              {ref(store.RefEmail, "corp", "a@b.c"), true},
		"email other issuer": {ref(store.RefEmail, "other", "a@b.c"), false},
		"email other value":  {ref(store.RefEmail, "corp", "x@b.c"), false},
		"group":              {ref(store.RefGroup, "corp", "g2"), true},
		"group missing":      {ref(store.RefGroup, "corp", "g3"), false},
		"group other issuer": {ref(store.RefGroup, "other", "g2"), false},
		"subject":            {ref(store.RefSubject, "corp", "s1"), true},
		"subject other":      {ref(store.RefSubject, "other", "s1"), false},
		"empty":              {nil, false},
	} {
		if got := MatchReferences(tc.refs, "pat", c); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func adminRef(name string) store.Reference {
	return store.Reference{List: store.ListAdmins, Kind: store.RefIdentity, Value: name}
}

func listRefs(t *testing.T, s Service, l store.RefList) []store.Reference {
	t.Helper()
	refs, err := s.ListReferences(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

func TestReferencesAddIdentityWritesFlag(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	if err := s.RegisterLocal(ctx, "pat"); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		added, err := s.AddReference(ctx, adminRef("pat"), nil)
		if err != nil || added != want {
			t.Fatalf("add %d = %v, %v", i, added, err)
		}
		if id, _ := identityRow(t, s, "pat"); !id.Admin {
			t.Fatal("flag not set")
		}
	}
	if refs := listRefs(t, s, store.ListAdmins); len(refs) != 1 || refs[0].CreatedAt <= 0 {
		t.Fatalf("refs = %+v, want CreatedAt > 0", refs)
	}
	for i, want := range []bool{true, false} {
		removed, err := s.RemoveReference(ctx, adminRef("pat"))
		if err != nil || removed != want {
			t.Fatalf("remove %d = %v, %v", i, removed, err)
		}
		if id, _ := identityRow(t, s, "pat"); id.Admin {
			t.Fatal("flag not cleared")
		}
	}
	if len(listRefs(t, s, store.ListAdmins)) != 0 {
		t.Fatal("row left")
	}
}

func TestReferencesRefuseUnknownIdentity(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	_, err := s.AddReference(ctx, adminRef("ghost"), nil)
	wantCode(t, err, bus.CodeNotRegistered)
	if len(listRefs(t, s, store.ListAdmins)) != 0 {
		t.Fatal("row written")
	}
}

func TestReferencesRefuseUnknownIssuer(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	r := store.Reference{List: store.ListUsers, Kind: store.RefGroup, Issuer: "nope", Value: "pai"}
	_, err := s.AddReference(ctx, r, []string{"corp"})
	wantCode(t, err, bus.CodeValidation)
	wantDetail(t, err, "unknown issuer \"nope\"; configured: corp")
	r.Issuer = "corp"
	if added, err := s.AddReference(ctx, r, []string{"corp"}); err != nil || !added {
		t.Fatalf("add = %v, %v", added, err)
	}
	// Removal needs no configured issuer.
	r.Issuer = "gone"
	r.Value = "x"
	if _, err := s.RemoveReference(ctx, r); err != nil {
		t.Fatal(err)
	}
}

func TestReferencesRejectInvalid(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	for _, r := range []store.Reference{
		{List: "owners", Kind: store.RefIdentity, Value: "pat"},
		{List: store.ListUsers, Kind: "phone", Issuer: "corp", Value: "x"},
		{List: store.ListUsers, Kind: store.RefGroup, Issuer: "corp"},
		{List: store.ListUsers, Kind: store.RefIdentity, Issuer: "corp", Value: "pat"},
		{List: store.ListUsers, Kind: store.RefEmail, Issuer: "corp", Value: "A@B.C"},
		{List: store.ListUsers, Kind: store.RefEmail, Issuer: "corp", Value: "nope"},
	} {
		_, err := s.AddReference(ctx, r, []string{"corp"})
		wantCode(t, err, bus.CodeValidation)
		_, err = s.RemoveReference(ctx, r)
		wantCode(t, err, bus.CodeValidation)
	}
	_, err := s.ListReferences(ctx, "owners")
	wantCode(t, err, bus.CodeValidation)
}

func TestMakeAdminUsesAdminsList(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	if err := s.RegisterLocal(ctx, "pat"); err != nil {
		t.Fatal(err)
	}
	if err := s.MakeAdmin(ctx, "pat", true); err != nil {
		t.Fatal(err)
	}
	refs := listRefs(t, s, store.ListAdmins)
	if len(refs) != 1 || refs[0].Value != "pat" || refs[0].Kind != store.RefIdentity {
		t.Fatalf("refs = %+v", refs)
	}
	if refs[0].CreatedAt <= 0 {
		t.Fatalf("CreatedAt = %d", refs[0].CreatedAt)
	}
	if id, _ := identityRow(t, s, "pat"); !id.Admin {
		t.Fatal("flag not set")
	}
	if err := s.MakeAdmin(ctx, "pat", false); err != nil {
		t.Fatal(err)
	}
	if len(listRefs(t, s, store.ListAdmins)) != 0 {
		t.Fatal("row left")
	}
	if id, _ := identityRow(t, s, "pat"); id.Admin {
		t.Fatal("flag left")
	}
}

// A flag set before the lists existed has no admins row; MakeAdmin(false)
// still clears the flag.
func TestMakeAdminFalseClearsLegacyFlag(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	if err := s.RegisterLocal(ctx, "pat"); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error { return tx.SetAdmin(ctx, "pat", true) }); err != nil {
		t.Fatal(err)
	}
	if len(listRefs(t, s, store.ListAdmins)) != 0 {
		t.Fatal("setup: the legacy flag has an admins row")
	}
	if id, _ := identityRow(t, s, "pat"); !id.Admin {
		t.Fatal("setup: flag not set")
	}
	if err := s.MakeAdmin(ctx, "pat", false); err != nil {
		t.Fatalf("MakeAdmin(false) = %v", err)
	}
	if id, _ := identityRow(t, s, "pat"); id.Admin {
		t.Fatal("legacy flag left set")
	}
	if len(listRefs(t, s, store.ListAdmins)) != 0 {
		t.Fatal("an admins row appeared")
	}
}

func TestRemoveStaleAdminReference(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	if err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.AddReference(ctx, store.Reference{List: store.ListAdmins, Kind: store.RefIdentity, Value: "gone", CreatedAt: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.RemoveReference(ctx, adminRef("gone"))
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	if len(listRefs(t, s, store.ListAdmins)) != 0 {
		t.Fatal("row left")
	}
}
