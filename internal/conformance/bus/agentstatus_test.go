package bus

import (
	"context"
	"slices"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/core"
	"github.com/ericfitz/agentmesh/internal/dbtest"
	ab2store "github.com/ericfitz/agentmesh/internal/store"
)

// statusOpen opens a session for b and fails the test on error.
func statusOpen(t *testing.T, b ab2bus.Bus, hint string) ab2bus.Session {
	t.Helper()
	s, err := b.Open(context.Background(), ab2bus.OpenSessionInput{Hint: hint})
	if err != nil {
		t.Fatalf("Open(%q): %v", hint, err)
	}
	return s
}

// statusSet sets a status and fails the test on error.
func statusSet(t *testing.T, b ab2bus.Bus, session string, tag ab2bus.StatusTag, text, problem string) ab2bus.AgentStatus {
	t.Helper()
	st, err := b.SetAgentStatus(context.Background(), ab2bus.SetAgentStatusInput{Session: session, Tag: tag, Text: text, Problem: problem})
	if err != nil {
		t.Fatalf("SetAgentStatus(%s): %v", tag, err)
	}
	return st
}

func statusRevisions(hist []ab2bus.AgentStatus) []int64 {
	out := make([]int64, 0, len(hist))
	for _, h := range hist {
		out = append(out, h.Revision)
	}
	return out
}

func statusEntry(t *testing.T, es []ab2bus.AgentStatusEntry, identity string) (ab2bus.AgentStatusEntry, bool) {
	t.Helper()
	for _, e := range es {
		if e.Identity == identity {
			return e, true
		}
	}
	return ab2bus.AgentStatusEntry{}, false
}

func statusNames(es []ab2bus.AgentStatusEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Identity)
	}
	return out
}

func TestAgentStatusSetAndRead(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		s := statusOpen(t, pat, "")
		statusSet(t, pat, s.ID, ab2bus.StatusPlanning, "reading the brief", "")
		h.clock.Advance(time.Second)
		statusSet(t, pat, s.ID, ab2bus.StatusImplementing, "writing the store", "flaky test")
		h.clock.Advance(time.Second)
		statusSet(t, pat, s.ID, ab2bus.StatusWaiting, "waiting for review", "")

		kim := h.as("kim")
		es, err := kim.AgentStatuses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(es) != 1 {
			t.Fatalf("AgentStatuses = %v, want one entry", statusNames(es))
		}
		got := es[0]
		if got.Identity != "pat" || got.Kind != ab2bus.IdentityAgent || !got.Live ||
			got.Status.Revision != 3 || got.Status.Tag != ab2bus.StatusWaiting || got.Status.LastTag != ab2bus.StatusWaiting {
			t.Fatalf("entry = %+v", got)
		}
		hist, err := kim.AgentStatusHistory(ctx, "pat")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(statusRevisions(hist), []int64{1, 2, 3}) {
			t.Fatalf("history revisions = %v, want [1 2 3]", statusRevisions(hist))
		}
		for _, st := range hist {
			if (st.Revision == 2) != (st.Problem != "") {
				t.Fatalf("revision %d problem = %q, want a problem on revision 2 only", st.Revision, st.Problem)
			}
		}
		ids, err := kim.Identities(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, id := range ids {
			if id.Name == "pat" {
				found = true
				if id.Status == nil || id.Status.Tag != ab2bus.StatusWaiting {
					t.Fatalf("Identities pat status = %+v, want tag waiting", id.Status)
				}
			}
		}
		if !found {
			t.Fatal("Identities does not list pat")
		}
	})
}

func TestAgentStatusHistoryBoundedAtN(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		cfg := core.Defaults()
		cfg.StatusHistoryCount = 3
		h := newHarness(t, e, harnessOptions{cfg: &cfg})
		pat := h.as("pat")
		s := statusOpen(t, pat, "")
		for i := 0; i < 5; i++ {
			statusSet(t, pat, s.ID, ab2bus.StatusImplementing, "step", "")
			h.clock.Advance(time.Second)
		}
		hist, err := h.as("kim").AgentStatusHistory(context.Background(), "pat")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(statusRevisions(hist), []int64{3, 4, 5}) {
			t.Fatalf("history revisions = %v, want [3 4 5]", statusRevisions(hist))
		}
	})
}

func TestAgentStatusValidation(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		pat := h.as("pat")
		s := statusOpen(t, pat, "")
		cases := []struct {
			name string
			in   ab2bus.SetAgentStatusInput
		}{
			{"missing_tag", ab2bus.SetAgentStatusInput{Session: s.ID, Text: "x"}},
			{"offline", ab2bus.SetAgentStatusInput{Session: s.ID, Tag: ab2bus.StatusOffline, Text: "x"}},
			{"bogus", ab2bus.SetAgentStatusInput{Session: s.ID, Tag: "bogus", Text: "x"}},
			{"empty_text", ab2bus.SetAgentStatusInput{Session: s.ID, Tag: ab2bus.StatusIdle}},
			{"multiline_text", ab2bus.SetAgentStatusInput{Session: s.ID, Tag: ab2bus.StatusIdle, Text: "a\nb"}},
			{"carriage_return_text", ab2bus.SetAgentStatusInput{Session: s.ID, Tag: ab2bus.StatusIdle, Text: "a\rb"}},
			{"multiline_problem", ab2bus.SetAgentStatusInput{Session: s.ID, Tag: ab2bus.StatusIdle, Text: "x", Problem: "a\nb"}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				_, err := pat.SetAgentStatus(context.Background(), c.in)
				codeOf(t, err, ab2bus.CodeValidation)
			})
		}
		es, err := h.as("kim").AgentStatuses(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(es) != 0 {
			t.Fatalf("a refused set stored a status: %v", statusNames(es))
		}
	})
}

func TestAgentStatusReplay(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		s := statusOpen(t, pat, "")
		in := ab2bus.SetAgentStatusInput{Session: s.ID, Tag: ab2bus.StatusReviewing, Text: "same", IdempotencyKey: "k1"}
		a, err := pat.SetAgentStatus(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(time.Second)
		b, err := pat.SetAgentStatus(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if a.Revision != b.Revision {
			t.Fatalf("replayed set revision %d, first %d; want equal", b.Revision, a.Revision)
		}
		hist, err := pat.AgentStatusHistory(ctx, "pat")
		if err != nil {
			t.Fatal(err)
		}
		if len(hist) != 1 {
			t.Fatalf("keyed twice: history revisions %v, want one", statusRevisions(hist))
		}
		un := ab2bus.SetAgentStatusInput{Session: s.ID, Tag: ab2bus.StatusIdle, Text: "unkeyed"}
		for i := 0; i < 2; i++ {
			if _, err := pat.SetAgentStatus(ctx, un); err != nil {
				t.Fatal(err)
			}
			h.clock.Advance(time.Second)
		}
		hist, err = pat.AgentStatusHistory(ctx, "pat")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(statusRevisions(hist), []int64{1, 2, 3}) {
			t.Fatalf("unkeyed twice: history revisions %v, want [1 2 3]", statusRevisions(hist))
		}
	})
}

func TestAgentStatusAccess(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		t.Run("human_set_refused", func(t *testing.T) {
			h := newHarness(t, e, harnessOptions{})
			hal := h.human("hal@corp.example")
			s := statusOpen(t, hal, "")
			_, err := hal.SetAgentStatus(context.Background(), ab2bus.SetAgentStatusInput{Session: s.ID, Tag: ab2bus.StatusIdle, Text: "hi"})
			codeOf(t, err, ab2bus.CodeForbidden)
		})
		t.Run("other_session_refused", func(t *testing.T) {
			h := newHarness(t, e, harnessOptions{})
			s := statusOpen(t, h.as("pat"), "")
			_, err := h.as("kim").SetAgentStatus(context.Background(), ab2bus.SetAgentStatusInput{Session: s.ID, Tag: ab2bus.StatusIdle, Text: "hi"})
			codeOf(t, err, ab2bus.CodeNotFound)
		})
		t.Run("admin_reads_all", func(t *testing.T) {
			h := newHarness(t, e, harnessOptions{})
			pat := h.as("pat")
			statusSet(t, pat, statusOpen(t, pat, "").ID, ab2bus.StatusIdle, "hi", "")
			es, err := h.admin("root").AgentStatuses(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := statusEntry(t, es, "pat"); !ok {
				t.Fatalf("admin list = %v, want pat", statusNames(es))
			}
		})
	})
}

func TestAgentStatusScope(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		pat := h.localAs("pat")
		root := h.localAdmin("root")
		for _, b := range []ab2bus.Bus{pat, root, coder} {
			statusSet(t, b, statusOpen(t, b, "").ID, ab2bus.StatusImplementing, "working", "")
		}
		es, err := coder.AgentStatuses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		names := statusNames(es)
		slices.Sort(names)
		if !slices.Equal(names, []string{"job1-coder", "root"}) {
			t.Fatalf("scoped list = %v, want [job1-coder root]", names)
		}
		_, err = coder.AgentStatusHistory(ctx, "pat")
		codeOf(t, err, ab2bus.CodeNotFound)
		if _, err := coder.AgentStatusHistory(ctx, "root"); err != nil {
			t.Fatalf("history of an administrator: %v", err)
		}
	})
}

func TestAgentStatusOfflineAfterLapse(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		statusSet(t, pat, statusOpen(t, pat, "").ID, ab2bus.StatusNeedsHuman, "stuck", "")
		h.clock.Advance(time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds+1) * time.Second)
		kim := h.as("kim")
		es, err := kim.AgentStatuses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := statusEntry(t, es, "pat")
		if !ok || got.Live || got.Status.Tag != ab2bus.StatusOffline || got.Status.LastTag != ab2bus.StatusNeedsHuman || got.Status.Text != "stuck" {
			t.Fatalf("entry = %+v (found %v), want not live, tag offline, last tag needs-human, text kept", got, ok)
		}
		ids, err := kim.Identities(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if id.Name == "pat" {
				t.Fatalf("Identities lists lapsed pat: %+v", id)
			}
		}
	})
}

func TestAgentStatusHintedSession(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		primary := statusOpen(t, pat, "")
		hinted := statusOpen(t, pat, "reviewer")
		first := statusSet(t, pat, hinted.ID, ab2bus.StatusReviewing, "checking", "")
		if first.DisplayName != "pat/reviewer" {
			t.Fatalf("hinted display name = %q, want pat/reviewer", first.DisplayName)
		}
		h.clock.Advance(time.Second)
		statusSet(t, pat, primary.ID, ab2bus.StatusIdle, "back", "")
		es, err := h.as("kim").AgentStatuses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(es) != 1 || es[0].Status.Revision != 2 || es[0].Status.DisplayName != "pat" || es[0].Status.Tag != ab2bus.StatusIdle {
			t.Fatalf("list = %+v, want one entry: revision 2 from the primary session", es)
		}
		hist, err := pat.AgentStatusHistory(ctx, "pat")
		if err != nil {
			t.Fatal(err)
		}
		if len(hist) != 2 || hist[0].DisplayName != "pat/reviewer" || hist[1].DisplayName != "pat" {
			t.Fatalf("history = %+v, want the hinted then the primary revision", hist)
		}
	})
}

func TestAgentStatusRetired(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")
		statusSet(t, pat, statusOpen(t, pat, "").ID, ab2bus.StatusIdle, "hi", "")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.RetireIdentity(ctx, "pat", h.core.NowMs())
			return err
		})
		kim := h.as("kim")
		es, err := kim.AgentStatuses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := statusEntry(t, es, "pat"); ok {
			t.Fatalf("list still has the retired pat: %v", statusNames(es))
		}
		_, err = kim.AgentStatusHistory(ctx, "pat")
		codeOf(t, err, ab2bus.CodeNotFound)
	})
}
