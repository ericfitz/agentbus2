package mcpserver_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/core/maintenance"
	"github.com/ericfitz/agentbus2/internal/mcpserver"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

func TestRegisterResumesAfterRestart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clean bool
	}{{"crash", false}, {"clean", true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			tbl := newFakeTable()
			first := startServer(t, e, tbl, "claude-code")
			reg := first.must(t, "register", nil)
			sid := str(reg, "session")
			if tc.clean {
				if err := first.Close(); err != nil {
					t.Fatal(err)
				}
			}
			// Either way the state file is still there and the MCP server is gone.
			if _, err := sessionstate.Read(e.DataDir, sid); err != nil {
				t.Fatalf("state file after the exit: %v", err)
			}
			tbl.kill(42)

			second := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { o.Self = 43 })
			// With a hint it is a new session; the primary file is untouched.
			hinted := second.must(t, "register", map[string]any{"hint": "other"})
			if hinted["resumed"] != false || str(hinted, "session") == sid {
				t.Fatalf("hinted register: %v", hinted)
			}
			got := second.must(t, "register", nil)
			if got["resumed"] != true || str(got, "session") != sid || str(got, "resume_token") != str(reg, "resume_token") {
				t.Fatalf("register after restart: %v (first %v)", got, reg)
			}
			f, err := sessionstate.Read(e.DataDir, sid)
			if err != nil {
				t.Fatal(err)
			}
			if f.MCPPID != 43 || !f.Primary {
				t.Errorf("file not rewritten for the new server: %+v", f)
			}
			// A third register without a hint in the same server is a new session.
			third := second.must(t, "register", nil)
			if third["resumed"] != false || str(third, "session") == sid {
				t.Errorf("third register: %v", third)
			}
		})
	}
}

func TestRegisterIgnoresOtherHarnessAndLiveServers(t *testing.T) {
	e := newTestEnv(t)
	tbl := newFakeTable()
	tbl.set(20, 1, 9, "codex")
	tbl.set(52, 20, 3, "agentbus2")
	c := e.conn(t, "pat")
	s, err := c.Bus.Open(context.Background(), bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	// A primary file of another harness whose server is dead: not ours.
	if err := sessionstate.Write(e.DataDir, sessionstate.File{
		Session: s.ID, Identity: "pat", Profile: "default", DisplayName: s.DisplayName, ResumeToken: s.ResumeToken,
		HarnessPID: 20, HarnessStart: 9, MCPPID: 99, Primary: true, OpenedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	r := startServer(t, e, tbl, "claude-code")
	got := r.must(t, "register", nil)
	if got["resumed"] != false || str(got, "session") == s.ID {
		t.Fatalf("resumed another harness's session: %v", got)
	}
	// A second MCP server under the same harness while the first is alive
	// starts its own primary session.
	r2 := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { o.Self = 43 })
	got2 := r2.must(t, "register", nil)
	if got2["resumed"] != false || str(got2, "session") == str(got, "session") {
		t.Fatalf("second server: %v", got2)
	}
	f1, _ := sessionstate.Read(e.DataDir, str(got, "session"))
	f2, _ := sessionstate.Read(e.DataDir, str(got2, "session"))
	if !f1.Primary || !f2.Primary {
		t.Errorf("each server marks its own first hintless session primary: %+v %+v", f1, f2)
	}
}

func TestRegisterResumesEarliestOfSeveralDeadPrimaries(t *testing.T) {
	e := newTestEnv(t)
	tbl := newFakeTable()
	c := e.conn(t, "pat")
	ctx := context.Background()
	var ids [2]bus.Session
	for i := range ids {
		s, err := c.Bus.Open(ctx, bus.OpenSessionInput{Hint: []string{"a", "b"}[i]})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = s
	}
	write := func(s bus.Session, opened int64, mcp int) {
		if err := sessionstate.Write(e.DataDir, sessionstate.File{
			Session: s.ID, Identity: "pat", Profile: "default", DisplayName: s.DisplayName, ResumeToken: s.ResumeToken,
			HarnessPID: 10, HarnessStart: 5, MCPPID: mcp, Primary: true, OpenedAt: opened,
		}); err != nil {
			t.Fatal(err)
		}
	}
	write(ids[1], 200, 98) // written first, opened later
	write(ids[0], 100, 99)
	r := startServer(t, e, tbl, "claude-code")
	got := r.must(t, "register", nil)
	if got["resumed"] != true || str(got, "session") != ids[0].ID {
		t.Fatalf("want the earliest opened_at (%s), got %v", ids[0].ID, got)
	}
	f, _ := sessionstate.Read(e.DataDir, ids[0].ID)
	if f.OpenedAt != 100 {
		t.Errorf("opened_at changed on resume: %+v", f)
	}
}

func TestRegisterWithResumeTokenAndStaleFiles(t *testing.T) {
	e := newTestEnv(t)
	tbl := newFakeTable()
	c := e.conn(t, "pat")
	ctx := context.Background()
	s, err := c.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	r := startServer(t, e, tbl, "claude-code")
	got := r.must(t, "register", map[string]any{"resume_token": s.ResumeToken})
	if got["resumed"] != true || str(got, "session") != s.ID {
		t.Fatalf("explicit resume: %v", got)
	}
	if _, err := sessionstate.Read(e.DataDir, s.ID); err != nil {
		t.Errorf("no state file for the explicitly resumed session: %v", err)
	}
	r.mustFail(t, "register", map[string]any{"resume_token": "short"}, bus.CodeValidation)

	// A primary file whose session was closed elsewhere is discarded and a
	// fresh session is opened in its place.
	closed, err := c.Bus.Open(ctx, bus.OpenSessionInput{Hint: "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionstate.Write(e.DataDir, sessionstate.File{
		Session: closed.ID, Identity: "pat", Profile: "default", DisplayName: closed.DisplayName, ResumeToken: closed.ResumeToken,
		HarnessPID: 20, HarnessStart: 9, MCPPID: 99, Primary: true, OpenedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Bus.Close(ctx, bus.CloseSessionInput{Session: closed.ID}); err != nil {
		t.Fatal(err)
	}
	tbl.set(20, 1, 9, "codex")
	tbl.set(53, 20, 4, "agentbus2")
	r2 := startServer(t, e, tbl, "codex", func(o *mcpserver.Options) { o.Self = 53 })
	got2 := r2.must(t, "register", nil)
	if got2["resumed"] != false || str(got2, "session") == closed.ID {
		t.Fatalf("register over a closed primary: %v", got2)
	}
	if _, err := sessionstate.Read(e.DataDir, closed.ID); err == nil {
		t.Errorf("the closed session's file was kept")
	}
	if f, err := sessionstate.Read(e.DataDir, str(got2, "session")); err != nil || !f.Primary || f.HarnessPID != 20 {
		t.Errorf("new primary file: %+v, %v", f, err)
	}
}

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestRegisterPastRetention(t *testing.T) {
	e := newTestEnv(t)
	tbl := newFakeTable()
	clk := &clock{t: time.Now()}
	cfg := pinned()
	cfg.SessionRetentionHours = 1
	tune := func(o *client.Options) { o.Config = cfg; o.Now = clk.now }
	first := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { tune(&o.Client) })
	sid := str(first.must(t, "register", nil), "session")
	tbl.kill(42)

	clk.advance(2 * time.Hour)
	c := e.conn(t, "pat", tune)
	maintenance.New(c.Core).Tick(context.Background())

	second := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { tune(&o.Client); o.Self = 43 })
	got := second.must(t, "register", nil)
	if got["resumed"] != false || str(got, "session") == sid {
		t.Fatalf("register past retention: %v", got)
	}
	files, err := sessionstate.List(e.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Session != str(got, "session") || !files[0].Primary {
		t.Fatalf("state files after the replacement: %+v", files)
	}
}

func TestSessionLapsedResumesOnce(t *testing.T) {
	e := newTestEnv(t)
	tbl := newFakeTable()
	clk := &clock{t: time.Now()}
	cfg := pinned()
	tune := func(o *client.Options) { o.Config = cfg; o.Now = clk.now }
	r := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { tune(&o.Client) })
	sam := e.conn(t, "sam", tune)
	ctx := context.Background()

	sid := str(r.must(t, "register", nil), "session")
	r.must(t, "subscribe", map[string]any{"channel": "general"})
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "m1"}); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Duration(cfg.SessionLeaseSeconds+cfg.LeaseSkewSeconds+60) * time.Second)

	// The bus alone answers session_lapsed...
	pat := e.conn(t, "pat", tune)
	if _, err := pat.Bus.Receive(ctx, bus.ReceiveInput{Session: sid}); bus.CodeOf(err) != bus.CodeSessionLapsed {
		t.Fatalf("direct receive on a lapsed session: %v", err)
	}
	// ...and the tool resumes with the stored token and receives.
	got := r.must(t, "receive", map[string]any{"session": sid})
	if len(arr(got, "messages")) != 1 {
		t.Fatalf("receive after a lapse: %v", got)
	}

	// send resumes too.
	clk.advance(time.Duration(cfg.SessionLeaseSeconds+cfg.LeaseSkewSeconds+60) * time.Second)
	r.must(t, "send", map[string]any{"session": sid, "channel": "general", "content": "after"})

	// A closed session is surfaced, not resumed.
	if err := pat.Bus.Close(ctx, bus.CloseSessionInput{Session: sid}); err != nil {
		t.Fatal(err)
	}
	r.mustFail(t, "receive", map[string]any{"session": sid}, bus.CodeSessionClosed)
	r.mustFail(t, "send", map[string]any{"session": sid, "channel": "general", "content": "x"}, bus.CodeSessionClosed)
	// An unknown session id is the bus's not_found.
	r.mustFail(t, "receive", map[string]any{"session": "nosuchsession"}, bus.CodeNotFound)
}

func TestSessionOfAnEarlierServerIsFoundFromItsFile(t *testing.T) {
	e := newTestEnv(t)
	tbl := newFakeTable()
	clk := &clock{t: time.Now()}
	cfg := pinned()
	tune := func(o *client.Options) { o.Config = cfg; o.Now = clk.now }
	first := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { tune(&o.Client) })
	sid := str(first.must(t, "register", nil), "session")
	tbl.kill(42)
	clk.advance(time.Duration(cfg.SessionLeaseSeconds+cfg.LeaseSkewSeconds+60) * time.Second)

	// The model still holds the id; the restarted server has not registered.
	second := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { tune(&o.Client); o.Self = 43 })
	got := second.must(t, "receive", map[string]any{"session": sid})
	if _, ok := got["messages"]; !ok {
		t.Fatalf("receive: %v", got)
	}
}
