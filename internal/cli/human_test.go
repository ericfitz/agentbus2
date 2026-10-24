package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/oidctest"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/server/cmd"
)

// humanServer is a running server whose issuer "corp" is a fake provider, with
// the users entry {email corp eric@corp.example}, an agent sam holding a key
// in profile "lab", and the profile itself written with an oidc setting.
type humanServer struct {
	base    string
	prov    *oidctest.Provider
	browser *browserSeam
}

// browserSeam is testOpenBrowser: it GETs the authorization URL, which the
// fake provider answers with a redirect to the loopback listener.
type browserSeam struct {
	opened atomic.Int32
	mu     sync.Mutex
	last   string
}

func (b *browserSeam) open(raw string) error {
	b.opened.Add(1)
	b.mu.Lock()
	b.last = raw
	b.mu.Unlock()
	go func() {
		c := &http.Client{Timeout: 5 * time.Second}
		if resp, err := c.Get(raw); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	return nil
}

func (h *humanServer) lastURL() string {
	h.browser.mu.Lock()
	defer h.browser.mu.Unlock()
	return h.browser.last
}

func startHumanServer(t *testing.T, e *testEnv, profileName string) *humanServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	prov := oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid", RefreshTokens: true,
		Claims: map[string]any{"sub": "u1", "email": "eric@corp.example"}})
	issuers, err := json.Marshal([]map[string]any{{"name": "corp", "issuer": prov.URL(), "jwks_uri": prov.JWKSURI(), "audience": "pai"}})
	if err != nil {
		t.Fatal(err)
	}
	set := "issuers=" + string(issuers)
	srvOut, srvErr := &syncBuf{}, &syncBuf{}
	done := make(chan int, 1)
	// Made before the stop cleanup is registered, so its RemoveAll runs after
	// the server has closed the database (Windows refuses to delete an open file).
	db := filepath.Join(t.TempDir(), "srv.sqlite")
	var so, se bytes.Buffer
	if code := cmd.RunAdmin(ctx, []string{"-sqlite", db, "-set", set, "users", "add", "-issuer", "corp", "-email", "eric@corp.example"}, nil, &so, &se); code != 0 {
		t.Fatalf("users add: %d %s%s", code, so.String(), se.String())
	}
	go func() {
		done <- cmd.Run(ctx, []string{"-listen", "127.0.0.1:0", "-sqlite", db, "-set", set}, nil, srvOut, srvErr)
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
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Register(ctx, gc, "sam", k.Public(), ""); err != nil {
		t.Fatalf("register sam: %v", err)
	}
	if err := (credential.Store{Dir: profile.CredentialDir(e.ConfigDir, profileName)}).Save("sam", k); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	e.writeProfile(t, profile.Profile{Name: profileName, Mode: profile.ModeServer, Server: base,
		OIDC: &profile.OIDC{Issuer: prov.URL(), ClientID: "cid", RedirectPort: port, Scopes: []string{"openid", "email"}}})

	h := &humanServer{base: base, prov: prov, browser: &browserSeam{}}
	prev := testOpenBrowser
	testOpenBrowser = h.browser.open
	t.Cleanup(func() { testOpenBrowser = prev })
	return h
}

func TestLoginHuman(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	hs := startHumanServer(t, e, "lab")
	out, errb := &syncBuf{}, &syncBuf{}
	if code := RunLogin(ctx, []string{"-profile", "lab", "-human"}, e.Environ, nil, out, errb); code != 0 {
		t.Fatalf("login -human: %d %s", code, errb.String())
	}
	re := regexp.MustCompile(`^logged in as eric@corp\.example at ` + regexp.QuoteMeta(hs.base) + ` \(token expires \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ\)\n$`)
	if !re.MatchString(out.String()) {
		t.Fatalf("stdout %q", out.String())
	}
	if want := "agentbus2 login: sign in at " + hs.lastURL() + "\n"; errb.String() != want {
		t.Fatalf("stderr %q, want %q", errb.String(), want)
	}
	// login -human always uses the browser, even with a valid session.
	out, errb = &syncBuf{}, &syncBuf{}
	if code := RunLogin(ctx, []string{"-profile", "lab", "-human"}, e.Environ, nil, out, errb); code != 0 {
		t.Fatalf("second login -human: %d %s", code, errb.String())
	}
	if n := hs.browser.opened.Load(); n != 2 {
		t.Errorf("%d browser opens, want 2", n)
	}
	// An agent login on the same profile still works with sam's key.
	out, errb = &syncBuf{}, &syncBuf{}
	if code := RunLogin(ctx, []string{"-profile", "lab", "-identity", "sam"}, e.Environ, nil, out, errb); code != 0 || !strings.Contains(out.String(), "logged in as sam") {
		t.Fatalf("agent login: %d %q %q", code, out.String(), errb.String())
	}
}

func TestLoginHumanNoOIDC(t *testing.T) {
	e := newTestEnv(t)
	e.writeProfile(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"})
	var out, errb syncBuf
	code := RunLogin(context.Background(), []string{"-profile", "lab", "-human"}, e.Environ, nil, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "oidc") || out.String() != "" {
		t.Fatalf("exit %d stdout %q stderr %q", code, out.String(), errb.String())
	}
}

func TestLoginHumanIdentityRefused(t *testing.T) {
	e := newTestEnv(t)
	e.writeProfile(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1",
		OIDC: &profile.OIDC{Issuer: "https://idp.example", ClientID: "cid", RedirectPort: 8765, Scopes: []string{"openid"}}})
	var out, errb syncBuf
	code := RunLogin(context.Background(), []string{"-profile", "lab", "-human", "-identity", "sam"}, e.Environ, nil, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "-identity does not apply") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	// A local profile is refused by the existing mode check.
	errb = syncBuf{}
	if code := RunLogin(context.Background(), []string{"-human"}, e.Environ, nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "server profiles only") {
		t.Fatalf("local: exit %d stderr %q", code, errb.String())
	}
}
