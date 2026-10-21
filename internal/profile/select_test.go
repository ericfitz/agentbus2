package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoWith(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.Mkdir(filepath.Join(root, ".local"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".local", "agentbus2.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSelectNameOrder(t *testing.T) {
	repo := repoWith(t, `{"profile":"fromrepo"}`)
	norepo := repoWith(t, "")
	tests := []struct {
		name       string
		sel        Selection
		wantName   string
		wantSource string
	}{
		{"flag beats all", Selection{Flag: "f", Env: "e", Cwd: repo}, "f", SourceFlag},
		{"env beats repo", Selection{Env: "e", Cwd: repo}, "e", SourceEnv},
		{"repo beats default", Selection{Cwd: repo}, "fromrepo", SourceRepo},
		{"default", Selection{Cwd: norepo}, "default", SourceDefault},
		{"default without cwd", Selection{}, "default", SourceDefault},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, src, err := SelectName(tc.sel)
			if err != nil || name != tc.wantName || src != tc.wantSource {
				t.Fatalf("SelectName = %q, %q, %v", name, src, err)
			}
		})
	}
}

func TestSelectNameRepoErrorSurfaces(t *testing.T) {
	repo := repoWith(t, `{"profile":"x","credential_process":["touch","/tmp/pwned"]}`)
	name, _, err := SelectName(Selection{Cwd: repo})
	if err == nil || !strings.Contains(err.Error(), "credential_process") {
		t.Fatalf("SelectName = %q, %v; want an error naming credential_process", name, err)
	}
}

// An explicit selection never reads the repo file (Decision 21: the flag and
// the variable come first), so a bad file cannot block or influence it; the
// file can only ever yield a profile name, so nothing in it runs either way.
func TestSelectNameExplicitSkipsRepoFile(t *testing.T) {
	repo := repoWith(t, `{"profile":"x","credential_process":["touch","/tmp/pwned"]}`)
	for _, sel := range []Selection{{Flag: "f", Cwd: repo}, {Env: "e", Cwd: repo}} {
		if name, _, err := SelectName(sel); err != nil || (name != "f" && name != "e") {
			t.Fatalf("SelectName(%+v) = %q, %v", sel, name, err)
		}
	}
}

func TestResolveIdentityOrder(t *testing.T) {
	server := Profile{Name: "lab", Mode: ModeServer, Server: "https://h", Identity: "fromprofile"}
	bare := Profile{Name: "lab", Mode: ModeServer, Server: "https://h"}
	local := Profile{Name: "loc", Mode: ModeLocal}
	tests := []struct {
		name       string
		p          Profile
		sel        IdentitySelection
		wantID     string
		wantSource string
		wantErr    string
	}{
		{"flag", server, IdentitySelection{Flag: "f", Env: "e", Credentials: []string{"c"}}, "f", "flag", ""},
		{"env", server, IdentitySelection{Env: "e", Credentials: []string{"c"}}, "e", "env", ""},
		{"profile", server, IdentitySelection{Credentials: []string{"c"}}, "fromprofile", "profile", ""},
		{"one credential", bare, IdentitySelection{Credentials: []string{"c"}}, "c", "credential", ""},
		{"two credentials", bare, IdentitySelection{Credentials: []string{"c", "d"}}, "", "", "identity is required: pass -identity, set AGENTBUS2_IDENTITY, or set identity in profile \"lab\""},
		{"no credentials", bare, IdentitySelection{}, "", "", "identity is required"},
		{"local never infers", local, IdentitySelection{Credentials: []string{"c"}}, "", "", "identity is required: pass -identity, set AGENTBUS2_IDENTITY, or set identity in profile \"loc\""},
		{"local with flag", local, IdentitySelection{Flag: "me"}, "me", "flag", ""},
		{"invalid flag", server, IdentitySelection{Flag: "a/b"}, "", "", "a/b"},
		{"invalid env", server, IdentitySelection{Env: "a/b"}, "", "", "a/b"},
		{"invalid credential file name", bare, IdentitySelection{Credentials: []string{"a/b"}}, "", "", "a/b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, src, err := tc.p.ResolveIdentity(tc.sel)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v must contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || id != tc.wantID || src != tc.wantSource {
				t.Fatalf("ResolveIdentity = %q, %q, %v", id, src, err)
			}
		})
	}
}
