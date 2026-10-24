package bus

import (
	"context"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/tasks"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func TestSenderKind(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		eric := h.human("eric@corp.example")
		h.channel("dev", ab2store.KindOrdinary, "pat", []ab2store.ACLEntry{
			{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite, ab2store.PermAdmin}},
			{Identity: "eric@corp.example", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}},
		})
		patSess, err := pat.Open(ctx, ab2bus.OpenSessionInput{Hint: "impl"})
		if err != nil {
			t.Fatal(err)
		}
		if err := pat.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "dev"}); err != nil {
			t.Fatal(err)
		}
		if _, err := pat.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "from pat"}); err != nil {
			t.Fatal(err)
		}
		if _, err := eric.Send(ctx, ab2bus.SendInput{Channel: "dev", Content: "from eric"}); err != nil {
			t.Fatal(err)
		}
		rec, err := pat.Receive(ctx, ab2bus.ReceiveInput{Session: patSess.ID, IncludeOwn: true})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]ab2bus.IdentityKind{"from pat": ab2bus.IdentityAgent, "from eric": ab2bus.IdentityHuman}
		if len(rec.Messages) != 2 {
			t.Fatalf("Receive = %+v, want 2 messages", rec.Messages)
		}
		for _, m := range rec.Messages {
			if m.SenderKind != want[m.Content] {
				t.Errorf("Receive %q: SenderKind = %q, want %q", m.Content, m.SenderKind, want[m.Content])
			}
		}
		hist, err := pat.History(ctx, ab2bus.HistoryInput{Channel: "dev"})
		if err != nil || len(hist) != 2 {
			t.Fatalf("History = %v, %v", hist, err)
		}
		for _, m := range hist {
			if m.SenderKind != want[m.Content] {
				t.Errorf("History %q: SenderKind = %q, want %q", m.Content, m.SenderKind, want[m.Content])
			}
		}
	})
}

func TestIdentitiesKind(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		h.human("eric@corp.example")
		h.session("pat", "pat/impl")
		h.session("eric@corp.example", "eric")
		got, err := pat.Identities(ctx)
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]ab2bus.IdentityKind{}
		for _, i := range got {
			kinds[i.Name] = i.Kind
		}
		if len(kinds) != 2 || kinds["pat"] != ab2bus.IdentityAgent || kinds["eric@corp.example"] != ab2bus.IdentityHuman {
			t.Fatalf("Identities kinds = %v, want pat agent and eric human", kinds)
		}
	})
}

func TestReclaimSenderKind(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("work", ab2store.KindTasks, "pat", nil)
		pat := h.as("pat")
		eric := h.human("eric@corp.example")
		h.session("eric@corp.example", "eric")
		made, err := pat.Create(ctx, ab2bus.TaskCreateInput{Channel: "work", Subject: "first"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := eric.Claim(ctx, ab2bus.TaskClaimInput{ID: made.ID, LeasedUntil: h.taskLeaseIn(10_000_000_000)}); err != nil {
			t.Fatal(err)
		}
		// The human's claim row is marked human.
		var claimKind ab2store.IdentityKind
		for _, m := range h.taskRevisions(made.ID) {
			if m.Revision != nil && *m.Revision == 2 {
				claimKind = m.SenderKind
			}
		}
		if claimKind != ab2store.IdentityHuman {
			t.Fatalf("claim revision SenderKind = %q, want human", claimKind)
		}
		h.clock.Advance(11 * 1_000_000_000)
		n, err := (tasks.Service{Core: h.core}).ReclaimChannel(ctx, ch.ID, h.core.NowMs())
		if err != nil || n != 1 {
			t.Fatalf("ReclaimChannel = %d, %v; want 1", n, err)
		}
		var reclaimed *ab2store.Message
		revs := h.taskRevisions(made.ID)
		for i := range revs {
			if revs[i].Type == "reclaimed" {
				reclaimed = &revs[i]
			}
		}
		if reclaimed == nil || reclaimed.SenderKind != ab2store.IdentityAgent {
			t.Fatalf("revisions = %+v, want a reclaimed row marked agent", revs)
		}
	})
}
