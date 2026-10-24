package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/oidctest"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/server/cmd"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// testBrowser is the HumanOpenBrowser seam: it GETs the authorization URL,
// which the fake provider answers with a redirect to the loopback listener.
type testBrowser struct{ opened atomic.Int32 }

func (b *testBrowser) open(raw string) error {
	b.opened.Add(1)
	go func() {
		c := &http.Client{Timeout: 5 * time.Second}
		if resp, err := c.Get(raw); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	return nil
}

// startHumanServer runs `agentbus2 server` with prov as the issuer "corp" and
// the users entry {email corp eric@corp.example}; it returns the base URL.
func startHumanServer(t *testing.T, prov *oidctest.Provider) string {
	t.Helper()
	issuers, err := json.Marshal([]map[string]any{{"name": "corp", "issuer": prov.URL(), "jwks_uri": prov.JWKSURI(), "audience": "pai"}})
	if err != nil {
		t.Fatal(err)
	}
	set := "issuers=" + string(issuers)
	ctx, cancel := context.WithCancel(context.Background())
	out, errb := &lockedBuf{}, &lockedBuf{}
	done := make(chan int, 1)
	// Made before the stop cleanup is registered, so its RemoveAll runs after
	// the server has closed the database (Windows refuses to delete an open file).
	db := filepath.Join(t.TempDir(), "srv.sqlite")
	var so, se bytes.Buffer
	if code := cmd.RunAdmin(ctx, []string{"-sqlite", db, "-set", set, "users", "add", "-issuer", "corp", "-email", "eric@corp.example"}, nil, &so, &se); code != 0 {
		t.Fatalf("users add: %d %s%s", code, so.String(), se.String())
	}
	go func() {
		done <- cmd.Run(ctx, []string{"-listen", "127.0.0.1:0", "-sqlite", db, "-set", set}, nil, out, errb)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("server did not stop")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := listeningRE.FindStringSubmatch(out.String()); m != nil {
			return "http://" + m[1]
		}
		select {
		case code := <-done:
			t.Fatalf("server exited %d: %s %s", code, out.String(), errb.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("no listening line: %s %s", out.String(), errb.String())
	return ""
}

func TestResolveHumanSignInNeedsOIDC(t *testing.T) {
	e := newEnv(t)
	oidc := &profile.OIDC{Issuer: "https://idp.example", ClientID: "cid", RedirectPort: 8765, Scopes: []string{"openid"}}
	e.save(t,
		profile.Profile{Name: "default", Mode: profile.ModeLocal},
		profile.Profile{Name: "plain", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"},
		profile.Profile{Name: "corp", Mode: profile.ModeServer, Server: "http://127.0.0.1:1", OIDC: oidc},
	)
	e.credential(t, "corp", "pat") // agent keys in the same profile never select the identity
	for _, name := range []string{"plain", "default"} {
		_, err := Resolve(Options{Environ: e.environ, Cwd: t.TempDir(), Profile: name, HumanSignIn: true})
		var re *ResolveError
		if !errors.As(err, &re) || !strings.Contains(err.Error(), "no oidc setting") || !strings.Contains(err.Error(), `"`+name+`"`) {
			t.Errorf("profile %s: err %v, want a ResolveError naming the oidc setting", name, err)
		}
	}
	r, err := Resolve(Options{Environ: e.environ, Cwd: t.TempDir(), Profile: "corp", HumanSignIn: true, Identity: "ignored", FallbackIdentity: "efitz"})
	if err != nil || r.Identity != "" || r.IdentitySource != "sign-in" || r.Profile.OIDC == nil {
		t.Fatalf("Resolve with oidc = %+v, %v", r, err)
	}
	// Without HumanSignIn the same profile still selects the agent's credential.
	if r, err = Resolve(Options{Environ: e.environ, Cwd: t.TempDir(), Profile: "corp"}); err != nil || r.Identity != "pat" {
		t.Fatalf("agent resolve = %q, %v", r.Identity, err)
	}
}

func TestOpenServerHumanSignIn(t *testing.T) {
	ctx := context.Background()
	prov := oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid", RefreshTokens: true,
		Claims: map[string]any{"sub": "u1", "email": "eric@corp.example"}})
	base := startHumanServer(t, prov)
	e := newEnv(t)
	e.save(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: base,
		OIDC: &profile.OIDC{Issuer: prov.URL(), ClientID: "cid", RedirectPort: freePort(t), Scopes: []string{"openid", "email"}}})
	b := &testBrowser{}
	var announced atomic.Value
	o := Options{Environ: e.environ, Cwd: t.TempDir(), Profile: "lab", HumanSignIn: true, HumanOpenBrowser: b.open,
		HumanAnnounce: func(u string) { announced.Store(u) }}

	c, err := Open(ctx, o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c.Resolved.Identity != "eric@corp.example" || c.Resolved.IdentitySource != "sign-in" || c.Human == nil || c.Source != nil {
		t.Fatalf("resolved %+v human %v source %v", c.Resolved, c.Human, c.Source)
	}
	if u, _ := announced.Load().(string); !strings.HasPrefix(u, prov.URL()) {
		t.Errorf("announced %q", u)
	}
	sess, err := c.Bus.Open(ctx, bus.OpenSessionInput{Hint: "human"})
	if err != nil {
		t.Fatalf("Open session: %v", err)
	}
	if _, err := c.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "hi", Session: sess.ID}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	msgs, err := c.Bus.History(ctx, bus.HistoryInput{Channel: "general", Count: 5})
	if err != nil || len(msgs) == 0 || msgs[len(msgs)-1].Sender != "eric@corp.example" || msgs[len(msgs)-1].SenderKind != bus.IdentityHuman {
		t.Fatalf("history %+v, %v: want the message from the human eric@corp.example", msgs, err)
	}
	cache := filepath.Join(e.data, "tokens", "lab", "human", "session.json")
	fi, err := os.Stat(cache)
	if err != nil {
		t.Fatalf("session cache: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("session mode %v", fi.Mode().Perm())
	}

	// A second Open reuses the session: no browser.
	c2, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.Close() }()
	if n := b.opened.Load(); n != 1 || c2.Resolved.Identity != "eric@corp.example" {
		t.Errorf("%d browser opens, identity %q; want 1 and the cached identity", n, c2.Resolved.Identity)
	}

	// Deferred: Open obtains nothing and the caller drives the source.
	e.save(t, profile.Profile{Name: "lab2", Mode: profile.ModeServer, Server: base,
		OIDC: &profile.OIDC{Issuer: prov.URL(), ClientID: "cid", RedirectPort: freePort(t), Scopes: []string{"openid"}}})
	d := o
	d.Profile, d.HumanDeferSignIn = "lab2", true
	c3, err := Open(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c3.Close() }()
	if n := b.opened.Load(); n != 1 || c3.Resolved.Identity != "" {
		t.Errorf("deferred Open: %d browser opens, identity %q", n, c3.Resolved.Identity)
	}
}

// The provider refusing the sign-in is a plain error, not a resolve error.
func TestOpenServerHumanSignInProviderRefusal(t *testing.T) {
	prov := oidctest.New(t, oidctest.Options{Audience: "pai", AuthorizeError: "access_denied"})
	base := startHumanServer(t, prov)
	e := newEnv(t)
	e.save(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: base,
		OIDC: &profile.OIDC{Issuer: prov.URL(), ClientID: "cid", RedirectPort: freePort(t), Scopes: []string{"openid"}}})
	_, err := Open(context.Background(), Options{Environ: e.environ, Cwd: t.TempDir(), Profile: "lab", HumanSignIn: true, HumanOpenBrowser: (&testBrowser{}).open})
	var re *ResolveError
	if err == nil || errors.As(err, &re) || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err %v", err)
	}
	if _, serr := os.Stat(filepath.Join(e.data, "tokens", "lab", "human", "session.json")); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("a failed sign-in left a session: %v", serr)
	}
}
