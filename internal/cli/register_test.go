package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/server/cmd"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

func TestRegisterCreatesServerProfile(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)

	start := func(t *testing.T, db string, extra ...string) string {
		t.Helper()
		return startServer(t, db, extra...)
	}
	// service opens a second handle on the server's SQLite file.
	service := func(t *testing.T, db string) identities.Service {
		t.Helper()
		st, err := sqlite.Open(ctx, db, sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		c, err := core.New(st, core.Defaults(), core.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return identities.Service{Core: c}
	}
	register := func(args ...string) (int, string, string) {
		var out, errb syncBuf
		code := RunRegister(ctx, args, e.Environ, nil, &out, &errb)
		return code, out.String(), errb.String()
	}

	db := filepath.Join(t.TempDir(), "srv.sqlite")
	base := start(t, db)
	svc := service(t, db)
	credDir := profile.CredentialDir(e.ConfigDir, "lab")

	code, out, errs := register("-profile", "lab", "-server", base, "-identity", "pat")
	if code != 0 || out != "registered pat at "+base+" (created)\n" {
		t.Fatalf("register: code %d, stdout %q, stderr %q", code, out, errs)
	}
	cfg, err := profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Profiles["lab"]; p.Mode != profile.ModeServer || p.Server != base || p.Identity != "pat" {
		t.Fatalf("profile = %+v", p)
	}
	if _, ok := cfg.Profiles["default"]; !ok {
		t.Fatal("the existing default profile was lost")
	}
	credPath := filepath.Join(credDir, "pat")
	if fi, err := os.Stat(credPath); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode %o", fi.Mode().Perm())
	}
	signer, alg, err := credential.Store{Dir: credDir}.Load("pat")
	if err != nil || alg != keys.EdDSA {
		t.Fatalf("credential: %v %s", err, alg)
	}
	kid, err := keys.KID(signer.Public())
	if err != nil {
		t.Fatal(err)
	}
	if _, a, err := svc.PublicKey(ctx, "pat", kid); err != nil || a != keys.EdDSA {
		t.Fatalf("the server does not list the key %s: %v", kid, err)
	}

	// A second run reuses the saved key: exit 0, "existing key", same file.
	before, _ := os.ReadFile(credPath)
	code, out, errs = register("-profile", "lab", "-identity", "pat")
	if code != 0 || out != "registered pat at "+base+" (existing key)\n" {
		t.Fatalf("second register: code %d, stdout %q, stderr %q", code, out, errs)
	}
	if after, _ := os.ReadFile(credPath); string(after) != string(before) {
		t.Fatal("the second run replaced the credential")
	}
	// The identity is inferred from the profile on the next run too.
	if code, _, errs = register("-profile", "lab"); code != 0 {
		t.Fatalf("third register: code %d %q", code, errs)
	}
	// Asking for another algorithm over a saved key is a usage error, not a
	// silent rotation.
	if code, _, errs = register("-profile", "lab", "-algorithm", "ES256"); code != 2 || !strings.Contains(errs, "EdDSA") {
		t.Fatalf("algorithm clash: code %d %q", code, errs)
	}

	// ES256 as kim.
	if code, out, errs = register("-profile", "lab", "-identity", "kim", "-algorithm", "ES256"); code != 0 {
		t.Fatalf("kim: code %d %q %q", code, out, errs)
	}
	if _, a, err := (credential.Store{Dir: credDir}).Load("kim"); err != nil || a != keys.ES256 {
		t.Fatalf("kim's key: %v %s", err, a)
	}

	// Unrecognized values and arguments.
	if code, _, errs = register("-profile", "lab", "-identity", "zed", "-algorithm", "RSA"); code != 2 || !strings.Contains(errs, "EdDSA or ES256") {
		t.Fatalf("RSA: code %d %q", code, errs)
	}
	if (credential.Store{Dir: credDir}).Exists("zed") {
		t.Fatal("a refused algorithm left a credential")
	}
	if code, _, _ = register("-profile", "lab", "-algorithm", ""); code != 2 {
		t.Fatalf("empty algorithm: code %d", code)
	}
	if code, _, _ = register("-profile", "lab", "extra"); code != 2 {
		t.Fatalf("extra argument: code %d", code)
	}
	if code, _, errs = register("-profile", "lab", "-server", "http://elsewhere:1"); code != 2 || !strings.Contains(errs, "disagrees") {
		t.Fatalf("server clash: code %d %q", code, errs)
	}
	if code, _, errs = register("-profile", "nosuch"); code != 2 || !strings.Contains(errs, "-server") {
		t.Fatalf("undefined profile: code %d %q", code, errs)
	}
	if code, _, errs = register("-profile", "fresh", "-server", base); code != 2 || !strings.Contains(errs, "identity is required") {
		t.Fatalf("missing identity: code %d %q", code, errs)
	}
	cfg, _ = profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if _, ok := cfg.Profiles["fresh"]; ok {
		t.Fatal("a refused register created the profile")
	}

	// A local profile needs no registration.
	if code, _, errs = register("-profile", "default"); code != 2 || !strings.Contains(errs, "register applies to server profiles only; a local profile needs no registration") {
		t.Fatalf("local: code %d %q", code, errs)
	}
	if code, _, _ = register("-profile", "default", "-server", base); code != 2 {
		t.Fatalf("local with -server: code %d", code)
	}

	// An unreachable server fails with exit 1 and writes nothing.
	if code, _, errs = register("-profile", "down", "-server", "http://127.0.0.1:1", "-identity", "pat"); code != 1 {
		t.Fatalf("unreachable: code %d %q", code, errs)
	}
	if ids, _ := (credential.Store{Dir: profile.CredentialDir(e.ConfigDir, "down")}).List(); len(ids) != 0 {
		t.Fatalf("a failed registration left a credential: %v", ids)
	}
	cfg, _ = profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if _, ok := cfg.Profiles["down"]; ok {
		t.Fatal("a failed registration created the profile")
	}

	// Enrollment mode: refused without a token, and nothing is written.
	db2 := filepath.Join(t.TempDir(), "enroll.sqlite")
	base2 := start(t, db2, "-registration", "enrollment")
	svc2 := service(t, db2)
	code, _, errs = register("-profile", "corp", "-server", base2, "-identity", "pat")
	if code != 1 || !strings.Contains(errs, "forbidden") {
		t.Fatalf("no token: code %d %q", code, errs)
	}
	if ids, _ := (credential.Store{Dir: profile.CredentialDir(e.ConfigDir, "corp")}).List(); len(ids) != 0 {
		t.Fatalf("a refused registration left a credential: %v", ids)
	}
	cfg, _ = profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if _, ok := cfg.Profiles["corp"]; ok {
		t.Fatal("a refused registration created the profile")
	}
	if code, _, errs = register("-profile", "corp", "-server", base2, "-identity", "pat", "-enrollment-token", "bogus"); code != 1 || !strings.Contains(errs, "forbidden") {
		t.Fatalf("bad token: code %d %q", code, errs)
	}
	tok, _, err := svc2.CreateEnrollmentToken(ctx, identities.TokenInput{})
	if err != nil {
		t.Fatal(err)
	}
	if code, out, errs = register("-profile", "corp", "-server", base2, "-identity", "pat", "-enrollment-token", tok); code != 0 {
		t.Fatalf("with token: code %d %q %q", code, out, errs)
	}
	if !(credential.Store{Dir: profile.CredentialDir(e.ConfigDir, "corp")}).Exists("pat") {
		t.Fatal("no credential after a successful enrollment")
	}
}

// startServer runs `agentbus2 server` on a loopback port over db and returns
// its base URL.
func startServer(t *testing.T, db string, extra ...string) string {
	t.Helper()
	ctx := context.Background()
	sctx, cancel := context.WithCancel(ctx)
	out, errb := &syncBuf{}, &syncBuf{}
	done := make(chan int, 1)
	args := append([]string{"-listen", "127.0.0.1:0", "-sqlite", db}, extra...)
	go func() { done <- cmd.Run(sctx, args, nil, out, errb) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("server did not stop")
		}
	})
	re := regexp.MustCompile(`listening on (\S+) `)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := re.FindStringSubmatch(out.String()); m != nil {
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

// TestRegisterPendingKeyReuse: the server commits the registration but the
// response is lost; the key survives as a pending file and the retry, with the
// same (now consumed) enrollment token, registers the same key.
func TestRegisterPendingKeyReuse(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	db := filepath.Join(t.TempDir(), "enroll.sqlite")
	base := startServer(t, db, "-registration", "enrollment")
	st, err := sqlite.Open(ctx, db, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c, err := core.New(st, core.Defaults(), core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := (identities.Service{Core: c}).CreateEnrollmentToken(ctx, identities.TokenInput{})
	if err != nil {
		t.Fatal(err)
	}

	// A proxy that forwards to the server and, on its first request, drops the
	// connection instead of relaying the answer. It records each request body.
	var mu sync.Mutex
	var bodies []string
	var dropped bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		drop := !dropped
		dropped = true
		mu.Unlock()
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, base+r.URL.RequestURI(), strings.NewReader(string(b)))
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if drop {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)

	register := func(args ...string) (int, string, string) {
		var out, errb syncBuf
		code := RunRegister(ctx, append([]string{"-profile", "lab", "-server", proxy.URL, "-identity", "pat", "-enrollment-token", tok}, args...), e.Environ, nil, &out, &errb)
		return code, out.String(), errb.String()
	}
	cs := credential.Store{Dir: profile.CredentialDir(e.ConfigDir, "lab")}

	code, _, errs := register()
	if code != 1 {
		t.Fatalf("lost response: code %d %q", code, errs)
	}
	if cs.Exists("pat") {
		t.Fatal("a failed run left a credential")
	}
	pk, _, ok, err := cs.LoadPending("pat")
	if err != nil || !ok {
		t.Fatalf("no pending key after a failed run: %v %v", ok, err)
	}
	if ids, _ := cs.List(); len(ids) != 0 {
		t.Fatalf("List shows the pending key: %v", ids)
	}

	// An algorithm that disagrees with the pending key is a usage error that
	// names the pending file.
	if code, _, errs = register("-algorithm", "ES256"); code != 2 || !strings.Contains(errs, cs.PendingPath("pat")) {
		t.Fatalf("algorithm clash: code %d %q", code, errs)
	}
	if len(bodies) != 1 {
		t.Fatalf("the refused run reached the server: %d requests", len(bodies))
	}

	code, out, errs := register()
	if code != 0 || !strings.HasSuffix(out, "(existing key)\n") {
		t.Fatalf("retry: code %d %q %q", code, out, errs)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatalf("the retry sent a different request:\n%v", bodies)
	}
	if !cs.Exists("pat") {
		t.Fatal("no credential after the retry")
	}
	if _, err := os.Stat(cs.PendingPath("pat")); !os.IsNotExist(err) {
		t.Fatalf("the pending file remains: %v", err)
	}
	got, _, err := cs.Load("pat")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := keys.KID(pk.Public())
	b, _ := keys.KID(got.Public())
	if a != b {
		t.Fatal("the credential is not the pending key")
	}
}

// TestRegisterPendingConflictAndStale: a pending key the server does not hold
// fails with a hint naming the pending file; a saved credential discards a
// stale pending file.
func TestRegisterPendingConflictAndStale(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	base := startServer(t, filepath.Join(t.TempDir(), "srv.sqlite"))
	register := func() (int, string, string) {
		var out, errb syncBuf
		code := RunRegister(ctx, []string{"-profile", "lab", "-server", base, "-identity", "pat"}, e.Environ, nil, &out, &errb)
		return code, out.String(), errb.String()
	}
	cs := credential.Store{Dir: profile.CredentialDir(e.ConfigDir, "lab")}
	if code, _, errs := register(); code != 0 {
		t.Fatalf("register: %d %q", code, errs)
	}
	// Another key is pending, and the credential is gone: the server holds the
	// first key, so the pending one conflicts.
	stale, _ := keys.Generate(keys.EdDSA)
	if err := cs.SavePending("pat", stale); err != nil {
		t.Fatal(err)
	}
	cred, _ := os.ReadFile(cs.Path("pat"))
	if err := os.Rename(cs.Path("pat"), cs.Path("pat")+".keep"); err != nil {
		t.Fatal(err)
	}
	code, _, errs := register()
	if code != 1 || !strings.Contains(errs, cs.PendingPath("pat")) || !strings.Contains(errs, "reset") {
		t.Fatalf("conflict: code %d %q", code, errs)
	}
	// With the credential back, the stale pending file is discarded.
	if err := os.Rename(cs.Path("pat")+".keep", cs.Path("pat")); err != nil {
		t.Fatal(err)
	}
	if code, _, errs = register(); code != 0 {
		t.Fatalf("with credential: %d %q", code, errs)
	}
	if _, err := os.Stat(cs.PendingPath("pat")); !os.IsNotExist(err) {
		t.Fatalf("stale pending file remains: %v", err)
	}
	if after, _ := os.ReadFile(cs.Path("pat")); string(after) != string(cred) {
		t.Fatal("the credential changed")
	}
}
