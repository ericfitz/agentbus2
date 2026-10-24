package bus

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// putKeys stores n fresh keys for identity through the Store.
func putKeys(t *testing.T, h *harness, identity string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		k, err := keys.Generate(keys.EdDSA)
		if err != nil {
			t.Fatal(err)
		}
		der, err := keys.MarshalPublic(k.Public())
		if err != nil {
			t.Fatal(err)
		}
		kid, err := keys.KID(k.Public())
		if err != nil {
			t.Fatal(err)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.PutPublicKey(ctx, ab2store.PublicKey{Identity: identity, KID: kid, Algorithm: string(keys.EdDSA), Key: der, CreatedAt: h.core.NowMs()})
		})
	}
}

func hasIdentity(t *testing.T, h *harness, name string) bool {
	t.Helper()
	found := false
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		_, err := tx.Identity(ctx, name)
		if err == nil {
			found = true
			return nil
		}
		if errors.Is(err, ab2store.ErrNotFound) {
			return nil
		}
		return err
	})
	return found
}

func channelNamesOf(t *testing.T, b ab2bus.Bus) []string {
	t.Helper()
	chs, err := b.ListChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range chs {
		out = append(out, c.Name)
	}
	return out
}

func TestAdminIdentitiesResetAndResetCredential(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		rootBus := h.admin("root")
		root := rootBus.(ab2bus.Admin)
		h.as("pat")
		putKeys(t, h, "pat", 2)
		h.channel("extra", ab2store.KindOrdinary, "root", nil)

		// Not an administrator: forbidden on both methods.
		patAdmin := h.as("pat").(ab2bus.Admin)
		codeOf(t, patAdmin.Reset(ctx, ab2bus.ResetInput{}), ab2bus.CodeForbidden)
		_, err := patAdmin.ResetCredential(ctx, ab2bus.ResetCredentialInput{Identity: "pat"})
		codeOf(t, err, ab2bus.CodeForbidden)

		// ResetCredential returns the stored key count, then 0, and an unknown identity is not_registered.
		res, err := root.ResetCredential(ctx, ab2bus.ResetCredentialInput{Identity: "pat"})
		if err != nil || res.Identity != "pat" || res.KeysRemoved != 2 {
			t.Fatalf("ResetCredential = %+v, %v", res, err)
		}
		res, err = root.ResetCredential(ctx, ab2bus.ResetCredentialInput{Identity: "pat"})
		if err != nil || res.KeysRemoved != 0 {
			t.Fatalf("second ResetCredential = %+v, %v", res, err)
		}
		_, err = root.ResetCredential(ctx, ab2bus.ResetCredentialInput{Identity: "ghost"})
		codeOf(t, err, ab2bus.CodeNotRegistered)

		// An unknown scope is validation on both transports.
		codeOf(t, root.Reset(ctx, ab2bus.ResetInput{Scope: "all"}), ab2bus.CodeValidation)
		if got := channelNamesOf(t, rootBus); len(got) != 1 || got[0] != "extra" {
			t.Fatalf("a refused scope changed state: %v", got)
		}

		// Scope "" is data: channels are [general], identities survive.
		if err := root.Reset(ctx, ab2bus.ResetInput{}); err != nil {
			t.Fatal(err)
		}
		if got := channelNamesOf(t, rootBus); len(got) != 1 || got[0] != "general" {
			t.Fatalf("channels after data reset = %v", got)
		}
		if !hasIdentity(t, h, "pat") {
			t.Fatal("data reset removed pat")
		}

		// Everything removes the other identities and keeps the caller; a second run is the same state.
		for i := 0; i < 2; i++ {
			if err := root.Reset(ctx, ab2bus.ResetInput{Scope: ab2bus.ResetEverything}); err != nil {
				t.Fatalf("everything #%d: %v", i, err)
			}
			if hasIdentity(t, h, "pat") || !hasIdentity(t, h, "root") {
				t.Fatalf("everything #%d: pat=%v root=%v", i, hasIdentity(t, h, "pat"), hasIdentity(t, h, "root"))
			}
		}
		if got := channelNamesOf(t, rootBus); len(got) != 1 || got[0] != "general" {
			t.Fatalf("channels after everything = %v", got)
		}
	})
}

type registryState struct {
	identities map[string]ab2store.Identity
	bindings   []ab2store.HumanBinding
	users      []ab2store.Reference
	admins     []ab2store.Reference
}

func readRegistry(t *testing.T, h *harness) registryState {
	t.Helper()
	st := registryState{identities: map[string]ab2store.Identity{}}
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		ids, err := tx.ListIdentities(ctx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if id.Name == ab2store.SlackBridge {
				continue // the reserved row exists in every store
			}
			st.identities[id.Name] = id
			if id.Kind == ab2store.IdentityHuman {
				b, err := tx.HumanBinding(ctx, id.Name)
				if err != nil {
					return err
				}
				st.bindings = append(st.bindings, b)
			}
		}
		if st.users, err = tx.ListReferences(ctx, ab2store.ListUsers); err != nil {
			return err
		}
		st.admins, err = tx.ListReferences(ctx, ab2store.ListAdmins)
		return err
	})
	return st
}

func idRefs(t *testing.T, refs []ab2store.Reference, want ...string) {
	t.Helper()
	if len(refs) != len(want) {
		t.Fatalf("refs = %+v, want identity refs %v", refs, want)
	}
	for i, r := range refs {
		if r.Kind != ab2store.RefIdentity || r.Value != want[i] {
			t.Fatalf("refs = %+v, want identity refs %v", refs, want)
		}
	}
}

func TestResetScopesHumans(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		adm := h.admin("adm").(ab2bus.Admin)
		h.human("eric@corp.example")
		h.human("kim@corp.example")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			for _, r := range []ab2store.Reference{
				{List: ab2store.ListUsers, Kind: ab2store.RefGroup, Issuer: "corp", Value: "pai-users", CreatedAt: h.core.NowMs()},
				{List: ab2store.ListAdmins, Kind: ab2store.RefGroup, Issuer: "corp", Value: "pai-admins", CreatedAt: h.core.NowMs()},
				{List: ab2store.ListAdmins, Kind: ab2store.RefIdentity, Value: "adm", CreatedAt: h.core.NowMs()},
			} {
				if _, err := tx.AddReference(ctx, r); err != nil {
					return err
				}
			}
			return nil
		})
		before := readRegistry(t, h)

		// data keeps identities, bindings and both lists.
		if err := adm.Reset(ctx, ab2bus.ResetInput{Scope: ab2bus.ResetData}); err != nil {
			t.Fatal(err)
		}
		after := readRegistry(t, h)
		if len(after.identities) != 3 || len(after.bindings) != 2 || len(after.users) != 1 || len(after.admins) != 2 {
			t.Fatalf("data reset changed the registry: before %+v after %+v", before, after)
		}

		// everything leaves the caller, no bindings, no users, admins exactly {identity adm}; twice.
		for i := 0; i < 2; i++ {
			if err := adm.Reset(ctx, ab2bus.ResetInput{Scope: ab2bus.ResetEverything}); err != nil {
				t.Fatalf("everything #%d: %v", i, err)
			}
			st := readRegistry(t, h)
			if len(st.identities) != 1 || !st.identities["adm"].Admin || len(st.bindings) != 0 || len(st.users) != 0 {
				t.Fatalf("everything #%d: %+v", i, st)
			}
			idRefs(t, st.admins, "adm")
		}
	})
}

// TestResetScopesHumansCallerKim: the caller is a human whose flag came only
// from a group reference (Decision 12): after everything she remains, with
// her binding, and the lists are exactly {admins identity kim}.
func TestResetScopesHumansCallerKim(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		h.human("eric@corp.example")
		h.human("kim@corp.example")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.AddReference(ctx, ab2store.Reference{List: ab2store.ListAdmins, Kind: ab2store.RefGroup, Issuer: "corp", Value: "pai-admins", CreatedAt: h.core.NowMs()}); err != nil {
				return err
			}
			if _, err := tx.AddReference(ctx, ab2store.Reference{List: ab2store.ListUsers, Kind: ab2store.RefGroup, Issuer: "corp", Value: "pai-users", CreatedAt: h.core.NowMs()}); err != nil {
				return err
			}
			return tx.SetAdmin(ctx, "kim@corp.example", true)
		})
		kim := newBus(h, "kim@corp.example").(ab2bus.Admin)
		for i := 0; i < 2; i++ {
			if err := kim.Reset(ctx, ab2bus.ResetInput{Scope: ab2bus.ResetEverything}); err != nil {
				t.Fatalf("everything #%d: %v", i, err)
			}
			st := readRegistry(t, h)
			if len(st.identities) != 1 || !st.identities["kim@corp.example"].Admin || len(st.bindings) != 1 || st.bindings[0].Value != "kim@corp.example" || len(st.users) != 0 {
				t.Fatalf("everything #%d: %+v", i, st)
			}
			idRefs(t, st.admins, "kim@corp.example")
		}
	})
}

// TestCreateIdentityByEmailOverlap: overlapping creators of one address end
// with one human row and one creator reporting created, on both engines.
func TestCreateIdentityByEmailOverlap(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		svc := identities.Service{Core: h.core}
		const n = 6
		var wg sync.WaitGroup
		var mu sync.Mutex
		created := 0
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id, c, err := svc.CreateIdentityByEmail(ctx, "eric@corp.example")
				mu.Lock()
				defer mu.Unlock()
				if err != nil || id != "eric@corp.example" {
					t.Errorf("CreateIdentityByEmail = %q, %v", id, err)
				}
				if c {
					created++
				}
			}()
		}
		wg.Wait()
		if created != 1 {
			t.Fatalf("%d creates reported created, want 1", created)
		}
		st := readRegistry(t, h)
		if len(st.identities) != 1 || len(st.bindings) != 1 || st.identities["eric@corp.example"].Kind != ab2store.IdentityHuman {
			t.Fatalf("registry = %+v", st)
		}
	})
}

func TestCreateIdentityByEmailBus(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{})
		root := h.admin("root").(ab2bus.Admin)
		h.as("agent-a")
		h.as("bot@corp.example") // an agent whose name looks like an address

		res, err := root.CreateIdentityByEmail(ctx, ab2bus.CreateIdentityByEmailInput{Email: "Eric@Corp.Example"})
		if err != nil || res.Identity != "eric@corp.example" || !res.Created {
			t.Fatalf("first = %+v, %v", res, err)
		}
		res, err = root.CreateIdentityByEmail(ctx, ab2bus.CreateIdentityByEmailInput{Email: "eric@corp.example"})
		if err != nil || res.Identity != "eric@corp.example" || res.Created {
			t.Fatalf("second = %+v, %v; want created=false", res, err)
		}

		// The same idempotency key replays the first result.
		res, err = root.CreateIdentityByEmail(ctx, ab2bus.CreateIdentityByEmailInput{Email: "pat@corp.example", IdempotencyKey: "k"})
		if err != nil || !res.Created {
			t.Fatalf("keyed first = %+v, %v", res, err)
		}
		res, err = root.CreateIdentityByEmail(ctx, ab2bus.CreateIdentityByEmailInput{Email: "pat@corp.example", IdempotencyKey: "k"})
		if err != nil || res.Identity != "pat@corp.example" || !res.Created {
			t.Fatalf("keyed replay = %+v, %v; want the first result", res, err)
		}

		agentAdmin := h.as("agent-a").(ab2bus.Admin)
		_, err = agentAdmin.CreateIdentityByEmail(ctx, ab2bus.CreateIdentityByEmailInput{Email: "x@corp.example"})
		codeOf(t, err, ab2bus.CodeForbidden)
		if hasIdentity(t, h, "x@corp.example") {
			t.Fatal("a refused request created the identity")
		}

		_, err = root.CreateIdentityByEmail(ctx, ab2bus.CreateIdentityByEmailInput{Email: "bot@corp.example"})
		codeOf(t, err, ab2bus.CodeConflict)
		_, err = root.CreateIdentityByEmail(ctx, ab2bus.CreateIdentityByEmailInput{Email: "x"})
		codeOf(t, err, ab2bus.CodeValidation)
	})
}
