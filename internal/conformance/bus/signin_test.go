package bus

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

const (
	siEric   = "eric@corp.example"
	siIssuer = "https://idp"
)

func siClaims() identities.HumanClaims {
	return identities.HumanClaims{IssuerName: "corp", Issuer: siIssuer, Email: siEric, Subject: "guid-1"}
}

func siAdd(t *testing.T, svc identities.Service, list ab2store.RefList, kind ab2store.RefKind, issuer, value string) {
	t.Helper()
	_, err := svc.AddReference(context.Background(), ab2store.Reference{List: list, Kind: kind, Issuer: issuer, Value: value}, []string{"corp", "other"})
	if err != nil {
		t.Fatalf("AddReference(%s %s %s): %v", list, kind, value, err)
	}
}

func siUsersEric(t *testing.T, svc identities.Service) {
	t.Helper()
	siAdd(t, svc, ab2store.ListUsers, ab2store.RefEmail, "corp", siEric)
}

// siState reads the identity row (found false when absent) and its binding.
func siState(t *testing.T, h *harness, name string) (id ab2store.Identity, found bool, b ab2store.HumanBinding, bound bool, general bool) {
	t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		id, err = tx.Identity(ctx, name)
		if errors.Is(err, ab2store.ErrNotFound) {
			err = nil
		} else if err == nil {
			found = true
		}
		if err != nil {
			return err
		}
		b, err = tx.HumanBinding(ctx, name)
		if err == nil {
			bound = true
		} else if !errors.Is(err, ab2store.ErrNotFound) {
			return err
		}
		if found {
			gen, err := tx.ChannelByName(ctx, "general")
			if err != nil {
				return err
			}
			subs, err := tx.Subscriptions(ctx, name)
			if err != nil {
				return err
			}
			for _, s := range subs {
				if s.ChannelID == gen.ID {
					general = true
				}
			}
		}
		return nil
	})
	return
}

func TestSignInHumanGate(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		c := siClaims()
		c.Groups = []string{"pai-users"}

		_, err := svc.SignInHuman(ctx, c)
		codeOf(t, err, ab2bus.CodeForbidden)
		if _, found, _, _, _ := siState(t, h, siEric); found {
			t.Fatal("a refused sign-in created the identity")
		}

		// A ref for another issuer does not admit corp claims.
		siAdd(t, svc, ab2store.ListUsers, ab2store.RefEmail, "other", siEric)
		_, err = svc.SignInHuman(ctx, c)
		codeOf(t, err, ab2bus.CodeForbidden)

		siUsersEric(t, svc)
		res, err := svc.SignInHuman(ctx, c)
		if err != nil || !res.Created || res.Identity != siEric || res.Admin {
			t.Fatalf("SignInHuman = %+v, %v; want created %s, not admin", res, err, siEric)
		}
		id, found, b, bound, general := siState(t, h, siEric)
		if !found || id.Kind != ab2store.IdentityHuman || !bound || b.Issuer != siIssuer || b.ProviderSubject != "guid-1" || b.Value != siEric || !general {
			t.Fatalf("state = %+v %+v bound %v general %v", id, b, bound, general)
		}
	})
}

func TestSignInHumanGateGroupAndSubject(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		siAdd(t, svc, ab2store.ListUsers, ab2store.RefGroup, "corp", "pai-users")
		c := siClaims()
		_, err := svc.SignInHuman(ctx, c)
		codeOf(t, err, ab2bus.CodeForbidden)
		c.Groups = []string{"pai-users"}
		if res, err := svc.SignInHuman(ctx, c); err != nil || !res.Created {
			t.Fatalf("group admit = %+v, %v", res, err)
		}

		siAdd(t, svc, ab2store.ListUsers, ab2store.RefSubject, "corp", "guid-9")
		c2 := identities.HumanClaims{IssuerName: "corp", Issuer: siIssuer, Email: "kim@corp.example", Subject: "guid-9"}
		if res, err := svc.SignInHuman(ctx, c2); err != nil || !res.Created {
			t.Fatalf("subject admit = %+v, %v", res, err)
		}
	})
}

func TestSignInHumanRejectsMissingClaims(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		siUsersEric(t, svc)
		for name, mod := range map[string]func(*identities.HumanClaims){
			"empty email":      func(c *identities.HumanClaims) { c.Email = "" },
			"no at sign":       func(c *identities.HumanClaims) { c.Email = "eric" },
			"everyone":         func(c *identities.HumanClaims) { c.Email = "everyone" },
			"empty subject":    func(c *identities.HumanClaims) { c.Subject = "" },
			"empty issuer URL": func(c *identities.HumanClaims) { c.Issuer = "" },
		} {
			c := siClaims()
			mod(&c)
			_, err := svc.SignInHuman(context.Background(), c)
			codeOf(t, err, ab2bus.CodeUnauthenticated)
			if _, found, _, _, _ := siState(t, h, siEric); found {
				t.Fatalf("%s: identity created", name)
			}
		}
	})
}

func TestSignInHumanBindsPrecreated(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		if _, created, err := svc.CreateIdentityByEmail(ctx, siEric); err != nil || !created {
			t.Fatalf("CreateIdentityByEmail = %v, %v", created, err)
		}
		siAdd(t, svc, ab2store.ListUsers, ab2store.RefIdentity, "", siEric)
		for i := 0; i < 2; i++ {
			res, err := svc.SignInHuman(ctx, siClaims())
			if err != nil || res.Created || res.Identity != siEric {
				t.Fatalf("sign-in %d = %+v, %v; want not created", i, res, err)
			}
			_, _, b, _, _ := siState(t, h, siEric)
			if b.ProviderSubject != "guid-1" || b.Issuer != siIssuer {
				t.Fatalf("sign-in %d binding = %+v", i, b)
			}
		}
	})
}

func TestSignInHumanReassignedEmail(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		siUsersEric(t, svc)
		if _, err := svc.SignInHuman(ctx, siClaims()); err != nil {
			t.Fatal(err)
		}
		c := siClaims()
		c.Subject = "guid-2"
		_, err := svc.SignInHuman(ctx, c)
		codeOf(t, err, ab2bus.CodeUnauthenticated)
		if err == nil || !strings.Contains(err.Error(), "different subject") {
			t.Fatalf("err = %v, want different subject", err)
		}
		// A different issuer URL with the same subject is also a different subject.
		c3 := siClaims()
		c3.Issuer = "https://idp2"
		_, err = svc.SignInHuman(ctx, c3)
		codeOf(t, err, ab2bus.CodeUnauthenticated)

		if _, err := svc.ResetCredential(ctx, siEric); err != nil {
			t.Fatal(err)
		}
		res, err := svc.SignInHuman(ctx, c)
		if err != nil || res.Created {
			t.Fatalf("after reset = %+v, %v", res, err)
		}
		_, _, b, _, _ := siState(t, h, siEric)
		if b.ProviderSubject != "guid-2" {
			t.Fatalf("binding = %+v, want guid-2", b)
		}
	})
}

func TestSignInHumanChangedEmail(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		siUsersEric(t, svc)
		siAdd(t, svc, ab2store.ListUsers, ab2store.RefEmail, "corp", "eric2@corp.example")
		if _, err := svc.SignInHuman(ctx, siClaims()); err != nil {
			t.Fatal(err)
		}
		c := siClaims()
		c.Email = "eric2@corp.example"
		_, err := svc.SignInHuman(ctx, c)
		codeOf(t, err, ab2bus.CodeUnauthenticated)
		if err == nil || !strings.Contains(err.Error(), siEric) {
			t.Fatalf("err = %v, want it to name %s", err, siEric)
		}
		if _, found, _, _, _ := siState(t, h, "eric2@corp.example"); found {
			t.Fatal("a second identity was created for the same subject")
		}
	})
}

func TestSignInHumanAgentName(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		h.as("kim@corp.example")
		siAdd(t, svc, ab2store.ListUsers, ab2store.RefEmail, "corp", "kim@corp.example")
		c := identities.HumanClaims{IssuerName: "corp", Issuer: siIssuer, Email: "kim@corp.example", Subject: "guid-k"}
		_, err := svc.SignInHuman(ctx, c)
		codeOf(t, err, ab2bus.CodeConflict)
		if _, _, _, bound, _ := siState(t, h, "kim@corp.example"); bound {
			t.Fatal("a binding was written")
		}
	})
}

func TestSignInHumanAdmins(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		siUsersEric(t, svc)
		siAdd(t, svc, ab2store.ListAdmins, ab2store.RefGroup, "corp", "pai-admins")
		c := siClaims()
		c.Groups = []string{"pai-admins"}
		res, err := svc.SignInHuman(ctx, c)
		if err != nil || !res.Admin {
			t.Fatalf("with group = %+v, %v; want admin", res, err)
		}
		if id, _, _, _, _ := siState(t, h, siEric); !id.Admin {
			t.Fatal("admin flag not written")
		}
		res, err = svc.SignInHuman(ctx, siClaims())
		if err != nil || res.Admin {
			t.Fatalf("without group = %+v, %v; want not admin", res, err)
		}
		if id, _, _, _, _ := siState(t, h, siEric); id.Admin {
			t.Fatal("admin flag not cleared")
		}
		siAdd(t, svc, ab2store.ListAdmins, ab2store.RefIdentity, "", siEric)
		res, err = svc.SignInHuman(ctx, siClaims())
		if err != nil || !res.Admin {
			t.Fatalf("identity ref = %+v, %v; want admin", res, err)
		}
	})
}

func TestSignInHumanEmailNormalized(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		siUsersEric(t, svc)
		c := siClaims()
		c.Email = " Eric@Corp.Example "
		res, err := svc.SignInHuman(context.Background(), c)
		if err != nil || res.Identity != siEric || !res.Created {
			t.Fatalf("SignInHuman = %+v, %v", res, err)
		}
	})
}

// siConcurrent signs in n times at once, each over its own Core on the same
// Store, and returns the results (every error fails the test).
func siConcurrent(t *testing.T, h *harness, n int, c identities.HumanClaims) []identities.HumanSignIn {
	t.Helper()
	svcs := make([]identities.Service, n)
	svcs[0] = identities.Service{Core: h.core}
	for i := 1; i < n; i++ {
		svcs[i] = coreOver(t, h)
	}
	res := make([]identities.HumanSignIn, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range svcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], errs[i] = svcs[i].SignInHuman(context.Background(), c)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("sign-in %d: %v", i, err)
		}
	}
	return res
}

func TestSignInHumanConcurrent(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		siUsersEric(t, identities.Service{Core: h.core})
		res := siConcurrent(t, h, 8, siClaims())
		created := 0
		for _, r := range res {
			if r.Identity != siEric {
				t.Fatalf("result %+v", r)
			}
			if r.Created {
				created++
			}
		}
		if created != 1 {
			t.Fatalf("%d results Created, want exactly 1", created)
		}
		_, found, b, bound, _ := siState(t, h, siEric)
		if !found || !bound || b.ProviderSubject != "guid-1" {
			t.Fatalf("state: found %v bound %v %+v", found, bound, b)
		}
	})
}

func TestSignInHumanConcurrentPrecreated(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		if _, _, err := svc.CreateIdentityByEmail(context.Background(), siEric); err != nil {
			t.Fatal(err)
		}
		siAdd(t, svc, ab2store.ListUsers, ab2store.RefIdentity, "", siEric)
		for _, r := range siConcurrent(t, h, 8, siClaims()) {
			if r.Created || r.Identity != siEric {
				t.Fatalf("result %+v, want not created", r)
			}
		}
		_, _, b, _, _ := siState(t, h, siEric)
		if b.ProviderSubject != "guid-1" || b.Issuer != siIssuer {
			t.Fatalf("binding = %+v", b)
		}
	})
}

// adminCountStore counts the SetAdmin calls its transactions make.
type adminCountStore struct {
	ab2store.Store
	n *atomic.Int32
}

func (s adminCountStore) Tx(ctx context.Context, fn func(context.Context, ab2store.Tx) error) error {
	return s.Store.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error {
		return fn(ctx, adminCountTx{Tx: tx, n: s.n})
	})
}

type adminCountTx struct {
	ab2store.Tx
	n *atomic.Int32
}

func (t adminCountTx) SetAdmin(ctx context.Context, name string, admin bool) error {
	t.n.Add(1)
	return t.Tx.SetAdmin(ctx, name, admin)
}

// Item 30 and Definition of Done D: the admin flag is written only when the
// evaluated value differs from the stored one, so a repeated sign-in with the
// same claims writes nothing.
func TestSignInHumanWritesAdminOnlyOnChange(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		var n atomic.Int32
		c, err := core.New(adminCountStore{Store: h.raw, n: &n}, h.cfg, core.Options{Now: h.clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		svc := identities.Service{Core: c}
		siUsersEric(t, svc)
		siAdd(t, svc, ab2store.ListAdmins, ab2store.RefGroup, "corp", "pai-admins")
		withGroup := siClaims()
		withGroup.Groups = []string{"pai-admins"}
		step := func(name string, claims identities.HumanClaims, wantAdmin bool, wantWrites int32) {
			t.Helper()
			n.Store(0)
			res, err := svc.SignInHuman(ctx, claims)
			if err != nil || res.Admin != wantAdmin {
				t.Fatalf("%s: SignInHuman = %+v, %v; want admin %v", name, res, err, wantAdmin)
			}
			if got := n.Load(); got != wantWrites {
				t.Fatalf("%s: SetAdmin called %d times, want %d", name, got, wantWrites)
			}
			if id, _, _, _, _ := siState(t, h, siEric); id.Admin != wantAdmin {
				t.Fatalf("%s: stored admin = %v, want %v", name, id.Admin, wantAdmin)
			}
		}
		step("first sign-in with the admin group", withGroup, true, 1)
		step("second identical sign-in", withGroup, true, 0)
		step("sign-in that drops the group", siClaims(), false, 1)
		step("repeat without the group", siClaims(), false, 0)
	})
}
