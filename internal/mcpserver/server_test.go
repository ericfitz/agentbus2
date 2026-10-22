package mcpserver_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/mcpserver"
	"github.com/ericfitz/agentbus2/internal/procs"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

// pinned is core.Defaults with the wake merge window off, so a wake does not
// wait two seconds for more messages.
func pinned() *core.Config {
	c := core.Defaults()
	c.WatchMergeSeconds = 0
	return &c
}

// testEnv is a temp HOME, config dir and data dir with a local profile
// `default` whose identity is pat.
type testEnv struct {
	Home, ConfigDir, DataDir string
	Environ                  []string
	Config                   profile.Config
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	e := &testEnv{Home: root + "/home", ConfigDir: root + "/config", DataDir: root + "/data"}
	for _, d := range []string{e.Home, e.ConfigDir, e.DataDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.Environ = []string{"HOME=" + e.Home, "AGENTBUS2_CONFIG_DIR=" + e.ConfigDir, "AGENTBUS2_DATA_DIR=" + e.DataDir, "PATH=" + os.Getenv("PATH")}
	e.Config = profile.Config{Path: e.ConfigDir + "/config.json", Profiles: map[string]profile.Profile{
		"default": {Name: "default", Mode: profile.ModeLocal, Identity: "pat"},
	}}
	if err := e.Config.Save(); err != nil {
		t.Fatal(err)
	}
	return e
}

// clientOptions are the client.Options every server and conn in a test use.
func (e *testEnv) clientOptions() client.Options {
	return client.Options{Environ: e.Environ, Cwd: e.Home, PollInterval: 50 * time.Millisecond, Config: pinned()}
}

// conn opens the default profile as identity, closed in t.Cleanup.
func (e *testEnv) conn(t *testing.T, identity string, tune ...func(*client.Options)) *client.Conn {
	t.Helper()
	o := e.clientOptions()
	o.Identity = identity
	for _, f := range tune {
		f(&o)
	}
	c, err := client.Open(context.Background(), o)
	if err != nil {
		t.Fatalf("open as %s: %v", identity, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

type fakeProc struct {
	ppid  int
	start int64
	comm  string
}

// fakeTable is a procs.Table over a map; an unknown pid answers procs.ErrGone.
type fakeTable struct {
	mu     sync.Mutex
	procs  map[int]fakeProc
	errAll error
}

func newFakeTable() *fakeTable {
	t := &fakeTable{procs: map[int]fakeProc{}}
	t.set(10, 1, 5, "claude")
	t.set(42, 10, 7, "agentbus2")
	t.set(43, 10, 8, "agentbus2")
	t.set(44, 10, 9, "agentbus2")
	return t
}

func (f *fakeTable) set(pid, ppid int, start int64, comm string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs[pid] = fakeProc{ppid, start, comm}
}

func (f *fakeTable) kill(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.procs, pid)
}

func (f *fakeTable) get(pid int) (fakeProc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errAll != nil {
		return fakeProc{}, f.errAll
	}
	p, ok := f.procs[pid]
	if !ok {
		return fakeProc{}, procs.ErrGone
	}
	return p, nil
}

func (f *fakeTable) Parent(pid int) (int, error)  { p, err := f.get(pid); return p.ppid, err }
func (f *fakeTable) Comm(pid int) (string, error) { p, err := f.get(pid); return p.comm, err }
func (f *fakeTable) StartTime(pid int) (int64, error) {
	p, err := f.get(pid)
	return p.start, err
}

// running is one in-process MCP server with a connected SDK client.
type running struct {
	Opts  mcpserver.Options
	Close func() error
	CS    *mcp.ClientSession
}

// startServer builds a server for env (Self 42 under the fake harness 10 unless
// tune changes it) and connects an in-memory SDK client named clientName.
func startServer(t *testing.T, e *testEnv, tbl *fakeTable, clientName string, tune ...func(*mcpserver.Options)) *running {
	t.Helper()
	o := mcpserver.Options{Client: e.clientOptions(), Table: tbl, Self: 42}
	for _, f := range tune {
		f(&o)
	}
	ctx := context.Background()
	srv, closeFn, err := mcpserver.NewServer(ctx, o)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "0.1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &running{Opts: o, Close: closeFn, CS: cs}
	// closeFn closes the server's Conn and its SQLite file, which Windows
	// would otherwise keep t.TempDir's RemoveAll from deleting; closing a
	// server the test already closed is a no-op.
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close(); _ = closeFn() })
	return r
}

// call invokes a tool and returns the decoded JSON object on success, or the
// decoded bus.Error on a tool failure.
func (r *running) call(t *testing.T, name string, args map[string]any) (map[string]any, *bus.Error) {
	t.Helper()
	res, err := r.CS.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport error %v", name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s: %d content items", name, len(res.Content))
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError {
		var be bus.Error
		if err := json.Unmarshal([]byte(text), &be); err != nil || be.Code == "" {
			t.Fatalf("%s: failure is not a bus.Error envelope: %q", name, text)
		}
		return nil, &be
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s: result is not a JSON object: %q", name, text)
	}
	return out, nil
}

func (r *running) must(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	out, be := r.call(t, name, args)
	if be != nil {
		t.Fatalf("%s: %v", name, be)
	}
	return out
}

func (r *running) failure(t *testing.T, name string, args map[string]any, code bus.Code) *bus.Error {
	t.Helper()
	out, be := r.call(t, name, args)
	if be == nil {
		t.Fatalf("%s: want %s, got result %v", name, code, out)
	}
	if be.Code != code {
		t.Fatalf("%s: want code %s, got %v", name, code, be)
	}
	return be
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func arr(m map[string]any, k string) []any { a, _ := m[k].([]any); return a }

func TestToolListIsExact(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	res, err := r.CS.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"register", "close_session", "create_channel", "list_channels", "delete_channel", "rename_channel",
		"get_acl", "set_acl", "subscribe", "unsubscribe", "send", "receive", "history", "search",
		"get_memory", "edit_memory", "delete_memory", "discover",
		"task_create", "task_claim", "task_release", "task_update", "task_get", "task_list",
	}
	got := map[string]bool{}
	for _, tl := range res.Tools {
		if got[tl.Name] {
			t.Errorf("tool %s listed twice", tl.Name)
		}
		got[tl.Name] = true
		for _, bad := range []string{"dm/", "persistent", "tasks/"} {
			if strings.Contains(tl.Description, bad) {
				t.Errorf("tool %s description mentions %q", tl.Name, bad)
			}
		}
		if tl.Description == "" {
			t.Errorf("tool %s has no description", tl.Name)
		}
	}
	if len(want) != 24 {
		t.Fatalf("test table has %d names", len(want))
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("missing tool %s", n)
		}
		delete(got, n)
	}
	for n := range got {
		t.Errorf("unexpected tool %s", n)
	}
}

var snake = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// walkSchema checks every property of schema (recursively through properties,
// items and additionalProperties) and records the names found at the top level.
func walkSchema(t *testing.T, where string, s map[string]any) {
	t.Helper()
	if props, ok := s["properties"].(map[string]any); ok {
		for name, p := range props {
			pm, _ := p.(map[string]any)
			if !snake.MatchString(name) {
				t.Errorf("%s: property %q is not snake_case", where, name)
			}
			if d, _ := pm["description"].(string); d == "" {
				t.Errorf("%s: property %q has no description", where, name)
			}
			walkSchema(t, where+"."+name, pm)
		}
	}
	if items, ok := s["items"].(map[string]any); ok {
		walkSchema(t, where+"[]", items)
	}
	if ap, ok := s["additionalProperties"].(map[string]any); ok {
		walkSchema(t, where+"{}", ap)
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		if list, ok := s[k].([]any); ok {
			for _, v := range list {
				if vm, ok := v.(map[string]any); ok {
					walkSchema(t, where, vm)
				}
			}
		}
	}
}

func TestToolSchemasAreSnakeCase(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	res, err := r.CS.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	top := map[string]map[string]bool{}
	for _, tl := range res.Tools {
		raw, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var s map[string]any
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		walkSchema(t, tl.Name, s)
		top[tl.Name] = map[string]bool{}
		if props, ok := s["properties"].(map[string]any); ok {
			for n := range props {
				top[tl.Name][n] = true
			}
		}
	}
	for tool, members := range map[string][]string{
		"send":           {"reply_to", "idempotency_key", "channel", "content"},
		"receive":        {"channels", "wait_seconds", "include_own", "session", "ack"},
		"register":       {"cursors", "hint", "resume_token"},
		"task_update":    {"task_id", "add_blocked_by", "leased_until"},
		"set_acl":        {"entries", "channel"},
		"create_channel": {"acl", "kind", "idempotency_key"},
	} {
		for _, m := range members {
			if !top[tool][m] {
				t.Errorf("%s has no %s member (have %v)", tool, m, top[tool])
			}
		}
	}
}

func TestRegisterOpensSession(t *testing.T) {
	e := newTestEnv(t)
	tbl := newFakeTable()
	r := startServer(t, e, tbl, "test-client")

	reg := r.must(t, "register", nil)
	if str(reg, "display_name") != "pat" || str(reg, "identity") != "pat" {
		t.Fatalf("register: %v", reg)
	}
	if n := len(str(reg, "resume_token")); n != 43 {
		t.Errorf("resume_token has %d characters", n)
	}
	if reg["resumed"] != false {
		t.Errorf("resumed = %v", reg["resumed"])
	}
	for _, k := range []string{"pending", "rejected_cursors", "others"} {
		if _, ok := reg[k].([]any); !ok {
			t.Errorf("%s is %T, want an array", k, reg[k])
		}
	}
	f, err := sessionstate.Read(e.DataDir, str(reg, "session"))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Primary || f.MCPPID != 42 || f.HarnessClient != "test-client" || f.HarnessClientVersion != "0.1" ||
		f.HarnessPID != 10 || f.HarnessStart != 5 || f.Identity != "pat" || f.Profile != "default" ||
		f.DisplayName != "pat" || f.ResumeToken != str(reg, "resume_token") || f.OpenedAt == 0 {
		t.Fatalf("state file: %+v", f)
	}

	reg2 := r.must(t, "register", map[string]any{"hint": "impl"})
	if str(reg2, "display_name") != "pat/impl" {
		t.Fatalf("hinted register: %v", reg2)
	}
	f2, err := sessionstate.Read(e.DataDir, str(reg2, "session"))
	if err != nil {
		t.Fatal(err)
	}
	if f2.Primary {
		t.Errorf("a hinted session is not primary: %+v", f2)
	}
	others := arr(reg2, "others")
	found := false
	for _, o := range others {
		if str(o.(map[string]any), "name") == "pat" {
			found = true
		}
	}
	if !found {
		t.Errorf("others does not list pat: %v", others)
	}

	// A second register without a hint is a new session, and not primary.
	reg3 := r.must(t, "register", nil)
	if str(reg3, "session") == str(reg, "session") {
		t.Fatalf("second register without a hint reused the session")
	}
	f3, _ := sessionstate.Read(e.DataDir, str(reg3, "session"))
	if f3.Primary {
		t.Errorf("only the first hintless session is primary: %+v", f3)
	}

	// Cursors: subscribe pat to general, then offer one ahead of the channel
	// and one on a channel that does not exist.
	r.must(t, "subscribe", map[string]any{"channel": "general"})
	reg4 := r.must(t, "register", map[string]any{"hint": "cur", "cursors": []any{map[string]any{"channel": "general", "seq": 999999}}})
	rej := arr(reg4, "rejected_cursors")
	if len(rej) != 1 || str(rej[0].(map[string]any), "channel") != "general" || str(rej[0].(map[string]any), "reason") != "ahead" {
		t.Errorf("rejected_cursors: %v", rej)
	}
	reg5 := r.must(t, "register", map[string]any{"hint": "cur2", "cursors": []any{map[string]any{"channel": "nosuch", "seq": 1}}})
	rej = arr(reg5, "rejected_cursors")
	if len(rej) != 1 || str(rej[0].(map[string]any), "channel") != "nosuch" || str(rej[0].(map[string]any), "reason") != "unsubscribed" {
		t.Errorf("rejected_cursors for an unknown channel: %v", rej)
	}
	// An unknown hint shape is rejected.
	r.mustFail(t, "register", map[string]any{"hint": "a/b"}, bus.CodeValidation)
}

func TestReceiveAckRoundTrip(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	sam := e.conn(t, "sam")
	ctx := context.Background()

	reg := r.must(t, "register", nil)
	sid := str(reg, "session")
	r.must(t, "subscribe", map[string]any{"channel": "general"})
	r.must(t, "create_channel", map[string]any{"name": "dev"})
	r.must(t, "subscribe", map[string]any{"channel": "dev"})

	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	got := r.must(t, "receive", map[string]any{"session": sid})
	msgs := arr(got, "messages")
	if len(msgs) != 1 || str(msgs[0].(map[string]any), "content") != "hello" {
		t.Fatalf("receive: %v", got)
	}
	batch := str(got, "batch")
	if batch == "" {
		t.Fatalf("no batch token: %v", got)
	}
	got = r.must(t, "receive", map[string]any{"session": sid, "ack": batch})
	if len(arr(got, "messages")) != 0 || got["ack_ignored"] != false {
		t.Fatalf("ack round trip: %v", got)
	}

	// idempotency_key on send: twice, the same seq.
	a := r.must(t, "send", map[string]any{"session": sid, "channel": "general", "content": "x", "idempotency_key": "k1"})
	b := r.must(t, "send", map[string]any{"session": sid, "channel": "general", "content": "x", "idempotency_key": "k1"})
	if a["seq"] != b["seq"] {
		t.Fatalf("idempotent send: %v vs %v", a, b)
	}

	// The channels filter reaches bus.ReceiveInput.Channels.
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "later"}); err != nil {
		t.Fatal(err)
	}
	got = r.must(t, "receive", map[string]any{"session": sid, "channels": []any{"dev"}})
	if len(arr(got, "messages")) != 0 {
		t.Fatalf("channels=[dev] returned %v", got)
	}
	got = r.must(t, "receive", map[string]any{"session": sid, "channels": []any{"general"}})
	if len(arr(got, "messages")) != 1 {
		t.Fatalf("channels=[general] returned %v", got)
	}
	// receive requires a session.
	r.mustFail(t, "receive", map[string]any{}, bus.CodeValidation)
}

func TestReceiveWaitSecondsReplacesWatch(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	sam := e.conn(t, "sam")
	ctx := context.Background()

	sid := str(r.must(t, "register", nil), "session")
	r.must(t, "subscribe", map[string]any{"channel": "general"})

	// A message arriving during the wait is returned within a second of its send.
	var sentAt time.Time
	go func() {
		time.Sleep(300 * time.Millisecond)
		sentAt = time.Now()
		_, _ = sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "wake"})
	}()
	got := r.must(t, "receive", map[string]any{"session": sid, "wait_seconds": 3})
	done := time.Now()
	if len(arr(got, "messages")) != 1 {
		t.Fatalf("wait_seconds receive: %v", got)
	}
	if d := done.Sub(sentAt); d > time.Second {
		t.Errorf("message returned %v after the send", d)
	}

	// Nothing pending and nothing arriving: an empty result after the wait.
	start := time.Now()
	got = r.must(t, "receive", map[string]any{"session": sid, "ack": str(got, "batch"), "wait_seconds": 0.3})
	if len(arr(got, "messages")) != 0 || time.Since(start) < 250*time.Millisecond {
		t.Errorf("empty wait: %v after %v", got, time.Since(start))
	}

	// A watch armed through a second Conn is replaced by the wait.
	pat2 := e.conn(t, "pat")
	type wres struct {
		err error
	}
	ch := make(chan wres, 1)
	go func() {
		_, err := pat2.Bus.Watch(ctx, bus.WatchInput{Session: sid, Timeout: 10 * time.Second})
		ch <- wres{err}
	}()
	time.Sleep(300 * time.Millisecond)
	r.must(t, "receive", map[string]any{"session": sid, "wait_seconds": 1})
	select {
	case w := <-ch:
		if bus.CodeOf(w.err) != bus.CodeReplaced {
			t.Fatalf("the armed watch ended with %v, want replaced", w.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the armed watch was not replaced")
	}
	r.mustFail(t, "receive", map[string]any{"session": sid, "wait_seconds": -1}, bus.CodeValidation)
}

func TestIdempotencyKeyReachesTheHeader(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	r.must(t, "register", nil)

	// A replay of create_channel with the same key and name succeeds; without
	// the key the second create is a conflict.
	r.must(t, "create_channel", map[string]any{"name": "keyed", "idempotency_key": "ck1"})
	r.must(t, "create_channel", map[string]any{"name": "keyed", "idempotency_key": "ck1"})
	r.mustFail(t, "create_channel", map[string]any{"name": "keyed"}, bus.CodeConflict)

	// delete_memory with a key replays too.
	r.must(t, "create_channel", map[string]any{"name": "mem", "kind": "memory"})
	sent := r.must(t, "send", map[string]any{"channel": "mem", "content": "remember"})
	id := sent["memory_id"]
	if id == nil {
		t.Fatalf("send on a memory channel: %v", sent)
	}
	r.must(t, "delete_memory", map[string]any{"id": id, "idempotency_key": "dk1"})
	r.must(t, "delete_memory", map[string]any{"id": id, "idempotency_key": "dk1"})
}

func TestSchemaErrorsWrapped(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	be := r.failure(t, "send", map[string]any{"channel": "general", "content": 5}, bus.CodeValidation)
	if be.Message == "" || be.Retryable {
		t.Errorf("envelope: %+v", be)
	}
	r.mustFail(t, "send", map[string]any{"channel": "general"}, bus.CodeValidation) // content missing
	// An unknown argument is refused by the schema, in the same envelope;
	// reads take no idempotency_key.
	for name, args := range map[string]map[string]any{
		"send":          {"channel": "general", "content": "x", "bogus": 1},
		"register":      {"name": "v1-argument"},
		"get_acl":       {"name": "general", "idempotency_key": "k"},
		"history":       {"channel": "general", "idempotency_key": "k"},
		"close_session": {"session": "s", "persistent": true},
	} {
		be := r.failure(t, name, args, bus.CodeValidation)
		if !strings.Contains(be.Message, "additional properties") {
			t.Errorf("%s: %q", name, be.Message)
		}
	}
}

func TestToolsReportUnresolvedIdentity(t *testing.T) {
	e := newTestEnv(t)
	e.writeNoIdentity(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	for name, args := range map[string]map[string]any{
		"register":      nil,
		"list_channels": nil,
		"discover":      nil,
		"send":          {"channel": "general", "content": "x"},
		"receive":       {"session": "abc"},
		"task_list":     {"channel": "t"},
		"get_memory":    {"id": 1},
	} {
		be := r.failure(t, name, args, bus.CodeValidation)
		if !strings.Contains(be.Message, "identity is required") || !strings.Contains(be.Message, "-identity") ||
			!strings.Contains(be.Message, "AGENTBUS2_IDENTITY") || !strings.Contains(be.Message, "profile") {
			t.Errorf("%s: message does not name the three ways: %q", name, be.Message)
		}
	}
	// The server is up: fixing the profile makes the next call work.
	e.Config.Profiles["default"] = profile.Profile{Name: "default", Mode: profile.ModeLocal, Identity: "pat"}
	if err := e.Config.Save(); err != nil {
		t.Fatal(err)
	}
	r.must(t, "register", nil)
}

func (e *testEnv) writeNoIdentity(t *testing.T) {
	t.Helper()
	e.Config.Profiles["default"] = profile.Profile{Name: "default", Mode: profile.ModeLocal}
	if err := e.Config.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestACLToolsRoundTrip(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	sam := e.conn(t, "sam")
	ctx := context.Background()
	r.must(t, "register", nil)

	r.must(t, "create_channel", map[string]any{"name": "private", "acl": []any{
		map[string]any{"identity": "pat", "permissions": []any{"read", "write", "admin"}},
	}})
	if _, err := sam.Bus.History(ctx, bus.HistoryInput{Channel: "private"}); bus.CodeOf(err) == "" {
		t.Fatalf("sam read a channel whose ACL names only pat (err %v)", err)
	}
	got := r.must(t, "get_acl", map[string]any{"name": "private"})
	entries := arr(got, "entries")
	if len(entries) != 1 || str(entries[0].(map[string]any), "identity") != "pat" {
		t.Fatalf("get_acl: %v", got)
	}
	r.must(t, "set_acl", map[string]any{"channel": "private", "entries": []any{
		map[string]any{"identity": "pat", "permissions": []any{"read", "write", "admin"}},
		map[string]any{"identity": "sam", "permissions": []any{"read"}},
	}})
	if _, err := sam.Bus.History(ctx, bus.HistoryInput{Channel: "private"}); err != nil {
		t.Fatalf("sam cannot read after set_acl: %v", err)
	}
	// Unrecognized enum values are rejected.
	r.mustFail(t, "set_acl", map[string]any{"channel": "private", "entries": []any{
		map[string]any{"identity": "sam", "permissions": []any{"execute"}},
	}}, bus.CodeValidation)
	r.mustFail(t, "create_channel", map[string]any{"name": "weird", "kind": "bogus"}, bus.CodeValidation)
	r.mustFail(t, "subscribe", map[string]any{"channel": "general", "tags": []any{"a"}}, bus.CodeValidation)
	r.mustFail(t, "subscribe", map[string]any{}, bus.CodeValidation)
	r.mustFail(t, "search", map[string]any{"query": "x", "mode": "bogus"}, bus.CodeValidation)

	// rename, list and delete.
	r.must(t, "rename_channel", map[string]any{"name": "private", "new_name": "private2"})
	list := r.must(t, "list_channels", nil)
	names := map[string]bool{}
	for _, c := range arr(list, "channels") {
		names[str(c.(map[string]any), "name")] = true
	}
	if !names["private2"] || names["private"] || !names["general"] {
		t.Errorf("list_channels: %v", names)
	}
	r.must(t, "delete_channel", map[string]any{"name": "private2"})
}

func TestCloseSessionRemovesStateFile(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	sid := str(r.must(t, "register", nil), "session")
	p := filepath.Join(sessionstate.Dir(e.DataDir), sid+".json")
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	r.must(t, "close_session", map[string]any{"session": sid})
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("state file after close_session: %v", err)
	}
	// A second close is idempotent on the bus and leaves no file behind.
	r.must(t, "close_session", map[string]any{"session": sid})
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("state file after the second close_session: %v", err)
	}
	r.mustFail(t, "close_session", map[string]any{"session": ""}, bus.CodeValidation)
	// The closer leaves other sessions' files in place.
	sid2 := str(r.must(t, "register", map[string]any{"hint": "x"}), "session")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionstate.Read(e.DataDir, sid2); err != nil {
		t.Fatalf("closer removed the state file: %v", err)
	}
}

func TestMemoryTaskAndSearchTools(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	sid := str(r.must(t, "register", nil), "session")

	r.must(t, "create_channel", map[string]any{"name": "mem", "kind": "memory"})
	sent := r.must(t, "send", map[string]any{"session": sid, "channel": "mem", "subject": "s", "content": "alpha beta", "tags": []any{"x"}})
	mid := sent["memory_id"]
	got := r.must(t, "get_memory", map[string]any{"id": mid})
	if str(got, "content") != "alpha beta" {
		t.Fatalf("get_memory: %v", got)
	}
	ed := r.must(t, "edit_memory", map[string]any{"id": mid, "content": "gamma"})
	if ed["revision"] == nil || ed["replaced"] == nil {
		t.Fatalf("edit_memory: %v", ed)
	}
	hist := r.must(t, "history", map[string]any{"channel": "mem"})
	if len(arr(hist, "messages")) == 0 {
		t.Fatalf("history: %v", hist)
	}
	sr := r.must(t, "search", map[string]any{"query": "gamma", "mode": "text"})
	if len(arr(sr, "hits")) == 0 {
		t.Fatalf("search: %v", sr)
	}

	r.must(t, "create_channel", map[string]any{"name": "work", "kind": "tasks"})
	tk := r.must(t, "task_create", map[string]any{"channel": "work", "subject": "do it", "metadata": map[string]any{"k": "v"}})
	id := tk["id"]
	if id == nil {
		t.Fatalf("task_create: %v", tk)
	}
	r.must(t, "task_claim", map[string]any{"task_id": id})
	upd := r.must(t, "task_update", map[string]any{"task_id": id, "status": "completed"})
	if str(upd["task"].(map[string]any), "status") != "completed" {
		t.Fatalf("task_update: %v", upd)
	}
	g := r.must(t, "task_get", map[string]any{"task_id": id})
	if str(g, "status") != "completed" {
		t.Fatalf("task_get: %v", g)
	}
	l := r.must(t, "task_list", map[string]any{"channel": "work"})
	if len(arr(l, "tasks")) != 1 {
		t.Fatalf("task_list: %v", l)
	}
	r.must(t, "task_release", map[string]any{"task_id": id})
	r.mustFail(t, "task_update", map[string]any{"task_id": id, "status": "bogus"}, bus.CodeValidation)

	// discover lists pat with its session.
	d := r.must(t, "discover", nil)
	if len(arr(d, "identities")) == 0 {
		t.Fatalf("discover: %v", d)
	}
}

func (r *running) mustFail(t *testing.T, name string, args map[string]any, code bus.Code) {
	t.Helper()
	_ = r.failure(t, name, args, code)
}

// Post-build decision 10 over the MCP JSON input: an omitted type or metadata
// is kept, an explicit "" or {} clears, if_revision guards.
func TestEditMemoryOmitKeepsEmptyClears(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	sid := str(r.must(t, "register", nil), "session")
	r.must(t, "create_channel", map[string]any{"name": "mem", "kind": "memory"})
	sent := r.must(t, "send", map[string]any{"session": sid, "channel": "mem", "content": "alpha",
		"type": "note", "metadata": map[string]any{"k": "v"}})
	mid := sent["memory_id"]
	get := func() map[string]any { return r.must(t, "get_memory", map[string]any{"id": mid}) }

	r.must(t, "edit_memory", map[string]any{"id": mid, "content": "beta"})
	if g := get(); str(g, "type") != "note" || g["metadata"] == nil || str(g, "content") != "beta" {
		t.Fatalf("omitted type/metadata were not kept: %v", g)
	}
	r.must(t, "edit_memory", map[string]any{"id": mid, "type": ""})
	if g := get(); g["type"] != nil && str(g, "type") != "" || g["metadata"] == nil || str(g, "content") != "beta" {
		t.Fatalf(`"type": "" did not clear only the type: %v`, g)
	}
	r.must(t, "edit_memory", map[string]any{"id": mid, "metadata": map[string]any{}})
	if g := get(); g["metadata"] != nil && len(g["metadata"].(map[string]any)) != 0 || str(g, "content") != "beta" {
		t.Fatalf("metadata {} did not clear: %v", g)
	}
	g := get()
	stale := g["revision"].(float64) - 1
	r.mustFail(t, "edit_memory", map[string]any{"id": mid, "content": "x", "if_revision": stale}, bus.CodeConflict)
}
