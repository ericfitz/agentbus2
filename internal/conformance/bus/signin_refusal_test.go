package bus

import (
	"context"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/dbtest"
)

// A sign-in that misses the users gate writes nothing: a human an admin
// created ahead of time stays unbound, and its admin flag stays as it was.
func TestSignInHumanUsersMissLeavesPrecreatedHuman(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		if _, created, err := svc.CreateIdentityByEmail(ctx, siEric); err != nil || !created {
			t.Fatalf("CreateIdentityByEmail = %v, %v", created, err)
		}
		if err := svc.MakeAdmin(ctx, siEric, true); err != nil {
			t.Fatal(err)
		}
		// A pre-created human has a binding row with no provider subject yet.
		before, _, bindingBefore, bound0, _ := siState(t, h, siEric)
		if !bound0 || bindingBefore.ProviderSubject != "" || bindingBefore.Issuer != "" || !before.Admin {
			t.Fatalf("setup: binding %+v (bound %v) admin %v, want an unclaimed binding and the admin flag", bindingBefore, bound0, before.Admin)
		}

		// No users entry covers the claims, however many times it is tried.
		for i := 0; i < 2; i++ {
			_, err := svc.SignInHuman(ctx, siClaims())
			codeOf(t, err, ab2bus.CodeForbidden)
			id, found, b, bound, _ := siState(t, h, siEric)
			if !found || !bound || b != bindingBefore {
				t.Fatalf("miss %d: found %v bound %v binding %+v, want the unclaimed binding %+v", i, found, bound, b, bindingBefore)
			}
			if !id.Admin {
				t.Fatalf("miss %d: the admin flag was cleared", i)
			}
		}
	})
}

// A sign-in refused after the users gate (a changed subject) leaves the
// existing binding as it was.
func TestSignInHumanRefusalKeepsBinding(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		siUsersEric(t, svc)
		if _, err := svc.SignInHuman(ctx, siClaims()); err != nil {
			t.Fatal(err)
		}
		_, _, want, _, _ := siState(t, h, siEric)
		c := siClaims()
		c.Subject = "guid-2"
		_, err := svc.SignInHuman(ctx, c)
		codeOf(t, err, ab2bus.CodeUnauthenticated)
		_, _, got, bound, _ := siState(t, h, siEric)
		if !bound || got != want {
			t.Fatalf("binding after a refused sign-in = %+v (bound %v), want %+v", got, bound, want)
		}
	})
}
