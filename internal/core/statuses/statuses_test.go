package statuses

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/core"
	"github.com/ericfitz/agentmesh/internal/core/sessions"
	"github.com/ericfitz/agentmesh/internal/store"
	"github.com/ericfitz/agentmesh/internal/store/sqlite"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type fixture struct {
	t   *testing.T
	svc Service
	ses sessions.Service
	clk *clock
	st  store.Store
}

func newFixture(t *testing.T, mut func(*core.Config)) *fixture {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "agentmesh.db"), sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := &clock{t: time.UnixMilli(1_700_000_000_000)}
	cfg := core.Defaults()
	if mut != nil {
		mut(&cfg)
	}
	c, err := core.New(st, cfg, core.Options{Now: clk.now})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	ses := sessions.Service{Core: c}
	return &fixture{t: t, svc: Service{Core: c, Sessions: ses}, ses: ses, clk: clk, st: st}
}

func (f *fixture) agent(name string) {
	f.t.Helper()
	err := f.st.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.CreateIdentity(ctx, name, f.svc.NowMs())
	})
	if err != nil {
		f.t.Fatalf("CreateIdentity(%s): %v", name, err)
	}
}

func (f *fixture) human(name string) {
	f.t.Helper()
	err := f.st.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.CreateHuman(ctx, store.HumanBinding{Identity: name, ClaimType: store.ClaimEmail, Value: name}, f.svc.NowMs())
	})
	if err != nil {
		f.t.Fatalf("CreateHuman(%s): %v", name, err)
	}
}

func (f *fixture) open(identity, hint string) bus.Session {
	f.t.Helper()
	s, err := f.ses.Open(context.Background(), identity, bus.OpenSessionInput{Hint: hint})
	if err != nil {
		f.t.Fatalf("Open(%s): %v", identity, err)
	}
	return s
}

func (f *fixture) advance(d time.Duration) { f.clk.t = f.clk.t.Add(d) }

func (f *fixture) set(identity, session string, tag bus.StatusTag, text string) (bus.AgentStatus, error) {
	return f.svc.Set(context.Background(), identity, bus.SetAgentStatusInput{Session: session, Tag: tag, Text: text})
}

func (f *fixture) mustSet(identity, session string, tag bus.StatusTag, text string) bus.AgentStatus {
	f.t.Helper()
	r, err := f.set(identity, session, tag, text)
	if err != nil {
		f.t.Fatalf("Set(%s, %s): %v", identity, text, err)
	}
	return r
}

func (f *fixture) stored(identity string, limit int) []store.AgentStatus {
	f.t.Helper()
	var out []store.AgentStatus
	err := f.st.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = tx.AgentStatusHistory(ctx, identity, limit)
		return err
	})
	if err != nil {
		f.t.Fatalf("AgentStatusHistory: %v", err)
	}
	return out
}

func wantErr(t *testing.T, err error, code bus.Code, msg string) {
	t.Helper()
	be, ok := err.(*bus.Error)
	if !ok {
		t.Fatalf("err = %v (%T), want *bus.Error %s %q", err, err, code, msg)
	}
	if be.Code != code || (msg != "" && be.Message != msg) {
		t.Fatalf("err = %s %q, want %s %q", be.Code, be.Message, code, msg)
	}
}

func revisions(h []bus.AgentStatus) []int64 {
	out := make([]int64, 0, len(h))
	for _, s := range h {
		out = append(out, s.Revision)
	}
	return out
}

func equalInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const tagList = "idle, planning, implementing, reviewing, waiting, blocked, needs-human"

func TestSetValidation(t *testing.T) {
	f := newFixture(t, nil)
	f.agent("pat")
	s := f.open("pat", "")
	line := strings.Repeat("é", 201)
	cases := []struct {
		name string
		in   bus.SetAgentStatusInput
		msg  string
	}{
		{"missing tag", bus.SetAgentStatusInput{Session: s.ID, Text: "x"}, "tag is required: one of " + tagList},
		{"offline", bus.SetAgentStatusInput{Session: s.ID, Tag: bus.StatusOffline, Text: "x"}, "tag offline is set by the bus for an agent with no live session; set one of " + tagList},
		{"unknown", bus.SetAgentStatusInput{Session: s.ID, Tag: "done", Text: "x"}, `unknown tag "done": one of ` + tagList},
		{"empty text", bus.SetAgentStatusInput{Session: s.ID, Tag: bus.StatusIdle}, "text is required: one line saying what you are doing now"},
		{"whitespace text", bus.SetAgentStatusInput{Session: s.ID, Tag: bus.StatusIdle, Text: "  \t "}, "text is required: one line saying what you are doing now"},
		{"long text", bus.SetAgentStatusInput{Session: s.ID, Tag: bus.StatusIdle, Text: line}, "text must be at most 200 characters"},
		{"multi-line text", bus.SetAgentStatusInput{Session: s.ID, Tag: bus.StatusIdle, Text: "a\nb"}, "text must be a single line"},
		{"multi-line problem", bus.SetAgentStatusInput{Session: s.ID, Tag: bus.StatusBlocked, Text: "x", Problem: "a\rb"}, "problem must be a single line"},
		{"long problem", bus.SetAgentStatusInput{Session: s.ID, Tag: bus.StatusBlocked, Text: "x", Problem: line}, "problem must be at most 200 characters"},
		{"empty session", bus.SetAgentStatusInput{Tag: bus.StatusIdle, Text: "x"}, "session is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.Set(context.Background(), "pat", tc.in)
			wantErr(t, err, bus.CodeValidation, tc.msg)
			if n := len(f.stored("pat", 100)); n != 0 {
				t.Fatalf("%d rows written", n)
			}
		})
	}
	// Exactly 200 runes passes; a whitespace-only problem is stored empty.
	r, err := f.svc.Set(context.Background(), "pat", bus.SetAgentStatusInput{Session: s.ID, Tag: bus.StatusBlocked, Text: strings.Repeat("é", 200), Problem: "  "})
	if err != nil {
		t.Fatalf("200 runes: %v", err)
	}
	if r.Problem != "" || f.stored("pat", 100)[0].Problem != "" {
		t.Fatalf("problem = %q", r.Problem)
	}
}

func TestSetAddsRevisionsAndPrunes(t *testing.T) {
	f := newFixture(t, func(c *core.Config) { c.StatusHistoryCount = 3 })
	f.agent("pat")
	s := f.open("pat", "")
	for i := int64(1); i <= 5; i++ {
		// The identical text on every call: decision 14, each set adds a row.
		r := f.mustSet("pat", s.ID, bus.StatusImplementing, "same")
		if r.Revision != i || r.Identity != "pat" || r.Tag != bus.StatusImplementing || r.LastTag != "" {
			t.Fatalf("set %d = %+v", i, r)
		}
	}
	h, err := f.svc.History(context.Background(), "pat", "pat")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got := revisions(h); !equalInts(got, []int64{3, 4, 5}) {
		t.Fatalf("history = %v, want [3 4 5]", got)
	}
	for _, r := range h {
		if r.Session != s.ID || r.DisplayName != s.DisplayName || r.SetAt != f.svc.NowMs() {
			t.Fatalf("row = %+v, session %+v", r, s)
		}
	}
}

func TestSetReplay(t *testing.T) {
	f := newFixture(t, nil)
	f.agent("pat")
	s := f.open("pat", "")
	in := bus.SetAgentStatusInput{Session: s.ID, Tag: bus.StatusPlanning, Text: "plan", IdempotencyKey: "k"}
	a, err := f.svc.Set(context.Background(), "pat", in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b, err := f.svc.Set(context.Background(), "pat", in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if a != b || len(f.stored("pat", 100)) != 1 {
		t.Fatalf("a = %+v, b = %+v, rows %d", a, b, len(f.stored("pat", 100)))
	}
	in.Text = "other"
	_, err = f.svc.Set(context.Background(), "pat", in)
	wantErr(t, err, bus.CodeConflict, "")
	f.mustSet("pat", s.ID, bus.StatusPlanning, "plan")
	f.mustSet("pat", s.ID, bus.StatusPlanning, "plan")
	if n := len(f.stored("pat", 100)); n != 3 {
		t.Fatalf("rows = %d, want 3 (one keyed, two unkeyed)", n)
	}
}

func TestSetRefusesHuman(t *testing.T) {
	f := newFixture(t, nil)
	f.human("kim@corp.example")
	f.agent("pat")
	hs := f.open("kim@corp.example", "")
	_, err := f.set("kim@corp.example", hs.ID, bus.StatusIdle, "x")
	wantErr(t, err, bus.CodeForbidden, "kim@corp.example is not an agent; only an agent sets a status")
	ps := f.open("pat", "")
	_, err = f.set("kim@corp.example", ps.ID, bus.StatusIdle, "x")
	wantErr(t, err, bus.CodeNotFound, "")
	if n := len(f.stored("kim@corp.example", 100)); n != 0 {
		t.Fatalf("rows = %d", n)
	}
}

func TestSetLapsedSession(t *testing.T) {
	f := newFixture(t, nil)
	f.agent("pat")
	s := f.open("pat", "")
	f.advance(time.Duration(f.svc.Cfg.SessionLeaseSeconds+f.svc.Cfg.LeaseSkewSeconds+1) * time.Second)
	_, err := f.set("pat", s.ID, bus.StatusIdle, "x")
	wantErr(t, err, bus.CodeSessionLapsed, "")
	s2 := f.open("pat", "")
	if err := f.ses.Close(context.Background(), "pat", bus.CloseSessionInput{Session: s2.ID}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = f.set("pat", s2.ID, bus.StatusIdle, "x")
	wantErr(t, err, bus.CodeSessionClosed, "")
	if n := len(f.stored("pat", 100)); n != 0 {
		t.Fatalf("rows = %d", n)
	}
}

func TestSetRenewsLease(t *testing.T) {
	f := newFixture(t, nil)
	f.agent("pat")
	s := f.open("pat", "")
	f.advance(30 * time.Second)
	f.mustSet("pat", s.ID, bus.StatusIdle, "x")
	var row store.Session
	err := f.st.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = tx.Session(ctx, s.ID)
		return err
	})
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if want := f.svc.LeaseUntil(f.svc.NowMs()); row.LeaseExpiresAt != want {
		t.Fatalf("LeaseExpiresAt = %d, want %d", row.LeaseExpiresAt, want)
	}
}

// open note: agent-status 2
func TestListDerivesOffline(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)
	f.agent("pat")
	f.agent("kim")
	f.agent("quiet")
	s1 := f.open("pat", "")
	f.mustSet("pat", s1.ID, bus.StatusImplementing, "coding")
	kimSession := f.open("kim", "")
	f.mustSet("kim", kimSession.ID, bus.StatusNeedsHuman, "stuck")
	list, err := f.svc.List(ctx, "kim")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Identity != "kim" || list[1].Identity != "pat" {
		t.Fatalf("list = %+v (quiet has no status, so it is absent)", list)
	}
	pat := list[1]
	if !pat.Live || pat.Kind != bus.IdentityKind("agent") || pat.Status.Tag != bus.StatusImplementing || pat.Status.LastTag != bus.StatusImplementing {
		t.Fatalf("live pat = %+v", pat)
	}
	setAt := pat.Status.SetAt

	// A second live session keeps pat live when the first lapses.
	f.advance(60 * time.Second)
	f.open("pat", "second")
	lease := int64(f.svc.Cfg.SessionLeaseSeconds+f.svc.Cfg.LeaseSkewSeconds) * 1000
	// Boundary: live at exactly LeaseExpiresAt + skew, offline 1 ms after.
	f.clk.t = time.UnixMilli(1_700_000_000_000 + lease)
	list, _ = f.svc.List(ctx, "kim")
	if !entry(list, "pat").Live {
		t.Fatal("pat offline at exactly lease+skew while its second session is live")
	}
	// kim's only session lapses: at lease+skew it is live, 1 ms later offline.
	list, _ = f.svc.List(ctx, "kim")
	if !entry(list, "kim").Live {
		t.Fatal("kim offline at exactly lease+skew")
	}
	f.clk.t = f.clk.t.Add(time.Millisecond)
	list, _ = f.svc.List(ctx, "kim")
	k := entry(list, "kim")
	if k.Live || k.Status.Tag != bus.StatusOffline || k.Status.LastTag != bus.StatusNeedsHuman || k.Status.Text != "stuck" {
		t.Fatalf("lapsed kim = %+v", k)
	}
	if !entry(list, "pat").Live {
		t.Fatal("pat should stay live through its second session")
	}
	// Both sessions of pat lapse.
	f.advance(2 * time.Duration(lease) * time.Millisecond)
	list, _ = f.svc.List(ctx, "kim")
	p := entry(list, "pat")
	if p.Live || p.Status.Tag != bus.StatusOffline || p.Status.LastTag != bus.StatusImplementing || p.Status.SetAt != setAt {
		t.Fatalf("lapsed pat = %+v (set_at was %d)", p, setAt)
	}
}

func entry(l []bus.AgentStatusEntry, name string) bus.AgentStatusEntry {
	for _, e := range l {
		if e.Identity == name {
			return e
		}
	}
	return bus.AgentStatusEntry{}
}

func TestListEmptyIsNotNil(t *testing.T) {
	f := newFixture(t, nil)
	f.agent("pat")
	l, err := f.svc.List(context.Background(), "pat")
	if err != nil || l == nil || len(l) != 0 {
		t.Fatalf("List = %#v, %v", l, err)
	}
	h, err := f.svc.History(context.Background(), "pat", "pat")
	if err != nil || h == nil || len(h) != 0 {
		t.Fatalf("History = %#v, %v", h, err)
	}
	_, err = f.svc.History(context.Background(), "pat", "nobody")
	wantErr(t, err, bus.CodeNotFound, `identity "nobody" is not registered`)
}

func TestHistoryCapsAfterLoweringN(t *testing.T) {
	f := newFixture(t, func(c *core.Config) { c.StatusHistoryCount = 5 })
	f.agent("pat")
	s := f.open("pat", "")
	for i := 0; i < 5; i++ {
		f.mustSet("pat", s.ID, bus.StatusIdle, "x")
	}
	f.svc.Cfg.StatusHistoryCount = 2
	h, err := f.svc.History(context.Background(), "pat", "pat")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got := revisions(h); !equalInts(got, []int64{4, 5}) {
		t.Fatalf("history = %v, want [4 5]", got)
	}
	if n := len(f.stored("pat", 100)); n != 5 {
		t.Fatalf("store holds %d rows, want 5 until the next set", n)
	}
	f.mustSet("pat", s.ID, bus.StatusIdle, "x")
	h, _ = f.svc.History(context.Background(), "pat", "pat")
	if got := revisions(h); !equalInts(got, []int64{5, 6}) {
		t.Fatalf("history = %v, want [5 6]", got)
	}
	if n := len(f.stored("pat", 100)); n != 2 {
		t.Fatalf("store holds %d rows after the set, want 2", n)
	}
}

func TestIdentitiesCarryStatus(t *testing.T) {
	f := newFixture(t, nil)
	f.agent("pat")
	f.agent("kim")
	f.agent("quiet")
	ps := f.open("pat", "")
	f.open("kim", "")
	f.open("quiet", "")
	f.mustSet("pat", ps.ID, bus.StatusReviewing, "one")
	f.mustSet("pat", ps.ID, bus.StatusWaiting, "two")
	ids, err := f.ses.Identities(context.Background(), "kim")
	if err != nil {
		t.Fatalf("Identities: %v", err)
	}
	var sawPat, sawQuiet bool
	for _, id := range ids {
		switch id.Name {
		case "pat":
			sawPat = true
			if id.Status == nil || id.Status.Revision != 2 || id.Status.Tag != bus.StatusWaiting || id.Status.LastTag != "" {
				t.Fatalf("pat status = %+v", id.Status)
			}
		case "quiet":
			sawQuiet = true
			if id.Status != nil {
				t.Fatalf("quiet status = %+v, want nil", id.Status)
			}
		}
	}
	if !sawPat || !sawQuiet {
		t.Fatalf("identities = %+v", ids)
	}
}
