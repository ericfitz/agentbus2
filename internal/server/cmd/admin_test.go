package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// adminEnv is one SQLite file per test plus a way to run RunAdmin against it.
type adminEnv struct {
	t    *testing.T
	file string
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	return &adminEnv{t: t, file: filepath.Join(t.TempDir(), "bus.db")}
}

// run executes RunAdmin with -sqlite <file> and the given arguments, with a
// nil environment.
func (e *adminEnv) run(args ...string) (code int, out, errOut string) {
	e.t.Helper()
	return e.runEnv(nil, append([]string{"-sqlite", e.file}, args...)...)
}

func (e *adminEnv) runEnv(environ []string, args ...string) (code int, out, errOut string) {
	e.t.Helper()
	var o, er bytes.Buffer
	code = RunAdmin(context.Background(), args, environ, &o, &er)
	return code, o.String(), er.String()
}

// service opens a second handle on the same file.
func (e *adminEnv) service() (identities.Service, store.Store) {
	e.t.Helper()
	st, err := sqlite.Open(context.Background(), e.file, sqlite.Options{})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = st.Close() })
	c, err := core.New(st, core.Defaults(), core.Options{})
	if err != nil {
		e.t.Fatal(err)
	}
	return identities.Service{Core: c}, st
}

func readStore[T any](t *testing.T, st store.Store, f func(context.Context, store.Tx) (T, error)) T {
	t.Helper()
	var out T
	err := st.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		out, err = f(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAdminUsersAdminsCommands(t *testing.T) {
	e := newAdminEnv(t)
	if code, _, errOut := e.run("admins", "add", "-identity", "pat"); code != 1 || !strings.Contains(errOut, "register it first") {
		t.Fatalf("admins add before pat exists: code %d stderr %q", code, errOut)
	}
	if code, _, errOut := e.run("create-identity", "pat"); code != 0 {
		t.Fatalf("create-identity: %d %s", code, errOut)
	}
	_, st := e.service()
	isAdmin := func() bool {
		return readStore(t, st, func(ctx context.Context, tx store.Tx) (bool, error) {
			id, err := tx.Identity(ctx, "pat")
			return id.Admin, err
		})
	}
	for i, want := range []string{"added", "already present"} {
		code, out, errOut := e.run("admins", "add", "-identity", "pat")
		if code != 0 || strings.TrimSpace(out) != want || !isAdmin() {
			t.Fatalf("admins add %d: code %d out %q stderr %q admin %v", i, code, out, errOut, isAdmin())
		}
	}
	if code, out, _ := e.run("admins", "list"); code != 0 || strings.TrimSpace(out) != "{identity: pat}" {
		t.Fatalf("admins list: %d %q", code, out)
	}
	if code, out, _ := e.run("users", "list"); code != 0 || out != "" {
		t.Fatalf("users list: %d %q", code, out)
	}
	// No issuer is configured yet: a well-formed claim add is a service error.
	if code, _, errOut := e.run("users", "add", "-issuer", "corp", "-group", "x"); code != 1 || !strings.Contains(errOut, "unknown issuer") {
		t.Fatalf("users add group: code %d stderr %q", code, errOut)
	}
	for _, bad := range [][]string{
		{"admins", "add", "-group", "x"},
		{"admins", "add", "-identity", "pat", "-email", "a@b"},
		{"admins", "add", "-identity", "pat", "-issuer", "corp"},
		{"admins", "add"},
		{"admins", "add", "-email", "nope", "-issuer", "corp"},
		{"admins", "frob"},
		{"admins"},
		{"admins", "list", "extra"},
		{"admins", "list", "-identity", "pat"},
	} {
		if code, _, errOut := e.run(bad...); code != 2 || errOut == "" {
			t.Errorf("%v: code %d stderr %q, want 2 with a message", bad, code, errOut)
		}
	}
	for i, want := range []string{"removed", "not present"} {
		code, out, _ := e.run("admins", "remove", "-identity", "pat")
		if code != 0 || strings.TrimSpace(out) != want || isAdmin() {
			t.Fatalf("admins remove %d: code %d out %q admin %v", i, code, out, isAdmin())
		}
	}
}

func TestAdminMakeAdminReplaced(t *testing.T) {
	e := newAdminEnv(t)
	code, out, errOut := e.run("make-admin", "pat")
	if code != 2 || out != "" || !strings.Contains(errOut, "make-admin was replaced by: admins add -identity NAME (admins remove -identity NAME)") {
		t.Fatalf("make-admin: code %d out %q stderr %q", code, out, errOut)
	}
}

func TestAdminResetCredential(t *testing.T) {
	e := newAdminEnv(t)
	svc, st := e.service()
	if err := svc.RegisterLocal(context.Background(), "pat"); err != nil {
		t.Fatal(err)
	}

	code, out, _ := e.run("reset-credential", "pat")
	if code != 0 || !strings.Contains(out, "0 credential(s)") {
		t.Fatalf("reset-credential (none): code %d out %q", code, out)
	}
	err := st.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.PutPublicKey(ctx, store.PublicKey{Identity: "pat", KID: "k1", Algorithm: "EdDSA", Key: []byte{1}, CreatedAt: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ = e.run("reset-credential", "pat")
	if code != 0 || !strings.Contains(out, "1 credential(s)") {
		t.Fatalf("reset-credential (one): code %d out %q", code, out)
	}
	if code, _, errOut := e.run("reset-credential", "ghost"); code != 1 || !strings.Contains(errOut, "register it first") {
		t.Fatalf("reset-credential of an unknown name: code %d stderr %q", code, errOut)
	}
}

func TestAdminCreateIdentity(t *testing.T) {
	e := newAdminEnv(t)
	code, out, _ := e.run("create-identity", "kim")
	if code != 0 || strings.TrimSpace(out) != "kim created" {
		t.Fatalf("first create: code %d out %q", code, out)
	}
	code, out, _ = e.run("create-identity", "kim")
	if code != 0 || strings.TrimSpace(out) != "kim already exists" {
		t.Fatalf("second create: code %d out %q", code, out)
	}
	svc, st := e.service()
	_ = svc
	id := readStore(t, st, func(ctx context.Context, tx store.Tx) (store.Identity, error) { return tx.Identity(ctx, "kim") })
	if id.Admin {
		t.Fatal("create-identity set the administrator flag")
	}
	n := readStore(t, st, func(ctx context.Context, tx store.Tx) (int, error) {
		gen, err := tx.ChannelByName(ctx, "general")
		if err != nil {
			return 0, err
		}
		subs, err := tx.Subscriptions(ctx, "kim")
		if err != nil {
			return 0, err
		}
		n := 0
		for _, s := range subs {
			if s.ChannelID == gen.ID {
				n++
			}
		}
		return n, nil
	})
	if n != 1 {
		t.Fatalf("kim has %d general subscriptions, want 1", n)
	}

	code, _, errOut := e.run("create-identity")
	if code != 2 || !strings.Contains(errOut, "keyless") || !strings.Contains(errOut, "enrollment token") {
		t.Fatalf("create-identity without a name: code %d stderr %q", code, errOut)
	}
	// A reserved or malformed name is refused by the Store rule, not accepted.
	if code, _, _ := e.run("create-identity", "everyone"); code == 0 {
		t.Fatal("create-identity accepted the reserved name everyone")
	}
}

var (
	tokenRE = regexp.MustCompile(`(?m)^token: (abe_\S+)$`)
	infoRE  = regexp.MustCompile(`(?m)^id: (\S+) identity: (\S+) expires: (\S+) uses: (\d+)$`)
)

func TestAdminEnrollmentLifecycle(t *testing.T) {
	e := newAdminEnv(t)
	code, out, errOut := e.run("enrollment", "create")
	if code != 0 {
		t.Fatalf("create: code %d stderr %q", code, errOut)
	}
	if !tokenRE.MatchString(out) {
		t.Fatalf("no token line in %q", out)
	}
	m := infoRE.FindStringSubmatch(out)
	if m == nil || m[2] != "any" || m[4] != "1" {
		t.Fatalf("info line %q (match %v)", out, m)
	}
	id1 := m[1]

	code, out, _ = e.run("enrollment", "list")
	if code != 0 || !strings.HasPrefix(out, id1+" any ") || strings.Contains(out, "expired") {
		t.Fatalf("list: code %d out %q", code, out)
	}

	code, out, errOut = e.run("enrollment", "create", "-uses", "3", "-expires", "1h", "-identity", "kim")
	if code != 0 {
		t.Fatalf("create with flags: code %d stderr %q", code, errOut)
	}
	m = infoRE.FindStringSubmatch(out)
	if m == nil || m[2] != "kim" || m[4] != "3" {
		t.Fatalf("info line %q", out)
	}
	id2 := m[1]
	_, out, _ = e.run("enrollment", "list")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 2 {
		t.Fatalf("list has %d lines: %q", len(lines), out)
	}

	code, out, _ = e.run("enrollment", "revoke", id1)
	if code != 0 || strings.TrimSpace(out) != id1+" revoked" {
		t.Fatalf("revoke: code %d out %q", code, out)
	}
	if code, _, _ := e.run("enrollment", "revoke", id1); code != 1 {
		t.Fatalf("second revoke: code %d, want 1", code)
	}
	_, out, _ = e.run("enrollment", "list")
	if strings.Contains(out, id1) || !strings.Contains(out, id2) {
		t.Fatalf("list after revoke: %q", out)
	}

	for _, bad := range [][]string{
		{"enrollment"},
		{"enrollment", "frobnicate"},
		{"enrollment", "create", "-uses", "0"},
		{"enrollment", "create", "-expires", "-1h"},
		{"enrollment", "create", "-expires", "soon"},
		{"enrollment", "create", "extra"},
		{"enrollment", "list", "extra"},
		{"enrollment", "revoke"},
		{"nonsense"},
		{"signing-key", "create", "x"},
	} {
		if code, _, errOut := e.run(bad...); code != 2 || errOut == "" {
			t.Errorf("%v: code %d stderr %q, want 2 with a message", bad, code, errOut)
		}
	}
	if _, _, errOut := e.run("nonsense"); !strings.Contains(errOut, "users and admins") {
		t.Errorf("unknown subcommand message does not list the subcommands: %q", errOut)
	}
	if code, _, _ := e.runEnv(nil, "-sqlite", e.file); code != 2 {
		t.Errorf("no subcommand: code %d, want 2", code)
	}
}

func TestAdminEnrollmentListMarksExpired(t *testing.T) {
	e := newAdminEnv(t)
	svc, st := e.service()
	_ = svc
	err := st.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.PutEnrollmentToken(ctx, store.EnrollmentToken{ID: "dead", TokenHash: "h", ExpiresAt: 1, UsesLeft: 1, CreatedAt: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := e.run("enrollment", "list")
	if code != 0 || !strings.HasSuffix(strings.TrimSpace(out), " expired") {
		t.Fatalf("list: code %d out %q", code, out)
	}
}

func TestAdminWorksWithoutServer(t *testing.T) {
	e := newAdminEnv(t)
	cfgPath := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(cfgPath, []byte(`{"listen": "nonsense", "registration": "open"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := e.run("-config", cfgPath, "create-identity", "a"); code != 0 {
		t.Fatalf("config with a nonsense listen: code %d stderr %q", code, errOut)
	}
	if code, _, errOut := e.run("-set", "listen=nonsense", "-set", "registration=enrollment", "create-identity", "b"); code != 0 {
		t.Fatalf("-set listen/registration: code %d stderr %q", code, errOut)
	}
	if code, _, errOut := e.runEnv([]string{"AGENTBUS2_SERVER_LISTEN=nonsense"}, "-sqlite", e.file, "create-identity", "c"); code != 0 {
		t.Fatalf("environment listen: code %d stderr %q", code, errOut)
	}
	// Syntax of registration is still judged, as for the server.
	if code, _, errOut := e.run("-set", "registration=bogus", "create-identity", "d"); code != 2 || !strings.Contains(errOut, "registration") {
		t.Fatalf("bad registration: code %d stderr %q", code, errOut)
	}
	for _, fl := range [][]string{{"-listen", "127.0.0.1:0"}, {"-registration", "open"}} {
		args := append([]string{"-sqlite", e.file}, fl...)
		args = append(args, "create-identity", "e")
		if code, _, _ := e.runEnv(nil, args...); code != 2 {
			t.Errorf("%v: code %d, want 2 (unknown flag)", fl, code)
		}
	}
}

func TestAdminFlagSet(t *testing.T) {
	e := newAdminEnv(t)
	code, _, errOut := e.run("-set", "colour=1", "create-identity", "a")
	if code != 2 || !strings.Contains(errOut, "colour") || !strings.Contains(errOut, "registration") {
		t.Fatalf("-set colour: code %d stderr %q", code, errOut)
	}
	if code, _, _ := e.run("-set", "nonsense", "create-identity", "a"); code != 2 {
		t.Fatalf("-set without '=': code %d, want 2", code)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	if code, _, _ := e.run("-config", missing, "create-identity", "a"); code != 2 {
		t.Fatalf("missing -config: code %d, want 2", code)
	}
	code, _, errOut = e.runEnv(nil, "-sqlite", e.file, "-postgres", "postgres://x", "create-identity", "a")
	if code != 2 || !strings.Contains(errOut, "exactly one") {
		t.Fatalf("both stores: code %d stderr %q", code, errOut)
	}
	code, _, errOut = e.runEnv(nil, "create-identity", "a")
	if code != 2 || !strings.Contains(errOut, "exactly one") {
		t.Fatalf("no store: code %d stderr %q", code, errOut)
	}
	code, out, errOut := e.runEnv([]string{"AGENTBUS2_SERVER_SQLITE=" + e.file}, "create-identity", "a")
	if code != 0 || !strings.Contains(out, "a created") {
		t.Fatalf("store from the environment: code %d out %q stderr %q", code, out, errOut)
	}
	code, _, errOut = e.runEnv([]string{"AGENTBUS2_SERVER_SQLITE=" + e.file, "AGENTBUS2_SERVER_COLOUR=1"}, "create-identity", "b")
	if code != 2 || !strings.Contains(errOut, "colour") {
		t.Fatalf("unknown environment setting: code %d stderr %q", code, errOut)
	}
	// -set overrides the environment; the -sqlite flag overrides both.
	other := filepath.Join(t.TempDir(), "other.db")
	if code, _, errOut := e.runEnv([]string{"AGENTBUS2_SERVER_SQLITE=" + e.file}, "-sqlite", other, "create-identity", "z"); code != 0 {
		t.Fatalf("flag over environment: code %d stderr %q", code, errOut)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("the -sqlite flag did not win: %v", err)
	}
	// -h prints usage naming every setting and exits 0.
	var o, er bytes.Buffer
	if code := RunAdmin(context.Background(), []string{"-h"}, nil, &o, &er); code != 0 || !strings.Contains(o.String(), "receive_max_count") {
		t.Fatalf("-h: code %d out %q", code, o.String())
	}
	// A store that cannot open is a runtime error (1), not a flag error.
	bad := filepath.Join(t.TempDir(), "no-such-dir", "x.db")
	if code, _, _ := e.runEnv(nil, "-sqlite", bad, "create-identity", "a"); code != 1 {
		t.Fatalf("unopenable store: code %d, want 1", code)
	}
}

func TestAdminCreateHuman(t *testing.T) {
	e := newAdminEnv(t)
	code, out, _ := e.run("create-human", "Eric@Corp.Example")
	if code != 0 || strings.TrimSpace(out) != "eric@corp.example created" {
		t.Fatalf("first create: code %d out %q", code, out)
	}
	code, out, _ = e.run("create-human", "eric@corp.example")
	if code != 0 || strings.TrimSpace(out) != "eric@corp.example already exists" {
		t.Fatalf("second create: code %d out %q", code, out)
	}
	_, st := e.service()
	id := readStore(t, st, func(ctx context.Context, tx store.Tx) (store.Identity, error) {
		return tx.Identity(ctx, "eric@corp.example")
	})
	if id.Kind != store.IdentityHuman || id.Admin {
		t.Fatalf("identity = %+v", id)
	}
	code, _, errOut := e.run("create-human", "bad")
	if code != 1 || !strings.Contains(errOut, "one '@'") {
		t.Fatalf("create-human bad: code %d stderr %q", code, errOut)
	}
	if code, _, _ := e.run("create-human"); code != 2 {
		t.Fatalf("create-human without an argument: code %d, want 2", code)
	}
	if code, _, _ := e.run("create-identity", "kim@corp.example"); code != 0 {
		t.Fatal("create-identity failed")
	}
	if code, _, errOut := e.run("create-human", "kim@corp.example"); code != 1 || !strings.Contains(errOut, "is an agent") {
		t.Fatalf("create-human over an agent: code %d stderr %q", code, errOut)
	}
}
