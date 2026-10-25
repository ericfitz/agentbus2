package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/oidctest"
	"github.com/ericfitz/agentbus2/internal/store"
)

const (
	grantExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	typeAccess    = "urn:ietf:params:oauth:token-type:access_token"
	typeID        = "urn:ietf:params:oauth:token-type:id_token"
	typeRefresh   = "urn:ietf:params:oauth:token-type:refresh_token"
	corpEric      = "eric@corp.example"
)

// logBuf is a goroutine-safe log sink.
type logBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sb.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sb.String()
}

// fedEnv is an env with a fake provider configured as the issuers entry
// "corp" and users {email corp eric@corp.example}.
type fedEnv struct {
	*env
	prov *oidctest.Provider
	log  *logBuf
}

func newFedEnv(t *testing.T, lifetime time.Duration, mutate func(*token.External)) *fedEnv {
	t.Helper()
	prov := oidctest.New(t, oidctest.Options{Audience: "pai"})
	if lifetime == 0 {
		lifetime = 900 * time.Second
	}
	ext := &token.External{Name: "corp", Issuer: prov.URL(), JWKSURI: prov.JWKSURI(), Audience: "pai", HumanLifetime: lifetime}
	if mutate != nil {
		mutate(ext)
	}
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeEnrollment, external: []*token.External{ext}})
	f := &fedEnv{env: e, prov: prov, log: &logBuf{}}
	e.opts.Logger = slog.New(slog.NewTextHandler(f.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.users(store.RefEmail, corpEric)
	return f
}

func (f *fedEnv) users(kind store.RefKind, value string) {
	f.t.Helper()
	_, err := identities.Service{Core: f.core}.AddReference(context.Background(),
		store.Reference{List: store.ListUsers, Kind: kind, Issuer: "corp", Value: value}, []string{"corp"})
	if err != nil {
		f.t.Fatal(err)
	}
}

func exchangeForm(subject string, extra map[string]string) url.Values {
	v := url.Values{"grant_type": {grantExchange}, "subject_token_type": {typeAccess}}
	if subject != "" {
		v.Set("subject_token", subject)
	}
	for k, val := range extra {
		if val == "" {
			v.Del(k)
		} else {
			v.Set(k, val)
		}
	}
	return v
}

func (f *fedEnv) post(v url.Values) (int, string, map[string]any) {
	f.t.Helper()
	resp := f.do("POST", "/v1/auth/token", "application/x-www-form-urlencoded", "", v.Encode())
	body := readBody(f.t, resp)
	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	return resp.StatusCode, body, m
}

// bearerClaims reads the claims and header typ of a minted bearer.
func bearerClaims(t *testing.T, raw string) (map[string]any, string) {
	t.Helper()
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]any
	if err := tok.UnsafeClaimsWithoutVerification(&all); err != nil {
		t.Fatal(err)
	}
	typ, _ := tok.Headers[0].ExtraHeaders[jose.HeaderType].(string)
	return all, typ
}

func TestTokenExchangeIssuesHumanBearer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lifetime time.Duration
		want     float64
	}{{"default", 0, 900}, {"configured", 120 * time.Second, 120}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFedEnv(t, tc.lifetime, nil)
			status, body, m := f.post(exchangeForm(f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric}), nil))
			if status != 200 {
				t.Fatalf("exchange = %d %s", status, body)
			}
			if m["issued_token_type"] != typeAccess || m["token_type"] != "Bearer" || m["expires_in"] != tc.want {
				t.Fatalf("response = %v", m)
			}
			raw, _ := m["access_token"].(string)
			c, typ := bearerClaims(t, raw)
			if c["sub"] != corpEric || c["agentbus2_kind"] != "human" || typ != "at+jwt" {
				t.Fatalf("bearer sub %v kind %v typ %q", c["sub"], c["agentbus2_kind"], typ)
			}
			// The listing shows identities with a live session.
			if r := f.do("POST", "/v1/sessions", "application/json", "Bearer "+raw, "{}"); r.StatusCode != 201 {
				t.Fatalf("open session with the bearer = %d %s", r.StatusCode, readBody(t, r))
			}
			resp := f.do("GET", "/v1/identities", "", "Bearer "+raw, "")
			if resp.StatusCode != 200 {
				t.Fatalf("GET /v1/identities with the bearer = %d", resp.StatusCode)
			}
			var ids []gen.Identity
			if err := json.Unmarshal([]byte(readBody(t, resp)), &ids); err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(ids, func(i gen.Identity) bool { return i.Name == corpEric })
			if i < 0 || ids[i].Kind != "human" {
				t.Fatalf("identities = %+v, want eric as human", ids)
			}
		})
	}
}

func TestTokenExchangeRefusals(t *testing.T) {
	good := func(f *fedEnv) string { return f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric}) }
	cases := []struct {
		name   string
		status int
		code   string
		form   func(f *fedEnv) url.Values
		prep   func(f *fedEnv)
	}{
		{"no-subject-token", 400, "invalid_request", func(f *fedEnv) url.Values { return exchangeForm("", nil) }, nil},
		{"id-token-type", 400, "invalid_request", func(f *fedEnv) url.Values {
			return exchangeForm(good(f), map[string]string{"subject_token_type": typeID})
		}, nil},
		{"unknown-type", 400, "invalid_request", func(f *fedEnv) url.Values {
			return exchangeForm(good(f), map[string]string{"subject_token_type": "urn:x:unknown"})
		}, nil},
		{"missing-type", 400, "invalid_request", func(f *fedEnv) url.Values {
			return exchangeForm(good(f), map[string]string{"subject_token_type": ""})
		}, nil},
		{"requested-refresh", 400, "invalid_request", func(f *fedEnv) url.Values {
			return exchangeForm(good(f), map[string]string{"requested_token_type": typeRefresh})
		}, nil},
		{"audience", 400, "invalid_target", func(f *fedEnv) url.Values {
			return exchangeForm(good(f), map[string]string{"audience": "https://other"})
		}, nil},
		{"resource", 400, "invalid_target", func(f *fedEnv) url.Values {
			return exchangeForm(good(f), map[string]string{"resource": "https://other"})
		}, nil},
		{"actor-token", 400, "invalid_request", func(f *fedEnv) url.Values {
			return exchangeForm(good(f), map[string]string{"actor_token": "x"})
		}, nil},
		{"unknown-issuer", 400, "invalid_request", func(f *fedEnv) url.Values {
			return exchangeForm(f.prov.Mint(map[string]any{"iss": "https://nobody", "sub": "g-1", "email": corpEric}), nil)
		}, nil},
		{"garbage", 400, "invalid_request", func(f *fedEnv) url.Values { return exchangeForm("not-a-jwt", nil) }, nil},
		{"expired", 400, "invalid_request", func(f *fedEnv) url.Values {
			return exchangeForm(f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric, "exp": time.Now().Add(-time.Hour).Unix()}), nil)
		}, nil},
		{"wrong-aud", 400, "invalid_request", func(f *fedEnv) url.Values {
			return exchangeForm(f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric, "aud": "someone-else"}), nil)
		}, nil},
		{"tampered", 400, "invalid_request", func(f *fedEnv) url.Values {
			parts := strings.Split(good(f), ".")
			sig := []byte(parts[2])
			if sig[10] == 'A' {
				sig[10] = 'B'
			} else {
				sig[10] = 'A'
			}
			parts[2] = string(sig)
			return exchangeForm(strings.Join(parts, "."), nil)
		}, nil},
		{"no-email", 400, "invalid_request", func(f *fedEnv) url.Values {
			return exchangeForm(f.prov.Mint(map[string]any{"sub": "g-1"}), nil)
		}, nil},
		{"jwks-down", 503, "temporarily_unavailable", func(f *fedEnv) url.Values { return exchangeForm(good(f), nil) },
			func(f *fedEnv) { f.prov.BreakJWKS() }},
		{"not-in-users", 400, exchangeDeniedCode, func(f *fedEnv) url.Values {
			return exchangeForm(f.prov.Mint(map[string]any{"sub": "g-2", "email": "mallory@corp.example"}), nil)
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFedEnv(t, 0, nil)
			if tc.prep != nil {
				tc.prep(f)
			}
			before := identityCount(t, f.env)
			form := tc.form(f)
			wantOAuthError(t, f.do("POST", "/v1/auth/token", "application/x-www-form-urlencoded", "", form.Encode()), tc.status, tc.code, "", form.Get("subject_token"))
			if n := identityCount(t, f.env); n != before {
				t.Errorf("identity rows %d -> %d after a refusal", before, n)
			}
		})
	}
}

func TestTokenExchangeClientID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claims map[string]any
		want   string
	}{
		{"client_id", map[string]any{"client_id": "cid-1", "azp": "azp-1"}, "cid-1"},
		{"azp", map[string]any{"client_id": nil, "azp": "azp-1"}, "azp-1"},
		{"neither", map[string]any{"client_id": nil}, "g-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFedEnv(t, 0, nil)
			claims := map[string]any{"sub": "g-1", "email": corpEric}
			for k, v := range tc.claims {
				claims[k] = v
			}
			status, body, m := f.post(exchangeForm(f.prov.Mint(claims), nil))
			if status != 200 {
				t.Fatalf("exchange = %d %s", status, body)
			}
			c, _ := bearerClaims(t, m["access_token"].(string))
			if c["client_id"] != tc.want {
				t.Errorf("client_id = %v, want %q", c["client_id"], tc.want)
			}
		})
	}
}

func TestMetadataAdvertisesExchange(t *testing.T) {
	e := newEnv(t, envOptions{})
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		var c gen.AuthorizationServerMetadata
		if err := json.Unmarshal([]byte(readBody(t, e.do("GET", path, "", "", ""))), &c); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(c.GrantTypesSupported, []string{"client_credentials", grantExchange}) ||
			!slices.Equal(c.TokenEndpointAuthMethodsSupported, []string{"private_key_jwt", "none"}) {
			t.Errorf("%s: grants %v methods %v", path, c.GrantTypesSupported, c.TokenEndpointAuthMethodsSupported)
		}
	}
}

func TestTokenExchangeNeverLogsToken(t *testing.T) {
	f := newFedEnv(t, 0, nil)
	var secrets []string
	ok := f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric})
	status, body, m := f.post(exchangeForm(ok, nil))
	if status != 200 {
		t.Fatalf("exchange = %d %s", status, body)
	}
	secrets = append(secrets, ok, m["access_token"].(string))
	for _, tok := range []string{
		f.prov.Mint(map[string]any{"sub": "g-2", "email": "mallory@corp.example"}),            // users miss
		f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric, "aud": "other"}),          // verify failure
		f.prov.Mint(map[string]any{"iss": "https://nobody", "sub": "g-3", "email": corpEric}), // unknown issuer
	} {
		if status, _, _ := f.post(exchangeForm(tok, nil)); status != 400 {
			t.Fatalf("refusal status %d", status)
		}
		secrets = append(secrets, tok)
	}
	logs := f.log.String()
	if !strings.Contains(logs, "corp") {
		t.Errorf("no refusal was logged with the issuer name:\n%s", logs)
	}
	for _, s := range secrets {
		if strings.Contains(logs, s) {
			t.Errorf("log carries a token")
		}
		// No segment of a token either (the payload alone is enough to leak).
		for _, seg := range strings.Split(s, ".") {
			if len(seg) > 20 && strings.Contains(logs, seg) {
				t.Errorf("log carries a token segment")
			}
		}
	}
}

// TestTokenExchangeLogsReassignedEmail covers spec 4.2: a refusal for a
// changed subject is logged with the identity, never the token.
func TestTokenExchangeLogsReassignedEmail(t *testing.T) {
	f := newFedEnv(t, 0, nil)
	if s, b, _ := f.post(exchangeForm(f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric}), nil)); s != 200 {
		t.Fatalf("first sign-in = %d %s", s, b)
	}
	tok := f.prov.Mint(map[string]any{"sub": "g-other", "email": corpEric})
	if s, _, m := f.post(exchangeForm(tok, nil)); s != 400 || m["error"] != "invalid_request" {
		t.Fatalf("reassigned email = %d %v", s, m)
	}
	logs := f.log.String()
	if !strings.Contains(logs, "different subject") || !strings.Contains(logs, corpEric) {
		t.Errorf("reassigned-email refusal not logged with reason and identity:\n%s", logs)
	}
	if strings.Contains(logs, tok) {
		t.Error("log carries the token")
	}
}

func TestTokenExchangeWithoutIssuer(t *testing.T) {
	e := newEnv(t, envOptions{})
	wantOAuthError(t, e.do("POST", "/v1/auth/token", "application/x-www-form-urlencoded", "",
		exchangeForm("x.y.z", nil).Encode()), 503, "temporarily_unavailable", "", "x.y.z")
}

func TestTokenExchangeClipsLoggedIssuer(t *testing.T) {
	f := newFedEnv(t, 0, nil)
	long := "https://" + strings.Repeat("x", 5000)
	tok := f.prov.Mint(map[string]any{"iss": long, "sub": "g-1", "email": corpEric})
	if s, _, _ := f.post(exchangeForm(tok, nil)); s != 400 {
		t.Fatalf("status %d", s)
	}
	logs := f.log.String()
	if !strings.Contains(logs, "unknown issuer") || len(logs) > 1500 || strings.Contains(logs, strings.Repeat("x", 300)) {
		t.Errorf("logged issuer not clipped (%d bytes)", len(logs))
	}
}

func TestClip(t *testing.T) {
	if clip("short") != "short" {
		t.Error("short changed")
	}
	got := clip(strings.Repeat("\u00e9", 300))
	if len(got) > maxLogged+3 || !utf8.ValidString(got) {
		t.Errorf("clip = %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}

func TestTokenExchangeExpiresInFollowsMint(t *testing.T) {
	// HumanLifetime 0 is token.DefaultHumanLifetime, not the issuer's own lifetime (1 h in the env).
	f := newFedEnv(t, 0, func(e *token.External) { e.HumanLifetime = 0 })
	_, body, m := f.post(exchangeForm(f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric}), nil))
	if m["expires_in"] != token.DefaultHumanLifetime.Seconds() {
		t.Fatalf("expires_in = %v, want the default (%s)", m["expires_in"], body)
	}
}
