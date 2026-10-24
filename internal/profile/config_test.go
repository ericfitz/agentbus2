package profile

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAbsentIsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope", "config.json")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != p || c.Profiles == nil || len(c.Profiles) != 0 {
		t.Fatalf("Load absent = %+v", c)
	}
}

func TestLoadStrict(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"unknown top-level key", `{"profiles":{},"colour":1}`, "colour"},
		{"unknown profile key", `{"profiles":{"a":{"mode":"local","colour":"red"}}}`, "colour"},
		{"null", `null`, "null"},
		{"null profiles", `{"profiles":null}`, "profiles"},
		{"trailing object", `{"profiles":{}} {}`, "exactly one"},
		{"trailing junk", `{"profiles":{}} x`, "exactly one"},
		{"empty file", ``, "empty"},
		{"whitespace file", " \n", "empty"},
		{"unknown mode", `{"profiles":{"a":{"mode":"remote"}}}`, "remote"},
		{"missing mode", `{"profiles":{"a":{}}}`, "mode"},
		{"null profile", `{"profiles":{"a":null}}`, "a"},
		{"profile name with slash", `{"profiles":{"a/b":{"mode":"local"}}}`, "a/b"},
		{"profile name dotdot", `{"profiles":{"..":{"mode":"local"}}}`, ".."},
		{"not an object", `[]`, "config.json"},
		{"top-level member case", `{"Profiles":{}}`, `unknown member "Profiles"`},
		{"profiles repeated", `{"profiles":{},"profiles":{"a":{"mode":"local"}}}`, `member "profiles" repeated`},
		{"profile repeated", `{"profiles":{"a":{"mode":"local"},"a":{"mode":"server","server":"https://h"}}}`, `profiles: key "a" repeated`},
		{"profile member case", `{"profiles":{"a":{"Mode":"local"}}}`, `profiles["a"]: unknown member "Mode"`},
		{"profile member repeated", `{"profiles":{"a":{"mode":"local","mode":"server"}}}`, `profiles["a"]: member "mode" repeated`},
		{"name field not a member", `{"profiles":{"a":{"mode":"local","Name":"b"}}}`, `"Name"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := writeCfg(t, tc.body)
			_, err := Load(p)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q must name %q and %q", err, p, tc.want)
			}
		})
	}
}

func TestLoadValid(t *testing.T) {
	p := writeCfg(t, `{"profiles":{
  "lab":{"mode":"server","server":"https://h:8443/","identity":"eric","credential_process":["x","y"]},
  "loc":{"mode":"local","sqlite":"/tmp/a.sqlite"}}}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	lab := c.Profiles["lab"]
	if lab.Name != "lab" || lab.Server != "https://h:8443" || lab.Identity != "eric" || !reflect.DeepEqual(lab.CredentialProcess, []string{"x", "y"}) {
		t.Fatalf("lab = %+v", lab)
	}
	if c.Profiles["loc"].Name != "loc" {
		t.Fatalf("loc = %+v", c.Profiles["loc"])
	}
}

func TestProfileValidate(t *testing.T) {
	tests := []struct {
		name    string
		p       Profile
		wantErr string
	}{
		{"local ok", Profile{Mode: ModeLocal}, ""},
		{"local with sqlite and identity", Profile{Mode: ModeLocal, SQLite: "/x", Identity: "a"}, ""},
		{"server ok", Profile{Mode: ModeServer, Server: "https://h:8443"}, ""},
		{"server http ok", Profile{Mode: ModeServer, Server: "http://127.0.0.1:1/"}, ""},
		{"empty mode", Profile{}, "mode"},
		{"unknown mode", Profile{Mode: "remote"}, `"remote"`},
		{"local with server", Profile{Mode: ModeLocal, Server: "https://h"}, "server"},
		{"local with credential_process", Profile{Mode: ModeLocal, CredentialProcess: []string{"x"}}, "credential_process"},
		{"server without server", Profile{Mode: ModeServer}, "server"},
		{"server with path", Profile{Mode: ModeServer, Server: "https://h:8443/api"}, "path"},
		{"server with query", Profile{Mode: ModeServer, Server: "https://h/?a=1"}, "query"},
		{"server with fragment", Profile{Mode: ModeServer, Server: "https://h/#f"}, "fragment"},
		{"server relative", Profile{Mode: ModeServer, Server: "h:8443"}, "server"},
		{"server ftp", Profile{Mode: ModeServer, Server: "ftp://h"}, "server"},
		{"server no host", Profile{Mode: ModeServer, Server: "https://"}, "server"},
		{"server with userinfo", Profile{Mode: ModeServer, Server: "https://u:p@h"}, "server"},
		{"server with sqlite", Profile{Mode: ModeServer, Server: "https://h", SQLite: "/x"}, "sqlite"},
		{"identity with slash", Profile{Mode: ModeLocal, Identity: "a/b"}, "identity"},
		{"identity dot", Profile{Mode: ModeLocal, Identity: "."}, "identity"},
		{"identity with colon", Profile{Mode: ModeLocal, Identity: "a:b"}, "identity"},
		{"identity reserved", Profile{Mode: ModeLocal, Identity: "aux"}, "identity"},
		{"empty credential_process word", Profile{Mode: ModeServer, Server: "https://h", CredentialProcess: []string{""}}, "credential_process"},
		{"server with credential_process", Profile{Mode: ModeServer, Server: "https://h", CredentialProcess: []string{"x"}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error %v must contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestOIDCValidate(t *testing.T) {
	ok := func() *OIDC {
		return &OIDC{Issuer: "https://idp.example", ClientID: "cid", RedirectPort: 8765, Scopes: []string{"openid", "email"}}
	}
	with := func(f func(*OIDC)) *OIDC { o := ok(); f(o); return o }
	srv := func(o *OIDC) Profile { return Profile{Mode: ModeServer, Server: "https://h", OIDC: o} }
	tests := []struct {
		name    string
		p       Profile
		wantErr string
	}{
		{"server ok", srv(ok()), ""},
		{"http loopback 127.0.0.1 ok", srv(with(func(o *OIDC) { o.Issuer = "http://127.0.0.1:9000" })), ""},
		{"http localhost ok", srv(with(func(o *OIDC) { o.Issuer = "http://localhost:9000/path" })), ""},
		{"http ipv6 loopback ok", srv(with(func(o *OIDC) { o.Issuer = "http://[::1]:9000" })), ""},
		{"http non-loopback refused", srv(with(func(o *OIDC) { o.Issuer = "http://idp.example" })), "https"},
		{"http lookalike host refused", srv(with(func(o *OIDC) { o.Issuer = "http://127.0.0.1.example" })), "https"},
		{"local mode refused", Profile{Mode: ModeLocal, OIDC: ok()}, "oidc"},
		{"port 0", srv(with(func(o *OIDC) { o.RedirectPort = 0 })), "redirect_port"},
		{"port 65536", srv(with(func(o *OIDC) { o.RedirectPort = 65536 })), "redirect_port"},
		{"port negative", srv(with(func(o *OIDC) { o.RedirectPort = -1 })), "redirect_port"},
		{"empty scopes", srv(with(func(o *OIDC) { o.Scopes = nil })), "scopes"},
		{"scope with space", srv(with(func(o *OIDC) { o.Scopes = []string{"a b"} })), "scopes"},
		{"empty scope entry", srv(with(func(o *OIDC) { o.Scopes = []string{"openid", ""} })), "scopes"},
		{"missing client_id", srv(with(func(o *OIDC) { o.ClientID = "" })), "client_id"},
		{"non-URL issuer", srv(with(func(o *OIDC) { o.Issuer = "idp" })), "issuer"},
		{"empty issuer", srv(with(func(o *OIDC) { o.Issuer = "" })), "issuer"},
		{"ftp issuer", srv(with(func(o *OIDC) { o.Issuer = "ftp://idp.example" })), "issuer"},
		{"issuer with userinfo", srv(with(func(o *OIDC) { o.Issuer = "https://u:p@idp.example" })), "issuer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error %v must contain %q", err, tc.wantErr)
			}
		})
	}
}

// The oidc object round-trips through Save and Load, and Load is strict about
// its members.
func TestOIDCLoadSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := &OIDC{Issuer: "https://idp.example", ClientID: "cid", RedirectPort: 8765, Scopes: []string{"openid"}}
	c := Config{Path: path, Profiles: map[string]Profile{"lab": {Name: "lab", Mode: ModeServer, Server: "https://h", OIDC: want}}}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Profiles["lab"].OIDC, want) {
		t.Fatalf("round trip = %+v", got.Profiles["lab"].OIDC)
	}
	bad := writeCfg(t, `{"profiles":{"lab":{"mode":"server","server":"https://h","oidc":{"issuer":"https://i","client_id":"c","redirect_port":1,"scopes":["a"],"colour":1}}}}`)
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "colour") {
		t.Fatalf("unknown oidc key: %v", err)
	}
	// A saved invalid oidc is refused before anything is written.
	c.Profiles["lab"] = Profile{Name: "lab", Mode: ModeServer, Server: "https://h", OIDC: &OIDC{Issuer: "http://idp.example", ClientID: "c", RedirectPort: 1, Scopes: []string{"a"}}}
	if err := c.Save(); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("Save of an http non-loopback issuer: %v", err)
	}
}

func TestSaveRoundTripAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	path := filepath.Join(dir, "config.json")
	c := Config{Path: path, Profiles: map[string]Profile{
		"lab": {Name: "lab", Mode: ModeServer, Server: "https://h:8443", Identity: "eric", CredentialProcess: []string{"a", "b"}},
		"loc": {Name: "loc", Mode: ModeLocal, SQLite: "/tmp/x.sqlite"},
	}}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v", fi.Mode().Perm())
		}
		di, _ := os.Stat(dir)
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", di.Mode().Perm())
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temp file left: %v", ents)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("reload = %+v, %v; want %+v", got, err, c)
	}
	// A second save over the existing file succeeds and leaves one file.
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if ents, _ = os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("temp file left after second save: %v", ents)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := (Config{Path: path, Profiles: map[string]Profile{"a": {Name: "a", Mode: "remote"}}}).Save(); err == nil {
		t.Fatal("invalid profile must not be saved")
	}
	if err := (Config{Path: path, Profiles: map[string]Profile{"a/b": {Mode: ModeLocal}}}).Save(); err == nil {
		t.Fatal("invalid profile name must not be saved")
	}
	if err := (Config{Profiles: map[string]Profile{}}).Save(); err == nil {
		t.Fatal("empty path must fail")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("failed saves left files: %v", ents)
	}
}

func TestResolveDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Config{Path: path, Profiles: map[string]Profile{"lab": {Name: "lab", Mode: ModeLocal}}}
	p, err := c.Resolve("default")
	if err != nil || !reflect.DeepEqual(p, DefaultProfile()) {
		t.Fatalf("Resolve default = %+v, %v", p, err)
	}
	if p, err := c.Resolve("lab"); err != nil || p.Name != "lab" {
		t.Fatalf("Resolve lab = %+v, %v", p, err)
	}
	_, err = c.Resolve("other")
	if err == nil || !strings.Contains(err.Error(), `"other"`) || !strings.Contains(err.Error(), path) {
		t.Fatalf("Resolve other error = %v", err)
	}
	// A defined default wins over the built-in.
	c.Profiles["default"] = Profile{Name: "default", Mode: ModeLocal, Identity: "me"}
	if p, _ := c.Resolve("default"); p.Identity != "me" {
		t.Fatalf("defined default ignored: %+v", p)
	}
	// An unsafe name is never resolved, even when "defined".
	c.Profiles["../x"] = Profile{Name: "../x", Mode: ModeLocal}
	if _, err := c.Resolve("../x"); err == nil {
		t.Fatal("unsafe profile name must be refused")
	}
}

func TestSQLitePathAndCredentialDir(t *testing.T) {
	if got := SQLitePath(Profile{Mode: ModeLocal}, "/d"); got != filepath.Join("/d", "agentbus2.sqlite") {
		t.Fatalf("default sqlite = %q", got)
	}
	if got := SQLitePath(Profile{Mode: ModeLocal, SQLite: "/x/y.db"}, "/d"); got != "/x/y.db" {
		t.Fatalf("explicit sqlite = %q", got)
	}
	if got := CredentialDir("/c", "lab"); got != filepath.Join("/c", "credentials", "lab") {
		t.Fatalf("CredentialDir = %q", got)
	}
}

func TestLoadTUISettings(t *testing.T) {
	p := writeCfg(t, `{"profiles":{},"tui":{"theme":"dark","themes":[{"name":"dark","agent":"cyan"}],"icons":"nerdfont","icon_map":{"idle":"z"},"rail_width":24}}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := &TUISettings{Theme: "dark", Themes: []TUIColorTheme{{Name: "dark", Agent: "cyan"}}, Icons: "nerdfont", IconMap: map[string]string{"idle": "z"}, RailWidth: 24}
	if !reflect.DeepEqual(c.TUI, want) {
		t.Fatalf("TUI = %+v, want %+v", c.TUI, want)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(p)
	if err != nil || !reflect.DeepEqual(c2.TUI, want) {
		t.Fatalf("round trip = %+v, %v", c2.TUI, err)
	}

	bad := []struct{ name, tui, want string }{
		{"unknown key", `{"colour":"x"}`, "colour"},
		{"null tui", `null`, `"tui" must not be null`},
		{"rail 1001", `{"rail_width":1001}`, "tui.rail_width must be between 0 and 1000, got 1001"},
		{"rail -1", `{"rail_width":-1}`, "tui.rail_width must be between 0 and 1000, got -1"},
		{"icons ascii", `{"icons":"ascii"}`, `tui.icons must be emoji, nerdfont or custom, got "ascii"`},
		{"empty theme name", `{"themes":[{"agent":"red"}]}`, "tui.themes[0].name must not be empty"},
		{"repeated theme", `{"themes":[{"name":"dark"},{"name":"dark"}]}`, `tui.themes[1].name "dark" repeats tui.themes[0]`},
		{"dangling theme", `{"theme":"light"}`, `tui.theme "light" is not default or the name of an entry of tui.themes`},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			p := writeCfg(t, `{"profiles":{},"tui":`+tc.tui+`}`)
			_, err := Load(p)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), p) {
				t.Fatalf("Load = %v, want error naming %q and %q", err, p, tc.want)
			}
		})
	}

	for _, ok := range []string{`{"rail_width":0}`, `{"rail_width":1000}`, `{"theme":"default"}`, `{"icons":"emoji"}`, `{"icons":"custom"}`} {
		if _, err := Load(writeCfg(t, `{"tui":`+ok+`}`)); err != nil {
			t.Fatalf("%s rejected: %v", ok, err)
		}
	}
	c3, err := Load(writeCfg(t, `{"profiles":{}}`))
	if err != nil || c3.TUI != nil {
		t.Fatalf("no tui: %+v, %v", c3.TUI, err)
	}
}

func TestSaveRejectsInvalidTUI(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := Config{Path: p, Profiles: map[string]Profile{}, TUI: &TUISettings{Icons: "ascii"}}
	if err := c.Save(); err == nil || !strings.Contains(err.Error(), "tui.icons") {
		t.Fatalf("Save = %v", err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("file written")
	}
}

func TestSetTUIRailWidthTwice(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.json")
	// Missing file: Load answers an empty Config, so the file is created
	// holding only tui, mode 0600.
	if err := SetTUIRailWidth(p, 30); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetTUIRailWidth(p, 30); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("second run changed the file:\n%s\n---\n%s", first, second)
	}
	if n := strings.Count(string(second), `"tui"`); n != 1 {
		t.Fatalf("%d tui objects in %s", n, second)
	}
	c, err := Load(p)
	if err != nil || c.TUI == nil || c.TUI.RailWidth != 30 || len(c.Profiles) != 0 {
		t.Fatalf("Load = %+v, %v", c, err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v", fi, err)
		}
	}
}

func TestSetTUIRailWidthErrors(t *testing.T) {
	p := writeCfg(t, `{"profiles":{},"colour":1}`)
	before, _ := os.ReadFile(p)
	if err := SetTUIRailWidth(p, 30); err == nil {
		t.Fatal("invalid file accepted")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("invalid file rewritten")
	}
	good := writeCfg(t, `{"profiles":{}}`)
	for _, w := range []int{-1, 1001} {
		if err := SetTUIRailWidth(good, w); err == nil || !strings.Contains(err.Error(), "tui.rail_width") {
			t.Fatalf("width %d: %v", w, err)
		}
	}
}

func TestSetTUIRailWidthKeepsProfiles(t *testing.T) {
	p := writeCfg(t, `{"profiles":{"lab":{"mode":"server","server":"https://x.example","identity":"pat"}},"tui":{"icons":"emoji"}}`)
	if err := SetTUIRailWidth(p, 30); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Profiles["lab"].Server != "https://x.example" || c.TUI == nil || c.TUI.RailWidth != 30 || c.TUI.Icons != "emoji" {
		t.Fatalf("reload = %+v", c)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v", fi, err)
		}
	}
}
