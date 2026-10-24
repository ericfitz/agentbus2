package oidcclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/oidctest"
)

// freePort asks the OS for a loopback port that is free now.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// browser is the OpenBrowser seam: it GETs the authorization URL with a client
// that follows redirects to the loopback listener and keeps the final page.
type browser struct {
	rewrite func(*url.URL)
	page    chan string
	opened  chan string
}

func newBrowser(rewrite func(*url.URL)) *browser {
	return &browser{rewrite: rewrite, page: make(chan string, 1), opened: make(chan string, 1)}
}

func (b *browser) open(raw string) error {
	b.opened <- raw
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if b.rewrite != nil {
		b.rewrite(u)
	}
	go func() {
		c := &http.Client{Timeout: 5 * time.Second}
		resp, err := c.Get(u.String())
		if err != nil {
			b.page <- "ERR " + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		b.page <- string(body)
	}()
	return nil
}

func config(p *oidctest.Provider, port int, b *browser) Config {
	return Config{
		Issuer:       p.URL(),
		ClientID:     "cid",
		RedirectPort: port,
		Scopes:       []string{"openid", "email"},
		OpenBrowser:  b.open,
		Timeout:      5 * time.Second,
	}
}

func TestSignInPKCE(t *testing.T) {
	p := oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid", RefreshTokens: true,
		Claims: map[string]any{"sub": "u1", "email": "a@b.example"}})
	port := freePort(t)
	b := newBrowser(nil)
	var announced string
	c := config(p, port, b)
	c.Announce = func(u string) { announced = u }
	tok, err := SignIn(context.Background(), c)
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatalf("tokens = %+v", tok)
	}
	if d := time.Until(tok.ExpiresAt); d < 4*time.Minute || d > 6*time.Minute {
		t.Errorf("ExpiresAt in %v, want about 5 minutes", d)
	}
	parsed, err := jwt.ParseSigned(tok.AccessToken, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatalf("access token is not a JWT: %v", err)
	}
	_ = parsed
	opened := <-b.opened
	if announced != opened {
		t.Errorf("announced %q, opened %q", announced, opened)
	}
	u, _ := url.Parse(opened)
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != "cid" || q.Get("scope") != "openid email" ||
		q.Get("redirect_uri") != fmt.Sprintf("http://127.0.0.1:%d/callback", port) || q.Get("code_challenge_method") != "S256" {
		t.Errorf("authorization URL = %s", opened)
	}
	// The provider saw a verifier that hashes to the challenge.
	var verifier string
	for _, r := range p.Requests() {
		if strings.HasPrefix(r, "POST /token ") {
			form, _ := url.ParseQuery(strings.TrimPrefix(r, "POST /token "))
			verifier = form.Get("code_verifier")
			if form.Get("grant_type") != "authorization_code" || form.Get("client_id") != "cid" || form.Get("code") == "" {
				t.Errorf("token request = %v", form)
			}
		}
	}
	if verifier == "" {
		t.Fatal("no code_verifier reached the token endpoint")
	}
	sum := sha256.Sum256([]byte(verifier))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != q.Get("code_challenge") {
		t.Errorf("challenge %q != S256(verifier) %q", q.Get("code_challenge"), got)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(verifier); err != nil || len(raw) != 32 {
		t.Errorf("verifier %q is not 32 random bytes, base64url", verifier)
	}
	if page := <-b.page; !strings.Contains(page, "close this window") {
		t.Errorf("callback page = %q", page)
	}
	// The listener is gone once SignIn returns.
	assertPortFree(t, port)
}

func assertPortFree(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err == nil {
			_ = l.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %d still bound: %v", port, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSignInStateMismatch(t *testing.T) {
	p := oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid"})
	port := freePort(t)
	b := newBrowser(func(u *url.URL) {
		q := u.Query()
		q.Set("state", "forged")
		u.RawQuery = q.Encode()
	})
	_, err := SignIn(context.Background(), config(p, port, b))
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("err = %v, want one naming state", err)
	}
	for _, r := range p.Requests() {
		if strings.HasPrefix(r, "POST /token") {
			t.Error("the code was exchanged despite a state mismatch")
		}
	}
	assertPortFree(t, port)
}

func TestSignInProviderError(t *testing.T) {
	p := oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid", AuthorizeError: "access_denied"})
	b := newBrowser(nil)
	_, err := SignIn(context.Background(), config(p, freePort(t), b))
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want one containing access_denied", err)
	}
}

func TestSignInPortBusy(t *testing.T) {
	p := oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid"})
	hold, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Close() }()
	port := hold.Addr().(*net.TCPAddr).Port
	b := newBrowser(nil)
	_, err = SignIn(context.Background(), config(p, port, b))
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Fatalf("err = %v, want one naming port %d", err, port)
	}
	select {
	case u := <-b.opened:
		t.Errorf("browser opened (%s) although the port was busy", u)
	default:
	}
}

func TestSignInTimeout(t *testing.T) {
	p := oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid"})
	port := freePort(t)
	c := config(p, port, newBrowser(nil))
	c.OpenBrowser = func(string) error { return nil } // never calls back
	c.Timeout = 200 * time.Millisecond
	start := time.Now()
	_, err := SignIn(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
	assertPortFree(t, port)
}

func TestSignInContextCanceled(t *testing.T) {
	p := oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid"})
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	c := config(p, port, newBrowser(nil))
	c.OpenBrowser = func(string) error { cancel(); return nil }
	if _, err := SignIn(ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertPortFree(t, port)
}

func TestSignInBrowserFailureIsAnnounced(t *testing.T) {
	p := oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid"})
	port := freePort(t)
	b := newBrowser(nil)
	var lines []string
	c := config(p, port, b)
	c.OpenBrowser = func(u string) error {
		go func() { _ = b.open(u) }() // the user pastes the URL by hand
		return errors.New("no display")
	}
	c.Announce = func(s string) { lines = append(lines, s) }
	if _, err := SignIn(context.Background(), c); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if len(lines) != 2 || !strings.Contains(lines[1], "no display") {
		t.Errorf("announced = %q, want the URL then the browser error", lines)
	}
}

func TestSignInDiscoveryIncomplete(t *testing.T) {
	for name, doc := range map[string]string{
		"token_endpoint":         `{"authorization_endpoint":"http://x/authorize"}`,
		"authorization_endpoint": `{"token_endpoint":"http://x/token"}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, doc)
			}))
			defer srv.Close()
			port := freePort(t)
			_, err := SignIn(context.Background(), Config{Issuer: srv.URL, ClientID: "cid", RedirectPort: port,
				OpenBrowser: func(string) error { t.Error("browser opened"); return nil }})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v, want one naming %s", err, name)
			}
		})
	}
}

func TestSignInTokenEndpointFailureHasNoToken(t *testing.T) {
	// A token response without a Bearer token_type is refused, and the error
	// does not echo the access token.
	secret := "SECRET-ACCESS-TOKEN"
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_, _ = fmt.Fprintf(w, `{"authorization_endpoint":%q,"token_endpoint":%q}`, base+"/authorize", base+"/token")
		case "/authorize":
			q := r.URL.Query()
			http.Redirect(w, r, q.Get("redirect_uri")+"?code=c&state="+q.Get("state"), http.StatusFound)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"mac"}`, secret)
		}
	}))
	defer srv.Close()
	base = srv.URL
	b := newBrowser(nil)
	_, err := SignIn(context.Background(), Config{Issuer: srv.URL, ClientID: "cid", RedirectPort: freePort(t),
		OpenBrowser: b.open, Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "token_type") || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v, want one naming token_type and no token", err)
	}
}

func TestRefresh(t *testing.T) {
	p := oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid", RefreshTokens: true})
	b := newBrowser(nil)
	first, err := SignIn(context.Background(), config(p, freePort(t), b))
	if err != nil {
		t.Fatal(err)
	}
	next, err := Refresh(context.Background(), Config{Issuer: p.URL(), ClientID: "cid"}, first.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if next.AccessToken == "" || next.RefreshToken == "" || time.Until(next.ExpiresAt) < time.Minute {
		t.Errorf("tokens = %+v", next)
	}
}

func TestRefreshRejected(t *testing.T) {
	p := oidctest.New(t, oidctest.Options{Audience: "pai", ClientID: "cid", RefreshTokens: true})
	_, err := Refresh(context.Background(), Config{Issuer: p.URL(), ClientID: "cid"}, "unknown")
	if !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("err = %v, want ErrRefreshRejected", err)
	}
	// An outage is not a rejection.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "openid-configuration") {
			_, _ = fmt.Fprintf(w, `{"authorization_endpoint":"x","token_endpoint":"http://%s/token"}`, r.Host)
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err = Refresh(context.Background(), Config{Issuer: srv.URL, ClientID: "cid"}, "rt")
	if err == nil || errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("err = %v, want a non-rejection error", err)
	}
}

func TestOpenBrowserRefusesNonHTTP(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "calc.exe", "", "javascript:alert(1)"} {
		if err := OpenBrowser(u); err == nil {
			t.Errorf("OpenBrowser(%q) = nil, want a refusal", u)
		}
	}
}
