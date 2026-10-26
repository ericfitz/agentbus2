// Package oidcclient is the browser sign-in client of spec 5.3: the OIDC
// authorization code flow with PKCE (S256) and a loopback redirect, plus the
// refresh grant. It imports no package of this module. No token is logged or
// put in an error.
package oidcclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout is how long SignIn waits for the browser callback.
const DefaultTimeout = 5 * time.Minute

const maxBody = 1 << 20

// ErrRefreshRejected means the provider refused the refresh token (400
// invalid_grant, or invalid_request, which Dex answers for a retired one):
// the caller signs in again.
var ErrRefreshRejected = errors.New("oidcclient: refresh token rejected")

// Config names the provider and how to reach the user.
type Config struct {
	Issuer       string
	ClientID     string
	RedirectPort int
	Scopes       []string
	HTTP         *http.Client           // nil: a client with a 30 s timeout
	OpenBrowser  func(url string) error // nil: OpenBrowser
	Announce     func(url string)       // nil: nothing
	Timeout      time.Duration          // callback wait; 0: DefaultTimeout
}

// Tokens are what the provider's token endpoint issued. ExpiresAt is zero when
// the response had no expires_in.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

type endpoints struct{ authorization, token string }

func (c Config) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// discover reads the provider's discovery document. The document's own issuer
// is not compared with Config.Issuer: OCI's documented issuer differs from the
// domain URL, and the server verifies iss.
func discover(ctx context.Context, c Config) (endpoints, error) {
	u := strings.TrimRight(c.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return endpoints{}, fmt.Errorf("oidcclient: discovery: %w", err)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return endpoints{}, fmt.Errorf("oidcclient: discovery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return endpoints{}, fmt.Errorf("oidcclient: discovery: %s answered %d", u, resp.StatusCode)
	}
	var doc struct {
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&doc); err != nil {
		return endpoints{}, fmt.Errorf("oidcclient: discovery document: %w", err)
	}
	if doc.Authorization == "" {
		return endpoints{}, errors.New("oidcclient: discovery document has no authorization_endpoint")
	}
	if doc.Token == "" {
		return endpoints{}, errors.New("oidcclient: discovery document has no token_endpoint")
	}
	return endpoints{authorization: doc.Authorization, token: doc.Token}, nil
}

func randomB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// callbackResult is what the loopback handler reports.
type callbackResult struct {
	code string
	err  error
}

const donePage = `<!doctype html><meta charset="utf-8"><title>agentbus2</title><p>Sign-in complete. You may close this window.</p>`

const ignoredPage = `<!doctype html><meta charset="utf-8"><title>agentbus2</title><p>This request does not belong to the sign-in in progress and was ignored.</p>`

func failPage(msg string) string {
	return `<!doctype html><meta charset="utf-8"><title>agentbus2</title><p>Sign-in failed: ` + html.EscapeString(msg) + `. You may close this window.</p>`
}

// clip bounds text taken from the callback before it goes in an error.
func clip(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// SignIn runs the authorization code flow and returns the provider's tokens.
func SignIn(ctx context.Context, c Config) (Tokens, error) {
	ep, err := discover(ctx, c)
	if err != nil {
		return Tokens{}, err
	}
	verifier, err := randomB64(32)
	if err != nil {
		return Tokens{}, fmt.Errorf("oidcclient: random: %w", err)
	}
	state, err := randomB64(16)
	if err != nil {
		return Tokens{}, fmt.Errorf("oidcclient: random: %w", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(c.RedirectPort))
	if err != nil {
		return Tokens{}, fmt.Errorf("oidcclient: listen on 127.0.0.1:%d for the sign-in callback: %w", c.RedirectPort, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	results := make(chan callbackResult, 1)
	report := func(r callbackResult) {
		select {
		case results <- r:
		default: // only the first callback counts
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		switch {
		case subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1:
			w.WriteHeader(http.StatusBadRequest)
			// A stray request, a prefetch or a second tab must not abort a
			// legitimate sign-in: ignore it and keep waiting.
			_, _ = io.WriteString(w, ignoredPage)
		case q.Get("error") != "":
			e := clip(q.Get("error"))
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, failPage(e))
			msg := fmt.Sprintf("oidcclient: provider refused the sign-in: %q", e)
			if d := q.Get("error_description"); d != "" {
				msg += fmt.Sprintf(" (%q)", clip(d))
			}
			report(callbackResult{err: errors.New(msg)})
		case q.Get("code") == "":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, failPage("no code"))
			report(callbackResult{err: errors.New("oidcclient: callback has no code")})
		default:
			_, _ = io.WriteString(w, donePage)
			report(callbackResult{code: q.Get("code")})
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		// Shutdown lets the callback response flush to the browser; Close ends
		// whatever a keep-alive connection still holds after the grace period.
		sctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if srv.Shutdown(sctx) != nil {
			_ = srv.Close()
		}
		_ = ln.Close() // free the port even if Serve never ran
	}()

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.ClientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if len(c.Scopes) > 0 {
		q.Set("scope", strings.Join(c.Scopes, " "))
	}
	authURL, err := withQuery(ep.authorization, q)
	if err != nil {
		return Tokens{}, err
	}
	if c.Announce != nil {
		c.Announce(authURL)
	}
	open := c.OpenBrowser
	if open == nil {
		open = OpenBrowser
	}
	if err := open(authURL); err != nil && c.Announce != nil {
		c.Announce(fmt.Sprintf("could not open the browser (%v); open the URL above by hand", err))
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var res callbackResult
	select {
	case res = <-results:
	case <-timer.C:
		return Tokens{}, fmt.Errorf("oidcclient: timed out after %v waiting for the browser sign-in", timeout)
	case <-ctx.Done():
		return Tokens{}, fmt.Errorf("oidcclient: sign-in: %w", ctx.Err())
	}
	if res.err != nil {
		return Tokens{}, res.err
	}
	return tokenRequest(ctx, c, ep.token, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirectURI},
		"client_id":     {c.ClientID},
		"code_verifier": {verifier},
	}, "")
}

// withQuery adds q to the endpoint URL, keeping any query it already has.
func withQuery(endpoint string, q url.Values) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("oidcclient: authorization_endpoint: %w", err)
	}
	v := u.Query()
	for k, vals := range q {
		v[k] = vals
	}
	u.RawQuery = v.Encode()
	return u.String(), nil
}

// Refresh exchanges a refresh token for new tokens. A 400 invalid_grant or
// invalid_request is ErrRefreshRejected; any other failure is a different error.
func Refresh(ctx context.Context, c Config, refreshToken string) (Tokens, error) {
	ep, err := discover(ctx, c)
	if err != nil {
		return Tokens{}, err
	}
	return tokenRequest(ctx, c, ep.token, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.ClientID},
	}, refreshToken)
}

// tokenRequest POSTs form to the token endpoint. keepRefresh is the refresh
// token to return when the response issues no new one.
func tokenRequest(ctx context.Context, c Config, endpoint string, form url.Values, keepRefresh string) (Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, fmt.Errorf("oidcclient: token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("oidcclient: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Tokens{}, fmt.Errorf("oidcclient: token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if resp.StatusCode == http.StatusBadRequest && (e.Error == "invalid_grant" || e.Error == "invalid_request") && form.Get("grant_type") == "refresh_token" {
			return Tokens{}, ErrRefreshRejected
		}
		// Only the status and the OAuth error code: never the body.
		return Tokens{}, fmt.Errorf("oidcclient: token endpoint answered %d %q", resp.StatusCode, clip(e.Error))
	}
	var out struct {
		AccessToken  string          `json:"access_token"`
		TokenType    string          `json:"token_type"`
		RefreshToken string          `json:"refresh_token"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Tokens{}, errors.New("oidcclient: token response is not JSON")
	}
	if out.AccessToken == "" {
		return Tokens{}, errors.New("oidcclient: token response has no access_token")
	}
	if !strings.EqualFold(out.TokenType, "Bearer") {
		return Tokens{}, errors.New("oidcclient: token response token_type is not Bearer")
	}
	t := Tokens{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken}
	if t.RefreshToken == "" {
		t.RefreshToken = keepRefresh
	}
	if secs := parseExpiresIn(out.ExpiresIn); secs > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(secs) * time.Second)
	}
	return t, nil
}

// maxExpiresIn bounds expires_in (one year) so a huge value cannot overflow a Duration.
const maxExpiresIn = 365 * 24 * 3600

// parseExpiresIn reads expires_in as a JSON number or numeric string, in whole
// seconds, clamped to maxExpiresIn. Anything else yields 0 (no expiry known).
func parseExpiresIn(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	str := strings.TrimSpace(string(raw))
	if str != "" && str[0] == '"' {
		var v string
		if json.Unmarshal(raw, &v) != nil {
			return 0
		}
		str = strings.TrimSpace(v)
	}
	f, err := strconv.ParseFloat(str, 64)
	if err != nil || f != f || f <= 0 {
		return 0
	}
	if f > maxExpiresIn {
		return maxExpiresIn
	}
	return int64(f)
}
