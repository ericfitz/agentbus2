package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/tui"
)

// pinTUI pins the TUI's window size and the host name (macmini) for the test
// and restores them.
func pinTUI(t *testing.T, w, h int) {
	t.Helper()
	prevHost := osHostname
	osHostname = func() (string, error) { return "MacMini.local", nil }
	t.Cleanup(func() { osHostname = prevHost })
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
// eric/macmini, then quit.
func driveSend(t *testing.T, r *runningTUI, sam *client.Conn) {
	t.Helper()
	driveSendAs(t, r, sam, "eric", "", nil)
}

// driveSendAs is driveSend for a TUI whose identity is identity. wantKind, when
// set, is the SenderKind sam must see; wantStderr is the exact stderr the TUI
// must leave behind (nil: none), computed once the TUI has quit.
func driveSendAs(t *testing.T, r *runningTUI, sam *client.Conn, identity string, wantKind bus.IdentityKind, wantStderr func() string) {
	t.Helper()
	ctx := context.Background()
	sess, err := sam.Bus.Open(ctx, bus.OpenSessionInput{Hint: "probe"})
	if err != nil {
		t.Fatal(err)
	}
	tuiSessionUp(t, sam.Bus, identity+"/macmini")
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
	if got.Display != identity+"/macmini" || got.Sender != identity || got.Channel != "general" {
		t.Fatalf("message %+v, want sender %s, display %s/macmini on general", *got, identity, identity)
	}
	if wantKind != "" && got.SenderKind != wantKind {
		t.Fatalf("message sender kind %q, want %q", got.SenderKind, wantKind)
	}
	if code := r.quit(t); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, r.stderr.String())
	}
	want := ""
	if wantStderr != nil {
		want = wantStderr()
	}
	if s := r.stderr.String(); s != want {
		t.Fatalf("stderr %q, want %q", s, want)
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

// TestRunTUIServerProfile: on a server profile the TUI is a human who signs in
// through the profile's corporate provider (Decision 11, spec 5.3). The sign-in
// URL is announced on stderr before the TUI starts and never again; the
// identity is the email the server bound; an agent with a key (sam) sees the
// TUI's message as a human's.
func TestRunTUIServerProfile(t *testing.T) {
	pinTUI(t, 80, 24)
	ctx := context.Background()
	e := newTestEnv(t)
	hs := startHumanServer(t, e, "lab")

	sam, err := client.Open(ctx, client.Options{Environ: e.Environ, Cwd: e.Home, Profile: "lab", Identity: "sam", PollInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sam.Close() })

	r := startTUI(t, e, "-profile", "lab")
	driveSendAs(t, r, sam, "eric@corp.example", bus.IdentityHuman, func() string { return "agentbus2 tui: sign in at " + hs.lastURL() + "\n" })
	if hs.browser.opened.Load() != 1 {
		t.Errorf("%d browser opens, want 1", hs.browser.opened.Load())
	}
	cache := filepath.Join(e.DataDir, "tokens", "lab", "human", "session.json")
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("no human session cache: %v", err)
	}
	// The next TUI reuses the session: no browser, nothing on stderr.
	r2 := startTUI(t, e, "-profile", "lab")
	driveSendAs(t, r2, sam, "eric@corp.example", bus.IdentityHuman, nil)
	if hs.browser.opened.Load() != 1 {
		t.Errorf("%d browser opens after the second TUI, want 1", hs.browser.opened.Load())
	}
}

// -identity names an agent's key; on a server profile the identity comes from
// sign-in, so the flag is refused before anything is dialed.
func TestRunTUIServerIdentityFlagRefused(t *testing.T) {
	pinTUI(t, 80, 24)
	e := newTestEnv(t)
	e.writeProfile(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1", OIDC: &profile.OIDC{Issuer: "https://idp.example", ClientID: "cid", RedirectPort: 8765, Scopes: []string{"openid"}}})
	var errb syncBuf
	code := RunTUI(context.Background(), []string{"-profile", "lab", "-identity", "eric"}, e.Environ, strings.NewReader(""), io.Discard, &errb)
	if code != 2 || !strings.Contains(errb.String(), "-identity does not apply on a server profile") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}

func TestRunTUIServerNoOIDC(t *testing.T) {
	pinTUI(t, 80, 24)
	e := newTestEnv(t)
	e.writeProfile(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"})
	var errb syncBuf
	code := RunTUI(context.Background(), []string{"-profile", "lab"}, e.Environ, strings.NewReader(""), io.Discard, &errb)
	if code != 2 || !strings.Contains(errb.String(), "no oidc setting") || !strings.Contains(errb.String(), `"lab"`) {
		t.Fatalf("exit %d stderr %q", code, errb.String())
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
	tuiSessionUp(t, other.Bus, "eric/macmini")
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
	tuiSessionUp(t, sam.Bus, "eric/macmini")
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

// TestRunTUIHostnameFallback: a host name that fails the name rule makes the
// session <identity>/tui, not an Open error (TUI decision 12).
func TestRunTUIHostnameFallback(t *testing.T) {
	pinTUI(t, 80, 24)
	osHostname = func() (string, error) { return "a/b", nil }
	e := newTestEnv(t)
	sam := e.conn(t, "sam")
	r := startTUI(t, e, "-identity", "eric")
	tuiSessionUp(t, sam.Bus, "eric/tui")
	if code := r.quit(t); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, r.stderr.String())
	}
}

// TestRunTUIOSUserWithKeys: an OS-login identity that holds keys cannot be
// the TUI's human identity; exit 2 names the identity and the -identity flag.
func TestRunTUIOSUserWithKeys(t *testing.T) {
	prev := currentUser
	currentUser = func() (*user.User, error) { return &user.User{Username: "eric"}, nil }
	t.Cleanup(func() { currentUser = prev })
	e := newTestEnv(t)
	e.writeProfile(t, profile.Profile{Name: "default", Mode: profile.ModeLocal}) // no identity
	c := e.conn(t, "eric")
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := keys.MarshalPublic(k.Public())
	kid, _ := keys.KID(k.Public())
	err = c.Core.Store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.PutPublicKey(ctx, store.PublicKey{Identity: "eric", KID: kid, Algorithm: string(keys.EdDSA), Key: der, CreatedAt: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	var out, errb syncBuf
	code := RunTUI(context.Background(), nil, e.Environ, strings.NewReader(""), &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), `identity "eric" holds keys`) || !strings.Contains(errb.String(), "pass -identity") {
		t.Fatalf("code %d stderr %q, want 2 and the holds-keys error", code, errb.String())
	}
}
