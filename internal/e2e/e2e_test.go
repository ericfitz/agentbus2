//go:build !windows

// Package e2e_test is the Milestone 6 exit (spec section 10): the built
// agentbus2 binary, a real `agentbus2 server` over TCP and a separate client
// data directory per simulated host. The server listens on 0.0.0.0, so it
// starts in enrollment mode and no client step can register without a token:
// that is the "another host" condition. Hosts A and B are two HOME
// directories; host C is a third with no session state file.
//
// TestSection24Sequence is declared first and the tests run in file order
// (-shuffle is not used), so its display-name assertions hold: nothing has
// opened a session for pat before it.
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

var (
	binPath string
	fx      *fixture
)

// syncBuf is a bytes.Buffer safe for a writer and a reader on two goroutines.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fixture is the server of step 0 and the registrations of step 1, started
// once in TestMain and stopped after both tests.
type fixture struct {
	root, sqlite  string
	homeA, homeB  string
	homeC         string
	port          string
	listening     string // the whole `listening on` line
	tokens        [3]string
	srv           *exec.Cmd
	srvOut        *syncBuf
	srvErr        *syncBuf
	srvDone       chan struct{}
	srvCode       int
	regPat        result
	regSam        result
	regKimNoToken result
	regKimControl result
}

// result is the outcome of one finished command.
type result struct {
	code        int
	stdout, err string
}

func (f *fixture) serverURL() string { return "http://127.0.0.1:" + f.port }

func environFor(home string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -2
}

// runIn runs the binary to completion under home with stdin.
func runIn(home, stdin string, args ...string) result {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Env = environFor(home)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	return result{code: exitCodeOf(err), stdout: o.String(), err: e.String()}
}

func run(home string, args ...string) result { return runIn(home, "", args...) }

// mustRun is run, failing the test unless the exit code is 0.
func mustRun(t testing.TB, home string, args ...string) result {
	t.Helper()
	r := run(home, args...)
	if r.code != 0 {
		t.Fatalf("agentbus2 %s: exit %d, stdout %q, stderr %q", strings.Join(args, " "), r.code, r.stdout, r.err)
	}
	return r
}

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(m.Run()) // the child of startHelper: no build, no server
	}
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	root, err := os.MkdirTemp("", "agentbus2-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(root) }()
	binPath = filepath.Join(root, "agentbus2")
	build := exec.Command("go", "build", "-trimpath", "-o", binPath, ".")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: go build failed: %v\n%s", err, out)
		return 1
	}
	fx, err = startFixture(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: fixture:", err)
		if fx != nil {
			fx.stopServer()
			fmt.Fprintf(os.Stderr, "server stdout: %s\nserver stderr: %s\n", fx.srvOut.String(), fx.srvErr.String())
		}
		return 1
	}
	code := m.Run()
	if c := fx.stopServer(); c != 0 {
		fmt.Fprintf(os.Stderr, "e2e: the server exited %d after SIGTERM, want 0\nstderr: %s\n", c, fx.srvErr.String())
		code = 1
	}
	return code
}

var (
	listeningRE = regexp.MustCompile(`agentbus2 server listening on (\S+) \((sqlite|postgres), registration (\S+), issuer (\S+), kid (\S+)\)`)
	tokenRE     = regexp.MustCompile(`(?m)^token: (abe_\S+)$`)
)

func startFixture(root string) (*fixture, error) {
	f := &fixture{root: root, sqlite: filepath.Join(root, "server.sqlite"),
		homeA: filepath.Join(root, "home-a"), homeB: filepath.Join(root, "home-b"), homeC: filepath.Join(root, "home-c"),
		srvOut: &syncBuf{}, srvErr: &syncBuf{}, srvDone: make(chan struct{})}
	for _, h := range []string{f.homeA, f.homeB, f.homeC, filepath.Join(root, "home-server")} {
		if err := os.MkdirAll(h, 0o700); err != nil {
			return nil, err
		}
	}
	f.srv = exec.Command(binPath, "server", "-listen", "0.0.0.0:0", "-sqlite", f.sqlite, "-set", "session_retention_hours=1")
	f.srv.Env = environFor(filepath.Join(root, "home-server"))
	f.srv.Stdout, f.srv.Stderr = f.srvOut, f.srvErr
	if err := f.srv.Start(); err != nil {
		return nil, err
	}
	go func() {
		err := f.srv.Wait()
		f.srvCode = exitCodeOf(err)
		close(f.srvDone)
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if m := listeningRE.FindStringSubmatch(f.srvOut.String()); m != nil {
			f.listening = m[0]
			_, port, err := net.SplitHostPort(m[1])
			if err != nil {
				return f, fmt.Errorf("listening address %q: %w", m[1], err)
			}
			f.port = port
			break
		}
		select {
		case <-f.srvDone:
			return f, fmt.Errorf("the server exited %d before listening", f.srvCode)
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return f, errors.New("no listening line within 30 s")
		}
	}
	if !strings.Contains(f.listening, "registration enrollment") {
		return f, fmt.Errorf("the server is not in enrollment mode: %s", f.listening)
	}
	for i := range f.tokens {
		r := run(f.homeA, "server", "admin", "-sqlite", f.sqlite, "enrollment", "create")
		m := tokenRE.FindStringSubmatch(r.stdout)
		if r.code != 0 || m == nil {
			return f, fmt.Errorf("enrollment create: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.err)
		}
		f.tokens[i] = m[1]
	}
	reg := func(home, identity, tok string, withServer bool) result {
		args := []string{"register", "-profile", "lab", "-identity", identity}
		if withServer {
			args = append(args, "-server", f.serverURL())
		}
		if tok != "" {
			args = append(args, "-enrollment-token", tok)
		}
		return run(home, args...)
	}
	f.regPat = reg(f.homeA, "pat", f.tokens[0], true)
	f.regSam = reg(f.homeB, "sam", f.tokens[1], true)
	f.regKimNoToken = reg(f.homeA, "kim", "", false)
	// The control: the same identity registers with a valid token, so the
	// refusal above is the missing token and nothing else.
	f.regKimControl = reg(f.homeC, "kim", f.tokens[2], true)
	return f, nil
}

// stopServer sends SIGTERM and returns the server's exit code (-1: it had to
// be killed). It is safe to call twice.
func (f *fixture) stopServer() int {
	select {
	case <-f.srvDone:
		return f.srvCode
	default:
	}
	_ = f.srv.Process.Signal(syscall.SIGTERM)
	select {
	case <-f.srvDone:
		return f.srvCode
	case <-time.After(30 * time.Second):
		_ = f.srv.Process.Kill()
		<-f.srvDone
		return -1
	}
}

// --- background processes ------------------------------------------------

// proc is an agentbus2 command that is still running.
type proc struct {
	cmd  *exec.Cmd
	out  *syncBuf
	errb *syncBuf
	done chan struct{}
	code int
}

// startProc starts the binary under home. It is killed when t ends, so a long
// running process belongs to the t of the test that outlives its steps.
func startProc(t *testing.T, home string, args ...string) *proc {
	t.Helper()
	p := &proc{out: &syncBuf{}, errb: &syncBuf{}, done: make(chan struct{})}
	p.cmd = exec.Command(binPath, args...)
	p.cmd.Env = environFor(home)
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.errb
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := p.cmd.Wait()
		p.code = exitCodeOf(err)
		close(p.done)
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

// exited waits up to d for the process to end and returns its exit code.
func (p *proc) exited(d time.Duration) (int, bool) {
	select {
	case <-p.done:
		return p.code, true
	case <-time.After(d):
		return 0, false
	}
}

func (p *proc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *proc) signal(s os.Signal) {
	_ = p.cmd.Process.Signal(s)
}

// waitLines waits up to d until p.out holds n complete lines.
func (p *proc) waitLines(n int, d time.Duration) []string {
	deadline := time.Now().Add(d)
	for {
		lines := completeLines(p.out.String())
		if len(lines) >= n || time.Now().After(deadline) {
			return lines
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func completeLines(s string) []string {
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return strings.Split(s[:i], "\n")
	}
	return nil
}

// --- helpers over the commands -------------------------------------------

type sessionInfo struct {
	Session     string `json:"session"`
	DisplayName string `json:"display_name"`
	ResumeToken string `json:"resume_token"`
}

func decode(t testing.TB, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("not JSON (%v): %q", err, s)
	}
}

func openSession(t testing.TB, home, identity string) sessionInfo {
	t.Helper()
	r := mustRun(t, home, "session", "open", "-profile", "lab", "-identity", identity)
	var s sessionInfo
	decode(t, r.stdout, &s)
	if s.Session == "" || s.DisplayName == "" {
		t.Fatalf("session open printed %q", r.stdout)
	}
	return s
}

// sendAs sends from identity on general and returns the seq. Flags precede the
// positional channel and content.
func sendAs(t testing.TB, home, identity, content string, flags ...string) int64 {
	t.Helper()
	args := append([]string{"send", "-profile", "lab", "-identity", identity}, flags...)
	r := mustRun(t, home, append(args, "general", content)...)
	var res struct {
		Seq int64 `json:"seq"`
	}
	decode(t, r.stdout, &res)
	if res.Seq == 0 {
		t.Fatalf("send printed %q", r.stdout)
	}
	return res.Seq
}

type receipt struct {
	Messages []struct {
		Seq     int64  `json:"seq"`
		Subject string `json:"subject"`
		Sender  string `json:"sender"`
	} `json:"messages"`
	Batch      string `json:"batch"`
	AckIgnored bool   `json:"ack_ignored"`
}

func receiveOn(t testing.TB, home, session, ack string) receipt {
	t.Helper()
	args := []string{"receive", "-profile", "lab", "-identity", "pat", "-session", session}
	if ack != "" {
		args = append(args, "-ack", ack)
	}
	r := mustRun(t, home, args...)
	var rc receipt
	decode(t, r.stdout, &rc)
	return rc
}

// drain receives and acknowledges everything pending on session, so a message
// sent by a cell cannot wake a later cell's session.
func drain(t testing.TB, home, session string) {
	t.Helper()
	ack := ""
	for range 10 {
		r := receiveOn(t, home, session, ack)
		if r.Batch == "" {
			return
		}
		ack = r.Batch
	}
	t.Fatalf("session %s: still receiving after 10 batches", session)
}

func dataDir(home string) string { return filepath.Join(home, ".local", "share", "agentbus2") }
func configDir(home string) string {
	return filepath.Join(home, ".config", "agentbus2")
}

// --- TestSection24Sequence -----------------------------------------------

type mcpProc struct {
	cs     *mcp.ClientSession
	cmd    *exec.Cmd
	stderr *syncBuf
}

func spawnMCP(t *testing.T, home string) *mcpProc {
	t.Helper()
	cmd := exec.Command(binPath, "mcp", "-profile", "lab", "-identity", "pat")
	cmd.Env = environFor(home)
	p := &mcpProc{cmd: cmd, stderr: &syncBuf{}}
	cmd.Stderr = p.stderr
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "e2e"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to agentbus2 mcp: %v (stderr %q)", err, p.stderr.String())
	}
	p.cs = cs
	return p
}

// call invokes a tool and decodes the text content into v.
func (p *mcpProc) call(t testing.TB, name string, args map[string]any, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := p.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("%s: no content", name)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s: content is %T", name, res.Content[0])
	}
	if res.IsError {
		t.Fatalf("%s: tool error %s", name, tc.Text)
	}
	if err := json.Unmarshal([]byte(tc.Text), v); err != nil {
		t.Fatalf("%s: not JSON (%v): %q", name, err, tc.Text)
	}
}

type registered struct {
	Session     string `json:"session"`
	DisplayName string `json:"display_name"`
	Resumed     bool   `json:"resumed"`
}

func TestSection24Sequence(t *testing.T) {
	parent := t // long-lived processes belong to the test, not to a step
	homeA, homeB := fx.homeA, fx.homeB
	var (
		s1, s2     sessionInfo
		waiter     *proc
		watcher    *proc
		seq1       int64
		hookLogErr string
	)
	var (
		batch1   string // the batch of step 7, acknowledged in step 8
		mcp2     *mcpProc
		mcpSess  string
		mcpBatch string
		mcpName  string
	)
	steps := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"0-server", func(t *testing.T) {
			if !strings.Contains(fx.listening, "registration enrollment") {
				t.Fatalf("listening line %q lacks 'registration enrollment'", fx.listening)
			}
			if fx.port == "" || fx.port == "0" {
				t.Fatalf("no port in %q", fx.listening)
			}
			for i, tk := range fx.tokens {
				if !strings.HasPrefix(tk, "abe_") {
					t.Fatalf("token %d is %q", i, tk)
				}
			}
		}},
		{"1-register", func(t *testing.T) {
			for name, r := range map[string]result{"pat": fx.regPat, "sam": fx.regSam, "kim with a token": fx.regKimControl} {
				if r.code != 0 {
					t.Fatalf("register %s: exit %d, stdout %q, stderr %q", name, r.code, r.stdout, r.err)
				}
			}
			cfg, err := os.ReadFile(filepath.Join(configDir(homeA), "config.json"))
			if err != nil || !strings.Contains(string(cfg), `"lab"`) {
				t.Fatalf("config.json under host A: %v, %q", err, cfg)
			}
			cred := filepath.Join(configDir(homeA), "credentials", "lab", "pat")
			fi, err := os.Stat(cred)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Fatalf("credential file %s has mode %v, want 0600", cred, fi.Mode().Perm())
			}
			if r := fx.regKimNoToken; r.code != 1 || !strings.Contains(r.err, "forbidden") {
				t.Fatalf("register without a token: exit %d, stderr %q, want 1 and forbidden", r.code, r.err)
			}
			if _, err := os.Stat(filepath.Join(configDir(homeA), "credentials", "lab", "kim")); err == nil {
				t.Fatal("a refused registration left a credential file")
			}
		}},
		{"2-token", func(t *testing.T) {
			r := mustRun(t, homeA, "login", "-profile", "lab", "-identity", "pat")
			if !strings.Contains(r.stdout, "logged in as pat") {
				t.Fatalf("login printed %q", r.stdout)
			}
			if _, err := os.Stat(filepath.Join(dataDir(homeA), "tokens", "lab", "pat.json")); err != nil {
				t.Fatalf("no token cache under host A's data dir: %v", err)
			}
		}},
		{"3-open-session", func(t *testing.T) {
			s1 = openSession(t, homeA, "pat")
			s2 = openSession(t, homeA, "pat")
			if s1.ResumeToken == "" || s2.ResumeToken == "" {
				t.Fatal("no resume token")
			}
			// sessions.DisplayName: <identity>, <identity>-2, ... for hintless
			// sessions, counting the identity's open ones; nothing has opened a
			// pat session before this test.
			if s1.DisplayName != "pat" || s2.DisplayName != "pat-2" {
				t.Fatalf("display names %q and %q, want pat and pat-2", s1.DisplayName, s2.DisplayName)
			}
		}},
		{"4-watch", func(t *testing.T) {
			waiter = startProc(parent, homeA, "wait", "-profile", "lab", "-identity", "pat", "-session", s1.Session, "-timeout", "30s")
			watcher = startProc(parent, homeA, "watch", "-profile", "lab", "-identity", "pat", "-session", s2.Session)
		}},
		{"5-send-from-another-identity", func(t *testing.T) {
			time.Sleep(300 * time.Millisecond)
			seq1 = sendAs(t, homeB, "sam", "hello from sam", "-subject", "e2e", "-key", "e2e-1")
		}},
		{"6-wake", func(t *testing.T) {
			code, ok := waiter.exited(5 * time.Second)
			if !ok || code != 0 {
				t.Fatalf("wait: exited %v with code %d, stdout %q, stderr %q", ok, code, waiter.out.String(), waiter.errb.String())
			}
			want := fmt.Sprintf("{\"seq\":%d,\"count\":1,\"channels\":[\"general\"]}\n", seq1)
			if waiter.out.String() != want {
				t.Fatalf("wait printed %q, want %q", waiter.out.String(), want)
			}
			lines := watcher.waitLines(1, 5*time.Second)
			wantLine := "agentbus2: 1 new message(s) for " + s2.DisplayName + " on general; call receive"
			if len(lines) != 1 || lines[0] != wantLine {
				t.Fatalf("watch printed %q, want exactly %q", watcher.out.String(), wantLine)
			}
			time.Sleep(time.Second)
			if !watcher.running() {
				t.Fatalf("watch exited %d after its wake line: stderr %q", watcher.code, watcher.errb.String())
			}
		}},
		{"7-receive", func(t *testing.T) {
			r := receiveOn(t, homeA, s1.Session, "")
			if len(r.Messages) != 1 || r.Messages[0].Subject != "e2e" || r.Messages[0].Seq != seq1 || r.Messages[0].Sender != "sam" || r.Batch == "" {
				t.Fatalf("receive: %+v", r)
			}
			batch1 = r.Batch
		}},
		{"8-ack", func(t *testing.T) {
			batch := batch1
			if batch == "" {
				t.Fatal("no batch from step 7")
			}
			r := receiveOn(t, homeA, s1.Session, batch)
			if len(r.Messages) != 0 || r.AckIgnored {
				t.Fatalf("receive -ack: %+v, want no messages and ack_ignored false", r)
			}
			// -no-ack here is accepted and has no effect with -session.
			again := mustRun(t, homeA, "receive", "-profile", "lab", "-identity", "pat", "-session", s1.Session, "-no-ack")
			var rc receipt
			decode(t, again.stdout, &rc)
			if len(rc.Messages) != 0 {
				t.Fatalf("a further receive returned %+v: the batch is not consumed", rc)
			}
		}},
		{"9-stale-bearer-refreshes", func(t *testing.T) {
			path := filepath.Join(dataDir(homeA), "tokens", "lab", "pat.json")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var entry map[string]any
			decode(t, string(b), &entry)
			disc, _ := entry["discovery"].(map[string]any)
			issuerURL, _ := disc["issuer"].(string)
			old, _ := entry["access_token"].(string)
			key, err := keys.Generate(keys.EdDSA)
			if err != nil {
				t.Fatal(err)
			}
			iss, err := token.NewIssuer(issuerURL, key, time.Hour, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			forged, _, err := iss.Mint("pat")
			if err != nil {
				t.Fatal(err)
			}
			entry["access_token"] = forged
			nb, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nb, 0o600); err != nil {
				t.Fatal(err)
			}
			mustRun(t, homeA, "channels", "list", "-profile", "lab", "-identity", "pat")
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var e2 map[string]any
			decode(t, string(after), &e2)
			if got, _ := e2["access_token"].(string); got == forged || got == "" {
				t.Fatalf("the cache still holds the forged bearer (old token %q)", old)
			}
		}},
		{"10-mcp-over-stdio", func(t *testing.T) {
			p := spawnMCP(parent, homeA)
			parent.Cleanup(func() { _ = p.cs.Close() })
			var reg registered
			p.call(t, "register", map[string]any{}, &reg)
			if reg.DisplayName != "pat-3" || reg.Resumed {
				t.Fatalf("register: %+v, want pat-3 and not resumed (S and S2 are still open)", reg)
			}
			st, err := sessionstate.Read(dataDir(homeA), reg.Session)
			if err != nil {
				t.Fatal(err)
			}
			if st.HarnessPID != os.Getpid() || st.HarnessClient != "claude-code" {
				t.Fatalf("state file: harness_pid %d (test %d), harness_client %q", st.HarnessPID, os.Getpid(), st.HarnessClient)
			}
			sendAs(t, homeB, "sam", "second from sam", "-subject", "e2e-2", "-key", "e2e-2")
			var rc receipt
			p.call(t, "receive", map[string]any{"session": reg.Session}, &rc)
			if len(rc.Messages) != 1 || rc.Messages[0].Subject != "e2e-2" || rc.Batch == "" {
				t.Fatalf("mcp receive: %+v", rc)
			}
			if err := p.cs.Close(); err != nil {
				t.Logf("closing the first mcp client: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dataDir(homeA), "sessions", reg.Session+".json")); err != nil {
				t.Fatalf("the state file is gone after a clean exit: %v", err)
			}
			// Restart: the same primary session resumes (Decision 79).
			p2 := spawnMCP(parent, homeA)
			parent.Cleanup(func() { _ = p2.cs.Close() })
			var again registered
			p2.call(t, "register", map[string]any{}, &again)
			if !again.Resumed || again.Session != reg.Session {
				t.Fatalf("restart register: %+v, want resumed session %s", again, reg.Session)
			}
			st2, err := sessionstate.Read(dataDir(homeA), reg.Session)
			if err != nil {
				t.Fatal(err)
			}
			if st2.MCPPID != p2.cmd.Process.Pid || st2.MCPPID == st.MCPPID {
				t.Fatalf("state file mcp_pid %d, want the new process %d (old %d)", st2.MCPPID, p2.cmd.Process.Pid, st.MCPPID)
			}
			// Hand the open MCP process and the batch to step 11.
			mcp2, mcpSess, mcpBatch, mcpName = p2, reg.Session, rc.Batch, reg.DisplayName
		}},
		{"11-stop-hook", func(t *testing.T) {
			if mcp2 == nil {
				t.Fatal("no mcp process from step 10")
			}
			sendAs(t, homeB, "sam", "third from sam", "-subject", "e2e-3", "-key", "e2e-3")
			logPath := filepath.Join(dataDir(homeA), "logs", "hook.log")
			var r result
			for attempt := range 2 {
				if attempt > 0 {
					time.Sleep(200 * time.Millisecond)
				}
				r = runIn(homeA, `{"stop_hook_active":false}`, "stop-hook")
				if strings.TrimSpace(r.stdout) != "" {
					break
				}
			}
			if lg, err := os.ReadFile(logPath); err == nil {
				hookLogErr = string(lg)
			}
			var out struct {
				Decision string `json:"decision"`
				Reason   string `json:"reason"`
			}
			if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
				t.Fatalf("stop-hook: exit %d, stdout %q, stderr %q, hook.log %q", r.code, r.stdout, r.err, hookLogErr)
			}
			if r.code != 0 || out.Decision != "block" ||
				!strings.Contains(out.Reason, "1 new message(s) for "+mcpName) || !strings.Contains(out.Reason, "background `agentbus2 wait`") {
				t.Fatalf("stop-hook: exit %d, %+v, hook.log %q", r.code, out, hookLogErr)
			}
			logBefore := hookLogErr
			// Receive and ack through the MCP client; the hook then stays silent.
			var rc receipt
			mcp2.call(t, "receive", map[string]any{"session": mcpSess, "ack": mcpBatch}, &rc)
			if len(rc.Messages) != 1 || rc.Messages[0].Subject != "e2e-3" || rc.Batch == "" {
				t.Fatalf("mcp receive with ack: %+v", rc)
			}
			var last receipt
			mcp2.call(t, "receive", map[string]any{"session": mcpSess, "ack": rc.Batch}, &last)
			if len(last.Messages) != 0 || last.AckIgnored {
				t.Fatalf("mcp ack: %+v", last)
			}
			silent := runIn(homeA, `{"stop_hook_active":false}`, "stop-hook")
			if silent.code != 0 || silent.stdout != "" {
				t.Fatalf("stop-hook after the ack: exit %d, stdout %q", silent.code, silent.stdout)
			}
			lg, _ := os.ReadFile(logPath)
			if string(lg) != logBefore {
				t.Fatalf("the silent hook fell open: hook.log grew from %q to %q", logBefore, lg)
			}
		}},
		{"12-watch-ends-only-by-signal", func(t *testing.T) {
			if !watcher.running() {
				t.Fatalf("watch exited %d before the signal: stderr %q", watcher.code, watcher.errb.String())
			}
			before := watcher.out.String()
			watcher.signal(syscall.SIGTERM)
			code, ok := watcher.exited(5 * time.Second)
			if !ok || code != 3 {
				t.Fatalf("watch after SIGTERM: exited %v with code %d, stderr %q", ok, code, watcher.errb.String())
			}
			if watcher.out.String() != before {
				t.Fatalf("watch printed after the signal: %q, was %q", watcher.out.String(), before)
			}
		}},
	}
	// The steps are subtests in this order. A step that fails ends the test:
	// the later ones depend on its state.
	for _, st := range steps {
		if !t.Run(st.name, st.fn) {
			t.FailNow()
		}
	}
}

// --- TestExitCodeTable ---------------------------------------------------

// cell opens a fresh hintless session for pat, drained and closed when the
// subtest ends (after the processes it started are killed): a fresh session's
// cursor starts at the identity's acked mark, so an unacked message would wake
// every later cell's session.
func cell(t *testing.T) sessionInfo {
	t.Helper()
	s := openSession(t, fx.homeA, "pat")
	t.Cleanup(func() {
		drain(t, fx.homeA, s.Session)
		if r := run(fx.homeA, "session", "close", "-profile", "lab", "-identity", "pat", s.Session); r.code != 0 {
			t.Errorf("closing cell session %s: exit %d, stderr %q", s.Session, r.code, r.err)
		}
	})
	return s
}

var (
	wakeLineRE = regexp.MustCompile(`^agentbus2: 1 new message\(s\) for pat(-\d+)? on general; call receive$`)
	wakeJSONRE = regexp.MustCompile(`^\{"seq":\d+,"count":1,"channels":\["general"\]\}\n$`)
)

func waitArgs(session string, extra ...string) []string {
	return append([]string{"wait", "-profile", "lab", "-identity", "pat", "-session", session}, extra...)
}

func watchArgs(session string) []string {
	return []string{"watch", "-profile", "lab", "-identity", "pat", "-session", session}
}

// expectExit fails unless p ends with code within d.
func expectExit(t *testing.T, what string, p *proc, want int, d time.Duration) {
	t.Helper()
	code, ok := p.exited(d)
	if !ok || code != want {
		t.Fatalf("%s: exited %v with code %d, want %d within %v; stdout %q, stderr %q", what, ok, code, want, d, p.out.String(), p.errb.String())
	}
}

// settle gives a started command time to arm its watch before the test acts on
// it (a signal before the handler is installed would kill it outright).
func settle() { time.Sleep(1500 * time.Millisecond) }

// harnessGone runs `agentbus2 args` under a helper harness and, once ready
// returns (the command is running), closes the helper. It returns the helper
// after the command recorded its exit code.
func harnessGone(t *testing.T, args []string, ready func(h *helper)) *helper {
	t.Helper()
	h := startHelper(t, fx.homeA, args...)
	ready(h)
	if code, ok := h.exitCode(); ok {
		t.Fatalf("the command ended (%s) before its harness did; output %q", code, h.out())
	}
	h.closeStdin()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := h.exitCode(); ok {
			return h
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the command did not end within 8 s of its harness: output %q", h.out())
	return nil
}

func TestExitCodeTable(t *testing.T) {
	home := fx.homeA

	t.Run("wait-0-woke", func(t *testing.T) {
		s := cell(t)
		p := startProc(t, home, waitArgs(s.Session, "-timeout", "30s")...)
		time.Sleep(300 * time.Millisecond)
		sendAs(t, fx.homeB, "sam", "wake", "-subject", "cell")
		expectExit(t, "wait", p, 0, 5*time.Second)
		if !wakeJSONRE.MatchString(p.out.String()) {
			t.Fatalf("wait printed %q", p.out.String())
		}
	})

	t.Run("wait-1-timeout", func(t *testing.T) {
		s := cell(t)
		p := startProc(t, home, waitArgs(s.Session, "-timeout", "1s")...)
		expectExit(t, "wait -timeout 1s", p, 1, 10*time.Second)
		if p.out.String() != "" {
			t.Fatalf("stdout %q, want empty", p.out.String())
		}
	})

	t.Run("wait-2-error", func(t *testing.T) {
		s := cell(t)
		p := startProc(t, home, waitArgs(s.Session, "-timeout", "5s", "-profile", "nosuch")...)
		expectExit(t, "wait -profile nosuch", p, 2, 10*time.Second)
		if !strings.Contains(p.errb.String(), "agentbus2:") || p.out.String() != "" {
			t.Fatalf("stdout %q, stderr %q", p.out.String(), p.errb.String())
		}
		// No -session and no state file for this harness (host C has none).
		r := run(fx.homeC, "wait", "-profile", "lab", "-identity", "kim", "-timeout", "5s")
		if r.code != 2 || !strings.Contains(r.err, "-session") || r.stdout != "" {
			t.Fatalf("wait without -session: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.err)
		}
	})

	t.Run("wait-3-replaced", func(t *testing.T) {
		s := cell(t)
		first := startProc(t, home, waitArgs(s.Session, "-timeout", "30s")...)
		settle()
		second := startProc(t, home, waitArgs(s.Session, "-timeout", "30s")...)
		expectExit(t, "the replaced wait", first, 3, 2*time.Second)
		if first.out.String() != "" || first.errb.String() != "" {
			t.Fatalf("the replaced wait wrote stdout %q, stderr %q", first.out.String(), first.errb.String())
		}
		if !second.running() {
			t.Fatalf("the replacing wait ended: %d, %q", second.code, second.errb.String())
		}
	})

	t.Run("wait-3-signal", func(t *testing.T) {
		s := cell(t)
		p := startProc(t, home, waitArgs(s.Session, "-timeout", "30s")...)
		settle()
		p.signal(syscall.SIGINT)
		expectExit(t, "wait after SIGINT", p, 3, 5*time.Second)
		if p.out.String() != "" {
			t.Fatalf("stdout %q, want empty", p.out.String())
		}
	})

	t.Run("wait-4-harness-gone", func(t *testing.T) {
		s := cell(t)
		// The pre-watcher is replaced once the helper's wait has registered its
		// watch: its exit 3 proves the helper's command is running under the
		// helper before the helper is closed.
		pre := startProc(t, home, waitArgs(s.Session, "-timeout", "30s")...)
		settle()
		h := harnessGone(t, waitArgs(s.Session, "-timeout", "60s"), func(h *helper) {
			expectExit(t, "the pre-watcher", pre, 3, 10*time.Second)
		})
		if code, _ := h.exitCode(); code != "4" || h.out() != "" {
			t.Fatalf("wait after its harness: exit %q, stdout %q, want 4 and empty", code, h.out())
		}
	})

	t.Run("watch-2-error", func(t *testing.T) {
		s := cell(t)
		p := startProc(t, home, append(watchArgs(s.Session), "-profile", "nosuch")...)
		expectExit(t, "watch -profile nosuch", p, 2, 10*time.Second)
		if !strings.Contains(p.errb.String(), "agentbus2:") || p.out.String() != "" {
			t.Fatalf("stdout %q, stderr %q", p.out.String(), p.errb.String())
		}
	})

	t.Run("watch-3-replaced", func(t *testing.T) {
		s := cell(t)
		first := startProc(t, home, watchArgs(s.Session)...)
		settle()
		second := startProc(t, home, watchArgs(s.Session)...)
		expectExit(t, "the replaced watch", first, 3, 2*time.Second)
		if first.out.String() != "" || first.errb.String() != "" {
			t.Fatalf("the replaced watch wrote stdout %q, stderr %q", first.out.String(), first.errb.String())
		}
		if !second.running() {
			t.Fatalf("the replacing watch ended: %d, %q", second.code, second.errb.String())
		}
	})

	t.Run("watch-3-signal", func(t *testing.T) {
		s := cell(t)
		p := startProc(t, home, watchArgs(s.Session)...)
		settle()
		p.signal(syscall.SIGHUP)
		expectExit(t, "watch after SIGHUP", p, 3, 5*time.Second)
		if p.out.String() != "" {
			t.Fatalf("stdout %q, want empty", p.out.String())
		}
	})

	t.Run("watch-4-harness-gone", func(t *testing.T) {
		s := cell(t)
		// Readiness: a message waits before the watch is armed, so its first
		// wake line proves the command found its harness and is looping.
		sendAs(t, fx.homeB, "sam", "ready", "-subject", "cell")
		h := harnessGone(t, watchArgs(s.Session), func(h *helper) {
			deadline := time.Now().Add(10 * time.Second)
			for !strings.HasSuffix(h.out(), "\n") {
				if time.Now().After(deadline) {
					t.Fatal("the watch printed no wake line within 10 s")
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
		code, _ := h.exitCode()
		lines := completeLines(h.out())
		if code != "4" || len(lines) != 1 || !wakeLineRE.MatchString(lines[0]) {
			t.Fatalf("watch after its harness: exit %q, stdout %q, want 4 and the one wake line", code, h.out())
		}
	})

	t.Run("watch-never-0-or-1", func(t *testing.T) {
		s := cell(t)
		sendAs(t, fx.homeB, "sam", "wake", "-subject", "cell")
		p := startProc(t, home, watchArgs(s.Session)...)
		lines := p.waitLines(1, 10*time.Second)
		if len(lines) != 1 || !wakeLineRE.MatchString(lines[0]) {
			t.Fatalf("watch printed %q, want one wake line", p.out.String())
		}
		time.Sleep(6 * time.Second) // watchMinGap (5 s) plus 1 s
		if !p.running() {
			t.Fatalf("watch ended on its own with %d: stderr %q", p.code, p.errb.String())
		}
		p.signal(syscall.SIGTERM)
		expectExit(t, "watch after SIGTERM", p, 3, 5*time.Second)
	})
}
