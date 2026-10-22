package cmd

import (
	"context"
	"crypto"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// adminOnce runs RunAdmin against a SQLite file with a nil environment.
func adminOnce(t *testing.T, file string, args ...string) {
	t.Helper()
	var o, e syncBuf
	if code := RunAdmin(context.Background(), append([]string{"-sqlite", file}, args...), nil, &o, &e); code != 0 {
		t.Fatalf("RunAdmin %v: exit %d, stderr %q", args, code, e.String())
	}
}

func TestRunLoadsConfigFileEnvAndFlags(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server.json")
	file, _ := json.Marshal(map[string]any{
		"listen": "127.0.0.1:0", "sqlite": filepath.Join(dir, "ab2.db"),
		"token_lifetime_seconds": 7200, "registration": "closed",
	})
	if err := os.WriteFile(cfgPath, file, 0o600); err != nil {
		t.Fatal(err)
	}

	// The file alone: closed, 7200.
	r := startRun(t, nil, "-config", cfgPath, "-registration", "open")
	if r.mode != "open" {
		t.Fatalf("flag did not beat file: mode %q", r.mode)
	}
	_, tok := registerAndMint(t, r, "pat")
	if tok.ExpiresIn != 7200*time.Second {
		t.Fatalf("expires_in %v, want the file's 7200 s", tok.ExpiresIn)
	}
	r.stop()

	// Environment beats the file; flags beat the environment.
	env := []string{"AGENTBUS2_SERVER_REGISTRATION=enrollment"}
	e := startRun(t, env, "-config", cfgPath)
	if e.mode != "enrollment" {
		t.Fatalf("environment did not beat file: mode %q", e.mode)
	}
	e.stop()

	f := startRun(t, env, "-config", cfgPath, "-set", "token_lifetime_seconds=60", "-registration", "open")
	if f.mode != "open" || !strings.Contains(f.stdout.String(), "registration open") {
		t.Fatalf("flag did not beat environment: %q", f.stdout.String())
	}
	_, tok = registerAndMint(t, f, "kim")
	if tok.ExpiresIn != 60*time.Second {
		t.Fatalf("expires_in %v, want 60 s", tok.ExpiresIn)
	}
}

func TestRunRejectsConfigErrors(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "ab2.db")
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"colour": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.json")
	sock := "unix:" + filepath.Join(dir, "s.sock")
	cases := []struct {
		name    string
		environ []string
		args    []string
		want    []string
	}{
		{"unknown key in file", nil, []string{"-config", bad, "-listen", "127.0.0.1:0", "-sqlite", db}, []string{"colour"}},
		{"unknown -set", nil, []string{"-listen", "127.0.0.1:0", "-sqlite", db, "-set", "colour=1"}, []string{"colour"}},
		{"unknown env", []string{"AGENTBUS2_SERVER_COLOUR=1"}, []string{"-listen", "127.0.0.1:0", "-sqlite", db}, []string{"colour"}},
		{"both stores", nil, []string{"-listen", "127.0.0.1:0", "-sqlite", db, "-postgres", "postgres://x"}, []string{"exactly one"}},
		{"missing config", nil, []string{"-config", missing, "-listen", "127.0.0.1:0", "-sqlite", db}, []string{"missing.json"}},
		{"no listen", nil, []string{"-sqlite", db}, []string{"listen is required"}},
		{"unix without issuer", nil, []string{"-listen", sock, "-sqlite", db}, []string{"issuer is required when listen is a unix socket", "listen", "issuer"}},
		{"unknown mode flag", nil, []string{"-listen", "127.0.0.1:0", "-sqlite", db, "-registration", "tofu"}, []string{"registration", "tofu"}},
		{"unknown mode env", []string{"AGENTBUS2_SERVER_REGISTRATION=tofu"}, []string{"-listen", "127.0.0.1:0", "-sqlite", db}, []string{"registration", "tofu"}},
		{"unknown mode file", nil, nil, []string{"registration", "tofu"}},
		{"jwks refresh below bound", nil, []string{"-listen", "127.0.0.1:0", "-sqlite", db, "-set", "jwks_refresh_seconds=5"}, []string{"jwks_refresh_seconds"}},
		{"embedding key file and env", nil, []string{"-listen", "127.0.0.1:0", "-sqlite", db,
			"-set", "embedding_endpoint=http://x/v1", "-set", "embedding_model=m",
			"-set", "embedding_api_key_file=/x", "-set", "embedding_api_key_env=Y"}, []string{"embedding_api_key"}},
	}
	for _, tc := range cases {
		args := tc.args
		if tc.name == "unknown mode file" {
			p := filepath.Join(dir, "tofu.json")
			if err := os.WriteFile(p, []byte(`{"registration":"tofu"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			args = []string{"-config", p, "-listen", "127.0.0.1:0", "-sqlite", db}
		}
		code, out, errs := runOnce(tc.environ, args...)
		if code != 2 {
			t.Errorf("%s: exit %d, want 2 (stdout %q stderr %q)", tc.name, code, out, errs)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(errs, w) {
				t.Errorf("%s: stderr %q lacks %q", tc.name, errs, w)
			}
		}
	}
}

// ---- external issuers (Decisions 57 and 85) ----

type extIDP struct {
	*httptest.Server
	fetches atomic.Int32
	es, ed  crypto.Signer
}

func newExtIDP(t *testing.T) *extIDP {
	t.Helper()
	p := &extIDP{}
	var err error
	if p.es, err = keys.Generate(keys.ES256); err != nil {
		t.Fatal(err)
	}
	if p.ed, err = keys.Generate(keys.EdDSA); err != nil {
		t.Fatal(err)
	}
	var set jose.JSONWebKeySet
	for _, k := range []struct {
		s   crypto.Signer
		alg string
	}{{p.es, "ES256"}, {p.ed, "EdDSA"}} {
		kid, _ := keys.KID(k.s.Public())
		set.Keys = append(set.Keys, jose.JSONWebKey{Key: k.s.Public(), KeyID: kid, Algorithm: k.alg, Use: "sig"})
	}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p.fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *extIDP) token(t *testing.T, k crypto.Signer, aud, sub string) string {
	t.Helper()
	kid, _ := keys.KID(k.Public())
	alg := jose.EdDSA
	if a, _ := keys.AlgorithmOf(k.Public()); a == keys.ES256 {
		alg = jose.ES256
	}
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: k}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw, err := jwt.Signed(s).Claims(jwt.Claims{
		Issuer: p.URL, Subject: sub, Audience: jwt.Audience{aud}, ID: sub + "-" + aud,
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Hour)),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (p *extIDP) issuersJSON() string {
	return `[{"issuer":"` + p.URL + `","jwks_uri":"` + p.URL + `/jwks","audience":"agentbus2","algorithms":["ES256"],"subjects":{"sub-1":"pat","sub-2":"kim"}}]`
}

func identityRow(t *testing.T, file, name string) (store.Identity, bool) {
	t.Helper()
	st, err := sqlite.Open(context.Background(), file, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var row store.Identity
	err = st.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = tx.Identity(ctx, name)
		return err
	})
	if err != nil {
		return store.Identity{}, false
	}
	return row, true
}

func TestRunWiresExternalIssuers(t *testing.T) {
	for _, via := range []string{"flag", "environment"} {
		t.Run("open/"+via, func(t *testing.T) {
			idp := newExtIDP(t)
			db := filepath.Join(t.TempDir(), "ab2.db")
			adminOnce(t, db, "create-identity", "pat")
			args := []string{"-listen", "127.0.0.1:0", "-sqlite", db}
			var environ []string
			if via == "flag" {
				args = append(args, "-set", "issuers="+idp.issuersJSON())
			} else {
				environ = []string{"AGENTBUS2_SERVER_ISSUERS=" + idp.issuersJSON()}
			}
			r := startRun(t, environ, args...)
			get := func(tok string) (int, []byte) { return httpDo(t, nil, "GET", r.base()+"/v1/channels", tok) }

			if code, body := get(idp.token(t, idp.es, "agentbus2", "sub-1")); code != 200 {
				t.Fatalf("ES256 sub-1: %d %s", code, body)
			}
			if code, _ := get(idp.token(t, idp.ed, "agentbus2", "sub-1")); code != 401 {
				t.Fatalf("Ed25519 token where algorithms is [ES256]: %d, want 401", code)
			}
			if code, _ := get(idp.token(t, idp.es, "agentbus2", "sub-9")); code != 401 {
				t.Fatalf("unmapped sub: %d, want 401", code)
			}
			if code, _ := get(idp.token(t, idp.es, "other", "sub-1")); code != 401 {
				t.Fatalf("wrong audience: %d, want 401", code)
			}
			code, body := httpDo(t, nil, "GET", r.base()+"/v1/discover", "")
			var d struct {
				Issuers []string `json:"issuers"`
			}
			if code != 200 || json.Unmarshal(body, &d) != nil || len(d.Issuers) != 1 || d.Issuers[0] != idp.URL {
				t.Fatalf("discover issuers: %d %s", code, body)
			}

			// Open mode creates the identity of a valid external token.
			if _, ok := identityRow(t, db, "kim"); ok {
				t.Fatal("kim exists before any token")
			}
			if code, body := get(idp.token(t, idp.es, "agentbus2", "sub-2")); code != 200 {
				t.Fatalf("open mode, sub-2: %d %s", code, body)
			}
			if row, ok := identityRow(t, db, "kim"); !ok || row.Admin {
				t.Fatalf("kim row %+v exists %v, want a non-admin identity", row, ok)
			}
			if n := idp.fetches.Load(); n != 1 {
				t.Fatalf("JWKS fetched %d times, want 1 (jwks_refresh_seconds holds the cache)", n)
			}
		})
	}

	t.Run("enrollment", func(t *testing.T) {
		idp := newExtIDP(t)
		db := filepath.Join(t.TempDir(), "ab2.db")
		r := startRun(t, nil, "-listen", "127.0.0.1:0", "-registration", "enrollment", "-sqlite", db, "-set", "issuers="+idp.issuersJSON())
		tok := idp.token(t, idp.es, "agentbus2", "sub-2")
		code, body := httpDo(t, nil, "GET", r.base()+"/v1/channels", tok)
		if code != 403 || problemCode(t, body) != "not_registered" {
			t.Fatalf("enrollment mode, unregistered: %d %s", code, body)
		}
		adminOnce(t, db, "create-identity", "kim")
		if code, body := httpDo(t, nil, "GET", r.base()+"/v1/channels", tok); code != 200 {
			t.Fatalf("after create-identity: %d %s", code, body)
		}
	})

	t.Run("jwks_refresh_seconds bound", func(t *testing.T) {
		db := filepath.Join(t.TempDir(), "ab2.db")
		r := startRun(t, nil, "-listen", "127.0.0.1:0", "-sqlite", db, "-set", "jwks_refresh_seconds=10")
		r.stop()
		code, _, errs := runOnce(nil, "-listen", "127.0.0.1:0", "-sqlite", db, "-set", "jwks_refresh_seconds=5")
		if code != 2 || !strings.Contains(errs, "jwks_refresh_seconds") {
			t.Fatalf("exit %d, stderr %q", code, errs)
		}
	})
}

func TestRunChannelCreationSwitch(t *testing.T) {
	create := func(t *testing.T, r *running, tok string) error {
		t.Helper()
		b := remote.New(genClient(t, r.base(), nil, genBearer(tok)))
		_, err := b.CreateChannel(context.Background(), bus.CreateChannelInput{Name: "dev", Kind: bus.KindOrdinary})
		return err
	}

	t.Run("admins", func(t *testing.T) {
		db := filepath.Join(t.TempDir(), "ab2.db")
		r := startRun(t, nil, "-listen", "127.0.0.1:0", "-sqlite", db, "-set", "channel_creation=admins")
		_, tok := registerAndMint(t, r, "pat")
		wantBusCode(t, create(t, r, tok.Token), bus.CodeForbidden)
		// The flag is read per request: promoting pat takes effect at once.
		adminOnce(t, db, "make-admin", "pat")
		if err := create(t, r, tok.Token); err != nil {
			t.Fatalf("admin create: %v", err)
		}
	})
	t.Run("open", func(t *testing.T) {
		r := startRun(t, nil, "-listen", "127.0.0.1:0", "-sqlite", filepath.Join(t.TempDir(), "ab2.db"), "-set", "channel_creation=open")
		_, tok := registerAndMint(t, r, "pat")
		if err := create(t, r, tok.Token); err != nil {
			t.Fatalf("open create: %v", err)
		}
	})
}

func genBearer(token string) gen.ClientOption {
	return gen.WithRequestEditorFn(remote.WithBearer(token))
}

// TestRunWarnsOnPlainHTTPJWKS: a plain-http jwks_uri on a non-loopback host
// starts the server with a warning on stderr naming the entry and the URI.
func TestRunWarnsOnPlainHTTPJWKS(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ab2.db")
	uri := "http://idp.example/jwks"
	issuers := `[{"issuer":"https://idp.example","jwks_uri":"` + uri + `","audience":"a","subjects":{"s":"pat"}}]`
	r := runServer(t, "-listen", "127.0.0.1:0", "-sqlite", db, "-set", "issuers="+issuers)
	if got := r.stderr.String(); !strings.Contains(got, "level=WARN") || !strings.Contains(got, "issuers: entry 0") || !strings.Contains(got, uri) {
		t.Fatalf("stderr lacks the jwks_uri warning: %q", got)
	}
}

// TestServerRefusesReadableSQLite: both `server` and `server admin` refuse a
// SQLite file readable by group or others, naming the file (post-build
// decision 8).
func TestServerRefusesReadableSQLite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix mode bits")
	}
	db := filepath.Join(t.TempDir(), "ab2.db")
	adminOnce(t, db, "create-identity", "pat")
	if err := os.Chmod(db, 0o644); err != nil {
		t.Fatal(err)
	}
	var o, e syncBuf
	if code := Run(context.Background(), []string{"-listen", "127.0.0.1:0", "-sqlite", db}, nil, &o, &e); code == 0 || !strings.Contains(e.String(), db) {
		t.Errorf("server: exit %d stderr %q, want non-zero naming %s", code, e.String(), db)
	}
	var ao, ae syncBuf
	if code := RunAdmin(context.Background(), []string{"-sqlite", db, "create-identity", "kim"}, nil, &ao, &ae); code == 0 || !strings.Contains(ae.String(), db) {
		t.Errorf("server admin: exit %d stderr %q, want non-zero naming %s", code, ae.String(), db)
	}
}
