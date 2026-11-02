//go:build !windows

package mcpcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSnippet(t *testing.T) {
	prev := executable
	executable = func() (string, error) { return "/opt/bin/agentbus2-mcponly", nil }
	t.Cleanup(func() { executable = prev })
	environ, _ := testEnviron(t)
	golden, err := filepath.Abs("testdata/config")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	for name, args := range map[string][]string{
		"default":            {"config", "-server", "http://h:1", "-identity", "pat"},
		"profile-credential": {"config", "-profile", "lab", "-server", "http://h:1", "-identity", "pat", "-credential", "/tmp/my key.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errs := run(args, environ)
			if code != 0 || errs != "" {
				t.Fatalf("code %d, stderr %q", code, errs)
			}
			want, err := os.ReadFile(filepath.Join(golden, name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			if out != string(want) {
				t.Errorf("output:\n%s\nwant:\n%s", out, want)
			}
			var doc struct {
				MCPServers map[string]struct {
					Command string   `json:"command"`
					Args    []string `json:"args"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(strings.SplitN(out, "\n\n", 2)[0]), &doc); err != nil {
				t.Fatalf("the JSON part does not parse: %v", err)
			}
			if e := doc.MCPServers["agentbus2"]; e.Command != "/opt/bin/agentbus2-mcponly" || len(e.Args) == 0 || e.Args[0] != "serve" {
				t.Errorf("entry = %+v", e)
			}
		})
	}
}

// A relative -credential becomes absolute: the harness starts the server from
// another directory.
func TestConfigCredentialAbsolute(t *testing.T) {
	prev := executable
	executable = func() (string, error) { return "/opt/bin/agentbus2-mcponly", nil }
	t.Cleanup(func() { executable = prev })
	environ, _ := testEnviron(t)
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	code, out, errs := run([]string{"config", "-server", "http://h:1", "-identity", "pat", "-credential", "rel.pem"}, environ)
	want := filepath.Join(wd, "rel.pem")
	if code != 0 || !strings.Contains(out, `"`+want+`"`) || !strings.Contains(out, " -credential "+want+"\n") {
		t.Errorf("code %d, stdout %q, stderr %q, want %s", code, out, errs, want)
	}
}
