package cmd

import (
	"bytes"
	"context"
	"crypto"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/ericfitz/agentbus2/internal/auth/assertion"
	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/core/notify/pgnotify"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/server"
	"github.com/ericfitz/agentbus2/internal/server/config"
)

// syncBuf is a bytes.Buffer safe for a writer goroutine and a polling reader.
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

var listeningRE = regexp.MustCompile(`agentbus2 server listening on (\S+) \((sqlite|postgres), registration (\S+), issuer (\S+), kid (\S+)\)`)

// running is one `agentbus2 server` started through Run in a goroutine.
type running struct {
	t                               *testing.T
	addr, engine, mode, issuer, kid string
	stdout, stderr                  *syncBuf
	cancel                          context.CancelFunc
	ch                              chan int
	finished                        chan struct{}
	stopOnce                        sync.Once
	code                            int
}

// launch starts Run and returns at once; waitReady blocks until the listening
// line. The test's cleanup cancels the server and waits for Run to return
// before TempDir removes the open database.
func launch(t *testing.T, environ []string, args ...string) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{t: t, stdout: &syncBuf{}, stderr: &syncBuf{}, cancel: cancel, ch: make(chan int, 1), finished: make(chan struct{})}
	go func() {
		r.ch <- Run(ctx, args, environ, r.stdout, r.stderr)
		close(r.finished)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.finished:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after the test ended")
		}
	})
	return r
}

func (r *running) waitReady() *running {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := listeningRE.FindStringSubmatch(r.stdout.String()); m != nil {
			r.addr, r.engine, r.mode, r.issuer, r.kid = m[1], m[2], m[3], m[4], m[5]
			return r
		}
		select {
		case <-r.finished:
			r.t.Fatalf("Run exited %d before listening: stdout %q stderr %q", <-r.ch, r.stdout.String(), r.stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	r.t.Fatalf("no listening line; stdout %q stderr %q", r.stdout.String(), r.stderr.String())
	return r
}

// stop cancels the server and returns Run's exit code.
func (r *running) stop() int {
	r.t.Helper()
	r.stopOnce.Do(func() {
		r.cancel()
		select {
		case r.code = <-r.ch:
		case <-time.After(15 * time.Second):
			r.t.Fatal("Run did not return after cancel")
		}
	})
	return r.code
}

func (r *running) base() string { return "http://" + r.addr }

func startRun(t *testing.T, environ []string, args ...string) *running {
	t.Helper()
	return launch(t, environ, args...).waitReady()
}

// runServer starts Run with a nil environment.
func runServer(t *testing.T, args ...string) *running {
	t.Helper()
	return startRun(t, nil, args...)
}

// runOnce runs Run to completion; it is for invocations that must fail.
func runOnce(environ []string, args ...string) (code int, stdout, stderr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var o, e bytes.Buffer
	code = Run(ctx, args, environ, &o, &e)
	return code, o.String(), e.String()
}

func httpDo(t *testing.T, hc *http.Client, method, url, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func problemCode(t *testing.T, body []byte) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("not a problem body: %q", body)
	}
	return p.Code
}

func genClient(t *testing.T, base string, hc *http.Client, opts ...gen.ClientOption) *gen.ClientWithResponses {
	t.Helper()
	if hc != nil {
		opts = append(opts, gen.WithHTTPClient(hc))
	}
	c, err := gen.NewClientWithResponses(base, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// registerKey registers a fresh Ed25519 key for name (open mode) and returns it.
func registerKey(t *testing.T, base string, hc *http.Client, name string) crypto.Signer {
	t.Helper()
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Register(context.Background(), genClient(t, base, hc), name, k.Public(), ""); err != nil {
		t.Fatalf("Register %s: %v", name, err)
	}
	return k
}

// mintToken exchanges a signed assertion for a bearer at issuer's token endpoint.
func mintToken(t *testing.T, base, issuer string, hc *http.Client, name string, k crypto.Signer) remote.AccessToken {
	t.Helper()
	kid, err := keys.KID(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	a, err := assertion.Sign(assertion.Input{Identity: name, Audience: issuer, Key: k, KID: kid, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := remote.Token(context.Background(), genClient(t, base, hc), a)
	if err != nil {
		t.Fatalf("Token %s: %v", name, err)
	}
	return tok
}

// registerAndMint registers name against r and returns its key and a bearer.
func registerAndMint(t *testing.T, r *running, name string) (crypto.Signer, remote.AccessToken) {
	t.Helper()
	k := registerKey(t, r.base(), nil, name)
	return k, mintToken(t, r.base(), r.issuer, nil, name, k)
}

func wantBusCode(t *testing.T, err error, want bus.Code) {
	t.Helper()
	var be *bus.Error
	if !errors.As(err, &be) || be.Code != want {
		t.Fatalf("error %v, want code %q", err, want)
	}
}

func TestRunFlags(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "ab2.db")
	cases := []struct {
		name string
		args []string
		errs []string
	}{
		{"none", nil, []string{"exactly one of sqlite or postgres is required"}},
		{"both stores", []string{"-listen", "127.0.0.1:0", "-sqlite", tmp, "-postgres", "postgres://x"}, []string{"exactly one of sqlite or postgres is required"}},
		{"no store", []string{"-listen", "127.0.0.1:0"}, []string{"exactly one of sqlite or postgres is required"}},
		{"no listen", []string{"-sqlite", tmp}, []string{"listen is required, for example 127.0.0.1:0 (set listen)"}},
		{"unknown flag", []string{"-frob"}, []string{"flag provided but not defined: -frob"}},
		{"stray argument", []string{"-listen", "127.0.0.1:0", "-sqlite", tmp, "extra"}, []string{`unexpected argument "extra"`}},
	}
	for _, tc := range cases {
		code, _, errs := runOnce(nil, tc.args...)
		if code != 2 {
			t.Fatalf("%s: exit %d, want 2 (stderr %q)", tc.name, code, errs)
		}
		for _, want := range tc.errs {
			if !strings.Contains(errs, want) {
				t.Errorf("%s: stderr %q lacks %q", tc.name, errs, want)
			}
		}
	}

	code, out, _ := runOnce(nil, "-h")
	if code != 0 {
		t.Fatalf("-h: exit %d", code)
	}
	for _, s := range config.Settings() {
		if !strings.Contains(out, s) {
			t.Errorf("-h usage lacks setting %q", s)
		}
	}
	for _, f := range []string{"-listen", "-sqlite", "-postgres", "-config", "-registration", "-set"} {
		if !strings.Contains(out, f) {
			t.Errorf("-h usage lacks flag %s", f)
		}
	}

	r := runServer(t, "-listen", "127.0.0.1:0", "-sqlite", tmp)
	if r.addr == "" || r.engine != "sqlite" || r.mode != "open" {
		t.Fatalf("stdout %q", r.stdout.String())
	}
	if got := r.stop(); got != 0 {
		t.Fatalf("clean shutdown exit %d", got)
	}
}

// The server logs through a slog logger on stderr (lease failures, job errors),
// and net/http's own errors go through it too.
func TestRunConfiguresLogging(t *testing.T) {
	var got Config
	var out, errb bytes.Buffer
	serve := func(_ context.Context, c Config) error { got = c; return nil }
	args := []string{"-listen", "127.0.0.1:0", "-sqlite", filepath.Join(t.TempDir(), "ab2.db")}
	if code := run(context.Background(), args, nil, &out, &errb, serve); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if got.Logger == nil {
		t.Fatal("Run passed no Logger: the server would log nothing")
	}
	got.Logger.Warn("lease trouble")
	if !strings.Contains(errb.String(), "lease trouble") {
		t.Fatalf("stderr %q lacks the logged line", errb.String())
	}
	if strings.Contains(out.String(), "lease trouble") {
		t.Fatalf("stdout %q: logs belong on stderr", out.String())
	}
	if got.Registration != "open" || got.Issuer == nil || got.Listener == nil {
		t.Fatalf("Config %+v: want registration open, an issuer and the bound listener", got)
	}
	if got.Notifier != nil {
		t.Fatalf("Notifier %T on sqlite, want nil (Serve's in-process Memory)", got.Notifier)
	}
}

// A Postgres store gets the LISTEN/NOTIFY listener (Decision 52), and the
// close function Run defers stops it; SQLite gets none. The listener never
// fails on an unreachable server, so this needs no database.
func TestStoreNotifier(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	n, closeN := storeNotifier(t.Context(), config.Config{SQLite: "ab2.db"}, logger)
	closeN()
	if n != nil {
		t.Fatalf("sqlite notifier %T, want nil", n)
	}
	n, closeN = storeNotifier(t.Context(), config.Config{Postgres: "postgres://127.0.0.1:1/none?connect_timeout=1"}, logger)
	if _, ok := n.(*pgnotify.Listener); !ok {
		closeN()
		t.Fatalf("postgres notifier %T, want *pgnotify.Listener", n)
	}
	done := make(chan struct{})
	go func() { closeN(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the listener did not return within 5 s")
	}
}

func TestRunRejectsUnopenableStore(t *testing.T) {
	dir := t.TempDir() // a directory is not a database file
	code, _, errs := runOnce(nil, "-listen", "127.0.0.1:0", "-sqlite", dir)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (stderr %q)", code, errs)
	}
	if !strings.Contains(errs, "opening the sqlite store") {
		t.Fatalf("stderr %q", errs)
	}
}

// The counts fail when a task declares an operation without updating this
// test. Every request carries a bad bearer: it must never turn an open
// operation into a 401 (Global Constraints). The one named exception is health
// (Eric's ruling, ADR 0010 decision 12): it counts as an open operation, with
// an optional bearer, but a presented invalid bearer on it is a 401 and no
// bearer is a 200.
func TestRunRefusesEveryProtectedOperation(t *testing.T) {
	r := runServer(t, "-listen", "127.0.0.1:0", "-sqlite", filepath.Join(t.TempDir(), "ab2.db"))
	doc := server.Spec()
	subst := strings.NewReplacer("{channel}", "dev", "{session}", "s", "{id}", "1", "{patterns}", "deploy", "{identity}", "pat", "{scope}", "job1")
	wantOpen := map[string]int{
		"token": 400, "register": 400, "jwks": 200, "discover": 200, "wellKnown": 200, "openidConfiguration": 200, "authorizationServerMetadata": 200,
	}
	protected, open := 0, 0
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			sec := doc.Security
			if op.Security != nil {
				sec = *op.Security
			}
			isHealth := op.OperationID == "Health" || op.OperationID == "health"
			isProtected := len(sec) > 0 && !isHealth
			url := r.base() + subst.Replace(path)
			if strings.Contains(url, "{") {
				t.Fatalf("%s %s: unsubstituted template parameter", method, path)
			}
			var body io.Reader
			if op.RequestBody != nil {
				body = strings.NewReader("{}")
			}
			req, err := http.NewRequest(method, url, body)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer x")
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if isProtected {
				protected++
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s %s: status %d, want 401: %s", method, path, resp.StatusCode, raw)
					continue
				}
				if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
					t.Errorf("%s %s: content type %q", method, path, ct)
				}
				if c := problemCode(t, raw); c != "unauthenticated" {
					t.Errorf("%s %s: code %q: %s", method, path, c, raw)
				}
				continue
			}
			open++
			if isHealth {
				// The named exception to "an open operation never answers 401".
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("health with a bad bearer: status %d, want 401: %s", resp.StatusCode, raw)
				} else if c := problemCode(t, raw); c != "unauthenticated" {
					t.Errorf("health with a bad bearer: code %q: %s", c, raw)
				}
				noBearer, err := http.Get(url)
				if err != nil {
					t.Fatal(err)
				}
				_ = noBearer.Body.Close()
				if noBearer.StatusCode != http.StatusOK {
					t.Errorf("health without a bearer: status %d, want 200", noBearer.StatusCode)
				}
				continue
			}
			want, ok := wantOpen[strings.ToLower(op.OperationID[:1])+op.OperationID[1:]]
			if !ok {
				t.Errorf("%s %s: open operation %q has no expectation", method, path, op.OperationID)
				continue
			}
			if resp.StatusCode != want {
				t.Errorf("%s %s (%s): status %d, want %d: %s", method, path, op.OperationID, resp.StatusCode, want, raw)
			}
		}
	}
	if protected != 46 || open != 8 || protected+open != 54 {
		t.Fatalf("operations: %d protected, %d open; want 46 and 8", protected, open)
	}
}

// jwksKIDs fetches /v1/auth/jwks and returns its kids.
func jwksKIDs(t *testing.T, base string) []string {
	t.Helper()
	code, body := httpDo(t, nil, "GET", base+"/v1/auth/jwks", "")
	if code != 200 {
		t.Fatalf("jwks: %d %s", code, body)
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range set.Keys {
		out = append(out, k.Kid)
	}
	return out
}

// Decision (Eric, 2026-10-21), item 2; Definition of Done D. The signing key
// is one Store row: a second start loads it; two starts at once end with one
// row (the deterministic proof is Task 5's TestIdentitiesSigningKeyConcurrent
// on both engines; this smoke may pass without the race occurring); a failed
// start leaves either nothing or the committed row, never half of one.
func TestSigningKeyStoreLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ab2.db")
	// A fixed issuer, so a token minted by one start verifies on the next.
	issuer := []string{"-set", "issuer=http://agentbus2.local"}
	args := append([]string{"-listen", "127.0.0.1:0", "-sqlite", path}, issuer...)

	first := runServer(t, args...)
	if want := "generated signing key " + first.kid; !strings.Contains(first.stdout.String(), want) {
		t.Fatalf("first start stdout %q lacks %q", first.stdout.String(), want)
	}
	if got := jwksKIDs(t, first.base()); len(got) != 1 || got[0] != first.kid {
		t.Fatalf("jwks kids %v, want [%s]", got, first.kid)
	}
	_, tok := registerAndMint(t, first, "pat")
	if got := first.stop(); got != 0 {
		t.Fatalf("exit %d", got)
	}

	// Second start over the same file: nothing generated, same kid, and the
	// first run's token verifies.
	second := runServer(t, args...)
	if strings.Contains(second.stdout.String(), "generated signing key") {
		t.Fatalf("second start generated a key: %q", second.stdout.String())
	}
	if second.kid != first.kid {
		t.Fatalf("second kid %s, first %s", second.kid, first.kid)
	}
	if got := jwksKIDs(t, second.base()); len(got) != 1 || got[0] != first.kid {
		t.Fatalf("second jwks kids %v", got)
	}
	if code, body := httpDo(t, nil, "GET", second.base()+"/v1/channels", tok.Token); code != 200 {
		t.Fatalf("token minted by the first run: %d %s", code, body)
	}
	second.stop()

	// Two servers started at once over one fresh file both start, report one
	// kid, and exactly one of them created the row.
	path2 := filepath.Join(t.TempDir(), "ab2.db")
	args2 := append([]string{"-listen", "127.0.0.1:0", "-sqlite", path2}, issuer...)
	a := launch(t, nil, args2...)
	b := launch(t, nil, args2...)
	a.waitReady()
	b.waitReady()
	if a.kid != b.kid {
		t.Fatalf("concurrent starts disagree on the kid: %s vs %s", a.kid, b.kid)
	}
	created := strings.Count(a.stdout.String(), "generated signing key") + strings.Count(b.stdout.String(), "generated signing key")
	if created != 1 {
		t.Fatalf("%d of the two starts generated a key, want exactly 1", created)
	}
	// A token minted by one verifies on the other.
	_, atok := registerAndMint(t, a, "kim")
	if code, body := httpDo(t, nil, "GET", b.base()+"/v1/channels", atok.Token); code != 200 {
		t.Fatalf("a's token on b: %d %s", code, body)
	}
	a.stop()
	b.stop()

	// A corrupt row stops the start with exit 1 naming the row, and is left alone.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("UPDATE signing_keys SET private_key_pem = 'garbage'"); err != nil {
		t.Fatal(err)
	}
	code, _, errs := runOnce(nil, args...)
	if code != 1 || !strings.Contains(errs, "signing key row") {
		t.Fatalf("corrupt row: exit %d, stderr %q", code, errs)
	}
	var pemText string
	if err := db.QueryRow("SELECT private_key_pem FROM signing_keys").Scan(&pemText); err != nil || pemText != "garbage" {
		t.Fatalf("the corrupt row was changed: %q, %v", pemText, err)
	}

	// signing_key_file is not a setting: the key is a Store row.
	code, _, errs = runOnce(nil, append(args, "-set", "signing_key_file=x")...)
	if code != 2 || !strings.Contains(errs, "signing_key_file") {
		t.Fatalf("signing_key_file: exit %d, stderr %q", code, errs)
	}
}

// A start that fails after the key row was committed leaves the row; the next
// start loads it and generates nothing. The failure is the embedding key,
// which Run reads after the signing key (the plan task 9 order).
func TestSigningKeyAfterFailedStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ab2.db")
	args := []string{"-listen", "127.0.0.1:0", "-sqlite", path}
	missing := filepath.Join(t.TempDir(), "no-such-key")
	code, out, errs := runOnce(nil, append(args, "-set", "embedding_endpoint=http://127.0.0.1:1/v1/embeddings",
		"-set", "embedding_model=m", "-set", "embedding_api_key_file="+missing)...)
	if code == 0 || !strings.Contains(errs, "embedding_api_key_file") {
		t.Fatalf("missing embedding key file: exit %d, stderr %q", code, errs)
	}
	m := regexp.MustCompile(`generated signing key (\S+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the failed start created no key row before failing: stdout %q", out)
	}
	r := runServer(t, args...)
	if strings.Contains(r.stdout.String(), "generated signing key") || r.kid != m[1] {
		t.Fatalf("the committed row was not reused: stdout %q, first kid %s", r.stdout.String(), m[1])
	}
	if got := jwksKIDs(t, r.base()); len(got) != 1 || got[0] != r.kid {
		t.Fatalf("jwks kids %v", got)
	}
}

// A database whose active key is a store row and a configuration naming an
// external key stop the start (exit 1) with the rotate instruction, and the
// row is left alone.
func TestSigningKeyProviderMismatchRefusesStart(t *testing.T) {
	keyBackends["awskms"] = func(string) (keyprovider.Provider, error) { return stubProvider{}, nil }
	t.Cleanup(func() { delete(keyBackends, "awskms") })
	path := filepath.Join(t.TempDir(), "ab2.db")
	args := []string{"-listen", "127.0.0.1:0", "-sqlite", path}
	first := runServer(t, args...)
	kid := first.kid
	if got := first.stop(); got != 0 {
		t.Fatalf("exit %d", got)
	}
	code, _, errs := runOnce(nil, append(args, "-set", "signing_key_uri=awskms://k")...)
	if code != 1 || !strings.Contains(errs, "signing key: ") || !strings.Contains(errs, "configured provider is awskms") ||
		!strings.Contains(errs, "signing-key rotate") {
		t.Fatalf("mismatch: exit %d, stderr %q", code, errs)
	}
	again := runServer(t, args...)
	defer again.stop()
	if again.kid != kid {
		t.Fatalf("kid changed from %s to %s", kid, again.kid)
	}
	// A scheme without a backend in this build is a configuration error (2).
	code, _, errs = runOnce(nil, append(args, "-set", "signing_key_uri=gcpkms://k")...)
	if code != 2 || !strings.Contains(errs, "the gcpkms backend is not in this build") {
		t.Fatalf("no backend: exit %d, stderr %q", code, errs)
	}
}

func TestRegistrationDefaultsByListener(t *testing.T) {
	// A non-loopback listener defaults to enrollment and warns about the issuer.
	r := runServer(t, "-listen", "0.0.0.0:0", "-sqlite", filepath.Join(t.TempDir(), "ab2.db"))
	_, port, _ := net.SplitHostPort(r.addr)
	if r.mode != "enrollment" {
		t.Fatalf("mode %q, want enrollment", r.mode)
	}
	if want := "issuer is unset: clients will see http://0.0.0.0:" + port; !strings.Contains(r.stdout.String(), want) {
		t.Fatalf("stdout %q lacks %q", r.stdout.String(), want)
	}
	k, _ := keys.Generate(keys.EdDSA)
	_, err := remote.Register(context.Background(), genClient(t, "http://127.0.0.1:"+port, nil), "pat", k.Public(), "")
	wantBusCode(t, err, bus.CodeForbidden)

	// Loopback defaults to open, no warning, and a tokenless register works.
	o := runServer(t, "-listen", "127.0.0.1:0", "-sqlite", filepath.Join(t.TempDir(), "ab2.db"))
	if o.mode != "open" || strings.Contains(o.stdout.String(), "issuer is unset") {
		t.Fatalf("loopback: mode %q, stdout %q", o.mode, o.stdout.String())
	}
	res, err := remote.Register(context.Background(), genClient(t, o.base(), nil), "pat", k.Public(), "")
	if err != nil || !res.Created {
		t.Fatalf("open register: %+v, %v", res, err)
	}

	// An explicit mode wins over the listener, both ways.
	c := runServer(t, "-listen", "127.0.0.1:0", "-registration", "enrollment", "-sqlite", filepath.Join(t.TempDir(), "ab2.db"))
	if c.mode != "enrollment" {
		t.Fatalf("mode %q, want enrollment", c.mode)
	}
	_, err = remote.Register(context.Background(), genClient(t, c.base(), nil), "pat", k.Public(), "")
	wantBusCode(t, err, bus.CodeForbidden)
	w := runServer(t, "-listen", "0.0.0.0:0", "-registration", "open", "-sqlite", filepath.Join(t.TempDir(), "ab2.db"))
	if w.mode != "open" || !strings.Contains(w.stdout.String(), "registration open") {
		t.Fatalf("-registration open on a non-loopback listener: mode %q, stdout %q", w.mode, w.stdout.String())
	}

	// The closed mode is gone: it is an unknown value like any other.
	code, _, errs := runOnce(nil, "-listen", "127.0.0.1:0", "-registration", "closed", "-sqlite", filepath.Join(t.TempDir(), "ab2.db"))
	if code != 2 || !strings.Contains(errs, "registration") {
		t.Fatalf("closed: exit %d, stderr %q", code, errs)
	}

	code, _, errs = runOnce(nil, "-listen", "127.0.0.1:0", "-registration", "tofu", "-sqlite", filepath.Join(t.TempDir(), "ab2.db"))
	if code != 2 || !strings.Contains(errs, "registration") {
		t.Fatalf("tofu: exit %d, stderr %q", code, errs)
	}
}

func TestRunOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	if len(sock) > 100 {
		t.Skipf("socket path %q is too long for sun_path", sock)
	}
	r := runServer(t, "-listen", "unix:"+sock, "-sqlite", filepath.Join(t.TempDir(), "ab2.db"), "-set", "issuer=http://agentbus2.local")
	if r.addr != sock || r.mode != "open" || r.issuer != "http://agentbus2.local" {
		t.Fatalf("listening line: addr %q mode %q issuer %q", r.addr, r.mode, r.issuer)
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	base := "http://agentbus2.local"
	k := registerKey(t, base, hc, "pat")
	tok := mintToken(t, base, base, hc, "pat", k)
	if code, body := httpDo(t, hc, "GET", base+"/v1/channels", tok.Token); code != 200 {
		t.Fatalf("channels over the socket: %d %s", code, body)
	}
	code, body := httpDo(t, hc, "GET", base+"/.well-known/openid-configuration", "")
	var oc struct {
		Issuer string `json:"issuer"`
	}
	if code != 200 || json.Unmarshal(body, &oc) != nil || oc.Issuer != base {
		t.Fatalf("openid-configuration: %d %s", code, body)
	}
}

// externalIssuers carries the name, algorithms, claim names, the human
// lifetime and jwks_refresh_seconds into token.External; the 300 s default of
// the TTL would hide a dropped value.
func TestExternalIssuersMapping(t *testing.T) {
	cfg := config.Default()
	cfg.JWKSRefreshSeconds = 42
	cfg.Issuers = []config.Issuer{
		{Name: "a", Issuer: "https://a", JWKSURI: "https://a/jwks", Audience: "x", Algorithms: []string{"ES256", "RS256"},
			Claims: config.IssuerClaims{Email: "mail", Subject: "user_id"}, HumanTokenLifetimeSeconds: 600},
		{Name: "b", Issuer: "https://b", JWKSURI: "https://b/jwks", Audience: "y"},
	}
	got := externalIssuers(cfg)
	if len(got) != 2 {
		t.Fatalf("%d externals, want 2", len(got))
	}
	if a := got[0]; a.Name != "a" || a.Issuer != "https://a" || a.JWKSURI != "https://a/jwks" || a.Audience != "x" ||
		a.TTL != 42*time.Second || len(a.Algorithms) != 2 || a.Algorithms[0] != jose.ES256 || a.Algorithms[1] != jose.RS256 ||
		a.Claims != (token.ClaimNames{Email: "mail", Subject: "user_id"}) || a.HumanLifetime != 600*time.Second {
		t.Fatalf("first external %+v", a)
	}
	if b := got[1]; b.Name != "b" || b.Algorithms != nil || b.TTL != 42*time.Second ||
		b.Claims != (token.ClaimNames{}) || b.HumanLifetime != 0 || b.EffectiveHumanLifetime() != token.DefaultHumanLifetime {
		t.Fatalf("second external %+v: want nil algorithms (the verifier's default), the TTL, and claims and lifetime left to token's defaults", b)
	}
}

// TestRunSlackTokenFiles: the Slack token files are read before the store is
// opened; a missing file or a token of the wrong shape exits 2 naming the
// setting and never echoing file content, and valid files reach Serve as a
// client and a bridge Config.
func TestRunSlackTokenFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	app := write("app", "xapp-1-SECRETAPP\n")
	bot := write("bot", " xoxb-SECRETBOT \n")
	badApp := write("badapp", "xoxb-SECRETWRONGAPP")
	badBot := write("badbot", "xapp-SECRETWRONGBOT")
	missing := filepath.Join(dir, "absent")
	base := func() []string {
		return []string{"-listen", "127.0.0.1:0", "-sqlite", filepath.Join(t.TempDir(), "ab2.db")}
	}
	set := func(kv ...string) []string {
		a := base()
		for _, x := range kv {
			a = append(a, "-set", x)
		}
		return a
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing bot file", set("slack_app_token_file="+app, "slack_bot_token_file="+missing), "slack_bot_token_file"},
		{"missing app file", set("slack_app_token_file="+missing, "slack_bot_token_file="+bot), "slack_app_token_file"},
		{"app token without xapp-", set("slack_app_token_file="+badApp, "slack_bot_token_file="+bot), "slack_app_token_file"},
		{"bot token without xoxb-", set("slack_app_token_file="+app, "slack_bot_token_file="+badBot), "slack_bot_token_file"},
		{"one file alone", set("slack_app_token_file=" + app), "slack_app_token_file and slack_bot_token_file must be set together"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			served := false
			var out, errb bytes.Buffer
			code := run(context.Background(), tc.args, nil, &out, &errb, func(context.Context, Config) error { served = true; return nil })
			if code != 2 || served || !strings.Contains(errb.String(), tc.want) {
				t.Fatalf("exit %d (served %v), stderr %q; want exit 2 naming %q", code, served, errb.String(), tc.want)
			}
			if strings.Contains(errb.String()+out.String(), "SECRET") {
				t.Errorf("output echoes file content: %q %q", out.String(), errb.String())
			}
		})
	}

	t.Run("valid files", func(t *testing.T) {
		var got Config
		var out, errb bytes.Buffer
		code := run(context.Background(), set("slack_app_token_file="+app, "slack_bot_token_file="+bot, "slack_archive_after_done_hours=5", "slack_max_message_chars=500"),
			nil, &out, &errb, func(_ context.Context, c Config) error { got = c; return nil })
		if code != 0 {
			t.Fatalf("exit %d, stderr %q", code, errb.String())
		}
		if got.Slack == nil || got.SlackCfg.ArchiveAfterDone != 5*time.Hour || got.SlackCfg.MaxMessageChars != 500 {
			t.Errorf("Config = %+v", got)
		}
	})
	t.Run("no Slack settings", func(t *testing.T) {
		var got Config
		var out, errb bytes.Buffer
		if code := run(context.Background(), base(), nil, &out, &errb, func(_ context.Context, c Config) error { got = c; return nil }); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, errb.String())
		}
		if got.Slack != nil {
			t.Error("a Slack client was built without settings")
		}
	})
}
