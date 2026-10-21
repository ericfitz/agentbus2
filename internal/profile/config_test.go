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
