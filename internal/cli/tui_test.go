package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/server/cmd"
	"github.com/ericfitz/agentbus2/internal/tui"
)

// pinTUI pins the TUI's window size for the test and restores it.
func pinTUI(t *testing.T, w, h int) {
	t.Helper()
	prev := testTUIOptions
	testTUIOptions = func(o *tui.Options) { o.Width, o.Height = w, h }
	t.Cleanup(func() { testTUIOptions = prev })
}

// runningTUI is a RunTUI in a goroutine fed through a pipe.
type runningTUI struct {
	in     *io.PipeWriter
	out    *syncBuf
	stderr *syncBuf
	done   chan int
	cancel context.CancelFunc
}

func startTUI(t *testing.T, e *testEnv, args ...string) *runningTUI {
	t.Helper()
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	r := &runningTUI{in: pw, out: &syncBuf{}, stderr: &syncBuf{}, done: make(chan int, 1), cancel: cancel}
	go func() { r.done <- RunTUI(ctx, args, e.Environ, pr, r.out, r.stderr) }()
	t.Cleanup(func() {
		cancel()
		_ = pw.Close()
		select {
		case <-r.done:
		case <-time.After(15 * time.Second):
			t.Error("RunTUI did not return")
		}
	})
	return r
}

// quit sends ctrl+c and returns RunTUI's exit code.
func (r *runningTUI) quit(t *testing.T) int {
	t.Helper()
	go func() { _, _ = r.in.Write([]byte("\x03")) }()
	select {
	case code := <-r.done:
		r.done <- code // the cleanup also waits for it
		return code
	case <-time.After(10 * time.Second):
		t.Fatalf("RunTUI did not exit on ctrl+c (stderr %q)", r.stderr.String())
		return -1
	}
}

// waitUntil polls cond until it holds or the deadline passes.
func waitUntil(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// tuiSessionUp waits until the identities list shows a session named want.
func tuiSessionUp(t *testing.T, b bus.Bus, want string) {
	t.Helper()
	waitUntil(t, "session "+want, 5*time.Second, func() bool {
		ids, err := b.Identities(context.Background())
		if err != nil {
			return false
		}
		for _, id := range ids {
			for _, s := range id.Sessions {
				if s.DisplayName == want {
					return true
				}
			}
		}
		return false
	})
}

// driveSend runs the shared script: wait for the TUI's session and its first
// frame, type a message into general, expect sam to receive it from
// eric/tui, then quit.
func driveSend(t *testing.T, r *runningTUI, sam *client.Conn) {
	t.Helper()
	ctx := context.Background()
	sess, err := sam.Bus.Open(ctx, bus.OpenSessionInput{Hint: "probe"})
	if err != nil {
		t.Fatal(err)
	}
	tuiSessionUp(t, sam.Bus, "eric/tui")
	waitUntil(t, "the first frame to list general", 5*time.Second, func() bool { return strings.Contains(r.out.String(), "general") })
	if _, err := r.in.Write([]byte("i")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.in.Write([]byte("from the tui\r")); err != nil {
		t.Fatal(err)
	}
	var got *bus.Message
	prev := ""
	waitUntil(t, "sam to receive the TUI's message", 2*time.Second, func() bool {
		res, err := sam.Bus.Receive(ctx, bus.ReceiveInput{Session: sess.ID, Ack: prev, IncludeOwn: true})
		if err != nil {
			t.Fatal(err)
		}
		prev = res.Batch
		for i := range res.Messages {
			if res.Messages[i].Content == "from the tui" {
				got = &res.Messages[i]
			}
		}
		return got != nil
	})
	if got.Display != "eric/tui" || got.Sender != "eric" || got.Channel != "general" {
		t.Fatalf("message %+v, want sender eric, display eric/tui on general", *got)
	}
	if code := r.quit(t); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, r.stderr.String())
	}
	if s := r.stderr.String(); s != "" {
		t.Fatalf("stderr %q, want empty", s)
	}
}

func TestRunTUILocalProfile(t *testing.T) {
	pinTUI(t, 80, 24)
	e := newTestEnv(t)
	sam := e.conn(t, "sam")
	r := startTUI(t, e, "-identity", "eric")
	driveSend(t, r, sam)
}

// TestRunTUIOSUserFallback: with no -identity and nothing in the environment,
// the TUI registers under the OS login name (decision 11).
func TestRunTUIOSUserFallback(t *testing.T) {
	pinTUI(t, 80, 24)
	prev := currentUser
	currentUser = func() (*user.User, error) { return &user.User{Username: "eric"}, nil }
	t.Cleanup(func() { currentUser = prev })
	e := newTestEnv(t)
	e.writeProfile(t, profile.Profile{Name: "default", Mode: profile.ModeLocal}) // no identity
	sam := e.conn(t, "sam")
	r := startTUI(t, e)
	driveSend(t, r, sam)
}

// TestRunTUINoIdentityNoUser: with the OS user unknown the old error stands.
func TestRunTUINoIdentityNoUser(t *testing.T) {
	prev := currentUser
	currentUser = func() (*user.User, error) { return nil, errors.New("no passwd entry") }
	t.Cleanup(func() { currentUser = prev })
	e := newTestEnv(t)
	e.writeProfile(t, profile.Profile{Name: "default", Mode: profile.ModeLocal}) // no identity
	var out, errb syncBuf
	if code := RunTUI(context.Background(), nil, e.Environ, strings.NewReader(""), &out, &errb); code != 2 || !strings.Contains(errb.String(), "identity is required") {
		t.Fatalf("code %d stderr %q, want 2 and the identity error", code, errb.String())
	}
}

func TestRunTUIServerProfile(t *testing.T) {
	pinTUI(t, 80, 24)
	ctx := context.Background()
	e := newTestEnv(t)

	sctx, cancel := context.WithCancel(ctx)
	srvOut, srvErr := &syncBuf{}, &syncBuf{}
	done := make(chan int, 1)
	// Made before the stop cleanup is registered, so its RemoveAll runs after
	// the server has closed the database (Windows refuses to delete an open file).
	db := filepath.Join(t.TempDir(), "srv.sqlite")
	go func() {
		done <- cmd.Run(sctx, []string{"-listen", "127.0.0.1:0", "-sqlite", db}, nil, srvOut, srvErr)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("server did not stop")
		}
	})
	re := regexp.MustCompile(`listening on (\S+) `)
	base := ""
	for deadline := time.Now().Add(10 * time.Second); base == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if m := re.FindStringSubmatch(srvOut.String()); m != nil {
			base = "http://" + m[1]
		}
	}
	if base == "" {
		t.Fatalf("no listening line: %s %s", srvOut.String(), srvErr.String())
	}

	gc, err := gen.NewClientWithResponses(base)
	if err != nil {
		t.Fatal(err)
	}
	store := credential.Store{Dir: profile.CredentialDir(e.ConfigDir, "lab")}
	for _, id := range []string{"eric", "sam"} {
		k, err := keys.Generate(keys.EdDSA)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := remote.Register(ctx, gc, id, k.Public(), ""); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		if err := store.Save(id, k); err != nil {
			t.Fatal(err)
		}
	}
	e.writeProfile(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: base})

	sam, err := client.Open(ctx, client.Options{Environ: e.Environ, Cwd: e.Home, Profile: "lab", Identity: "sam", PollInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sam.Close() })

	r := startTUI(t, e, "-profile", "lab", "-identity", "eric")
	driveSend(t, r, sam)
	if _, err := os.Stat(filepath.Join(e.DataDir, "tokens", "lab", "eric.json")); err != nil {
		t.Fatalf("no token cache for eric: %v", err)
	}
}

func TestRunTUIUsageErrors(t *testing.T) {
	pinTUI(t, 80, 24)
	e := newTestEnv(t)
	cases := []struct {
		name string
		args []string
		code int
		want string // substring of stderr
	}{
		{"unknown profile", []string{"-profile", "nosuch"}, 2, "nosuch"},
		{"help", []string{"-h"}, 0, "-profile"},
		{"v1 -as", []string{"-as", "eric"}, 2, "-as"},
		{"v1 -config", []string{"-config", "x"}, 2, "-config"},
		{"positional", []string{"extra"}, 2, `"extra"`},
	}
	for _, c := range cases {
		var stderr syncBuf
		code := RunTUI(context.Background(), c.args, e.Environ, strings.NewReader(""), io.Discard, &stderr)
		if code != c.code || !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%s: exit %d stderr %q, want exit %d naming %q", c.name, code, stderr.String(), c.code, c.want)
		}
	}
}

func TestRunTUIWarnsOnSharedIdentity(t *testing.T) {
	pinTUI(t, 80, 24)
	e := newTestEnv(t)
	other := e.conn(t, "eric")
	if _, err := other.Bus.Open(context.Background(), bus.OpenSessionInput{Hint: "agent"}); err != nil {
		t.Fatal(err)
	}
	r := startTUI(t, e, "-identity", "eric")
	tuiSessionUp(t, other.Bus, "eric/tui")
	want := "agentbus2 tui: identity eric has 1 other live session(s); subscriptions and acks are shared with them (pass -identity for a separate name)\n"
	if got := r.stderr.String(); got != want {
		t.Fatalf("stderr %q, want %q", got, want)
	}
	if code := r.quit(t); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, r.stderr.String())
	}
}

func TestRunTUIRailWidthSaved(t *testing.T) {
	pinTUI(t, 100, 32)
	e := newTestEnv(t)
	sam := e.conn(t, "sam")
	r := startTUI(t, e, "-identity", "eric")
	tuiSessionUp(t, sam.Bus, "eric/tui")
	waitUntil(t, "the first frame to list general", 5*time.Second, func() bool { return strings.Contains(r.out.String(), "general") })
	waitUntil(t, "rail_width saved", 5*time.Second, func() bool {
		go func() { _, _ = r.in.Write([]byte(">")) }()
		time.Sleep(50 * time.Millisecond)
		cfg, err := profile.Load(e.Config.Path)
		return err == nil && cfg.TUI != nil && cfg.TUI.RailWidth > 0
	})
	cfg, err := profile.Load(e.Config.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Profiles["default"]; !ok {
		t.Fatalf("profiles not kept: %+v", cfg.Profiles)
	}
	if code := r.quit(t); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, r.stderr.String())
	}
}
