package mcpserver_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/core/maintenance"
	"github.com/ericfitz/agentbus2/internal/filelock"
	"github.com/ericfitz/agentbus2/internal/mcpserver"
	"github.com/ericfitz/agentbus2/internal/procs"
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
			// A third hintless register in the same server (a /clear) is the
			// primary session again, not a new one.
			third := second.must(t, "register", nil)
			if third["resumed"] != true || str(third, "session") != sid {
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

// A session swept past retention while its MCP server stays up: the bus
// answers not_found (the row is gone), so the tool replaces the state file
// with a fresh session that inherits primary and names it in the error; the
// harness's hooks then find a live session, not the swept one.
func TestSweptSessionIsReplacedOnUse(t *testing.T) {
	for _, tool := range []string{"receive", "send"} {
		t.Run(tool, func(t *testing.T) {
			e := newTestEnv(t)
			tbl := newFakeTable()
			clk := &clock{t: time.Now()}
			cfg := pinned()
			cfg.SessionRetentionHours = 1
			tune := func(o *client.Options) { o.Config = cfg; o.Now = clk.now }
			r := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { tune(&o.Client) })
			sid := str(r.must(t, "register", nil), "session")
			clk.advance(2 * time.Hour)
			c := e.conn(t, "pat", tune)
			maintenance.New(c.Core).Tick(context.Background())

			args := map[string]any{"session": sid}
			if tool == "send" {
				args["channel"], args["content"] = "general", "x"
			}
			be := r.failure(t, tool, args, bus.CodeSessionLapsed)
			files, err := sessionstate.List(e.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 || files[0].Session == sid || !files[0].Primary || files[0].MCPPID != 42 {
				t.Fatalf("state files after the swept session was used: %+v", files)
			}
			nid := files[0].Session
			if !strings.Contains(be.Message, nid) {
				t.Errorf("error does not name the new session %s: %q", nid, be.Message)
			}
			f, found, err := sessionstate.ForHarness(e.DataDir, procs.Ref{Pid: 10, Start: 5})
			if err != nil || !found || f.Session != nid {
				t.Errorf("the harness resolves %+v, %v; want %s", f, err, nid)
			}
			r.must(t, "receive", map[string]any{"session": nid})
			// An id the server holds no file for stays the bus's not_found.
			r.mustFail(t, "receive", map[string]any{"session": "nosuchsession"}, bus.CodeNotFound)
		})
	}
}

// A not_found that is not about the session (a send to a missing channel)
// leaves the session and its file alone.
func TestNotFoundOnALiveSessionIsPassedThrough(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "claude-code")
	sid := str(r.must(t, "register", nil), "session")
	r.mustFail(t, "send", map[string]any{"session": sid, "channel": "nosuch", "content": "x"}, bus.CodeNotFound)
	files, err := sessionstate.List(e.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Session != sid {
		t.Fatalf("state files: %+v", files)
	}
}

// close_session of the primary session frees the primary mark: the next
// register without a hint is the harness's primary session again.
func TestCloseSessionFreesPrimary(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "claude-code")
	sid := str(r.must(t, "register", nil), "session")
	r.must(t, "close_session", map[string]any{"session": sid})
	nid := str(r.must(t, "register", nil), "session")
	f, err := sessionstate.Read(e.DataDir, nid)
	if err != nil || !f.Primary {
		t.Fatalf("register after closing the primary: %+v, %v", f, err)
	}
}

// Overlapping restarts: register runs under the sessions directory lock, so
// a register waits while another process holds it.
func TestRegisterWaitsForTheDirectoryLock(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "claude-code")
	if err := os.MkdirAll(sessionstate.Dir(e.DataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	lf, err := os.OpenFile(filepath.Join(sessionstate.Dir(e.DataDir), ".register.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := filelock.Lock(lf); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.CS.CallTool(context.Background(), &mcp.CallToolParams{Name: "register", Arguments: map[string]any{}})
		done <- err
	}()
	select {
	case err := <-done:
		_ = lf.Close()
		t.Fatalf("register returned while the lock was held (err %v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	_ = lf.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("register did not finish after the lock was released")
	}
}

// Two servers restarting at once under one harness: exactly one resumes the
// dead primary session.
func TestOverlappingRestartsResumeOnce(t *testing.T) {
	e := newTestEnv(t)
	tbl := newFakeTable()
	first := startServer(t, e, tbl, "claude-code")
	sid := str(first.must(t, "register", nil), "session")
	tbl.kill(42)
	a := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { o.Self = 43 })
	b := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { o.Self = 44 })
	var wg sync.WaitGroup
	res := make([]string, 2)
	for i, s := range []*running{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := s.CS.CallTool(context.Background(), &mcp.CallToolParams{Name: "register", Arguments: map[string]any{}})
			if err != nil || out.IsError {
				return
			}
			res[i] = out.Content[0].(*mcp.TextContent).Text
		}()
	}
	wg.Wait()
	n := 0
	for _, text := range res {
		if text == "" {
			t.Fatalf("a register failed: %q", res)
		}
		if strings.HasPrefix(text, `{"session":"`+sid+`"`) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d servers resumed %s: %q", n, sid, res)
	}
}

// close_session of a session swept past retention removes its file and
// opens nothing in its place.
func TestCloseSweptSession(t *testing.T) {
	e := newTestEnv(t)
	clk := &clock{t: time.Now()}
	cfg := pinned()
	cfg.SessionRetentionHours = 1
	tune := func(o *client.Options) { o.Config = cfg; o.Now = clk.now }
	r := startServer(t, e, newFakeTable(), "claude-code", func(o *mcpserver.Options) { tune(&o.Client) })
	sid := str(r.must(t, "register", nil), "session")
	clk.advance(2 * time.Hour)
	c := e.conn(t, "pat", tune)
	maintenance.New(c.Core).Tick(context.Background())
	r.mustFail(t, "close_session", map[string]any{"session": sid}, bus.CodeNotFound)
	files, err := sessionstate.List(e.DataDir)
	if err != nil || len(files) != 0 {
		t.Fatalf("state files after closing a swept session: %+v, %v", files, err)
	}
}

// A /clear in Claude Code calls register again without a hint while the same
// MCP server still holds the pre-clear session: the new conversation takes
// over that primary session (resumed, same id), and the harness keeps exactly
// one primary state file.
func TestHintlessRegisterAgainResumesOwnPrimary(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "claude-code")
	first := r.must(t, "register", nil)
	sid := str(first, "session")
	for i := 0; i < 2; i++ {
		again := r.must(t, "register", nil)
		if again["resumed"] != true || str(again, "session") != sid {
			t.Fatalf("hintless register %d after /clear: %v (first %v)", i+1, again, first)
		}
	}
	// A hinted register is still a new, non-primary session.
	hinted := r.must(t, "register", map[string]any{"hint": "sub"})
	if hinted["resumed"] != false || str(hinted, "session") == sid {
		t.Fatalf("hinted register: %v", hinted)
	}
	all, err := sessionstate.List(e.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	primaries := 0
	for _, f := range all {
		if f.Primary {
			primaries++
			if f.Session != sid {
				t.Errorf("primary is %s, want %s", f.Session, sid)
			}
		}
	}
	if primaries != 1 || len(all) != 2 {
		t.Errorf("want 1 primary of 2 files, got %d of %d: %+v", primaries, len(all), all)
	}
}

// When the held primary was closed elsewhere, a hintless register replaces it
// with a fresh session that is primary.
func TestHintlessRegisterAfterPrimaryClosedElsewhere(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "claude-code")
	sid := str(r.must(t, "register", nil), "session")
	if err := e.conn(t, "pat").Bus.Close(context.Background(), bus.CloseSessionInput{Session: sid}); err != nil {
		t.Fatal(err)
	}
	again := r.must(t, "register", nil)
	if str(again, "session") == sid {
		t.Fatalf("closed session reused: %v", again)
	}
	f, err := sessionstate.Read(e.DataDir, str(again, "session"))
	if err != nil || !f.Primary {
		t.Fatalf("replacement not primary: %+v, %v", f, err)
	}
}
