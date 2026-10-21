package repoconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func put(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".local", "agentbus2.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindWalksToGitRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A file above the repository root is never reached.
	put(t, root, `{"profile":"above"}`)
	f, err := Find(sub)
	if err != nil || f != nil {
		t.Fatalf("above .git: Find = %+v, %v", f, err)
	}
	put(t, repo, `{"profile":"lab"}`)
	f, err = Find(sub)
	if err != nil || f == nil || f.Profile != "lab" || f.Path != filepath.Join(repo, ".local", "agentbus2.json") {
		t.Fatalf("Find = %+v, %v", f, err)
	}
	// The nearest file wins.
	put(t, filepath.Join(repo, "a"), `{"profile":"near"}`)
	if f, _ := Find(sub); f == nil || f.Profile != "near" {
		t.Fatalf("nearest = %+v", f)
	}
}

func TestFindGitFileMarksRoot(t *testing.T) {
	// A worktree has a .git file, not a directory.
	root := t.TempDir()
	repo := filepath.Join(root, "wt")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	put(t, root, `{"profile":"above"}`)
	if f, err := Find(repo); err != nil || f != nil {
		t.Fatalf("Find = %+v, %v", f, err)
	}
}

func TestFindNoGitWalksToRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("walks to a filesystem root")
	}
	f, err := Find(t.TempDir())
	if err != nil || f != nil {
		t.Fatalf("Find = %+v, %v", f, err)
	}
}

func TestRepoFileRejectsEverythingButProfile(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"credential_process", `{"profile":"lab","credential_process":["x"]}`, "credential_process"},
		{"identity", `{"identity":"x"}`, "identity"},
		{"identity with profile", `{"profile":"lab","identity":"x"}`, "identity"},
		{"channels", `{"profile":"lab","channels":["a"]}`, "channels"},
		{"empty profile", `{"profile":""}`, "profile"},
		{"missing profile", `{}`, "profile"},
		{"non-string profile", `{"profile":3}`, "profile"},
		{"null profile", `{"profile":null}`, "profile"},
		{"bad profile name", `{"profile":"a/b"}`, "a/b"},
		{"dotdot profile", `{"profile":".."}`, ".."},
		{"null file", `null`, "agentbus2.json"},
		{"not an object", `[]`, "agentbus2.json"},
		{"empty file", ``, "agentbus2.json"},
		{"trailing", `{"profile":"a"} {}`, "agentbus2.json"},
		{"malformed", `{`, "agentbus2.json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			put(t, repo, tc.body)
			f, err := Find(repo)
			if err == nil || f != nil {
				t.Fatalf("Find = %+v, %v; want an error", f, err)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), filepath.Join(repo, ".local", "agentbus2.json")) {
				t.Fatalf("error %q must name the path and %q", err, tc.want)
			}
		})
	}
	t.Run("valid", func(t *testing.T) {
		repo := t.TempDir()
		put(t, repo, `{"profile":"lab"}`)
		f, err := Find(repo)
		if err != nil || f == nil || f.Profile != "lab" {
			t.Fatalf("Find = %+v, %v", f, err)
		}
	})
	t.Run("message names unexpected key", func(t *testing.T) {
		repo := t.TempDir()
		put(t, repo, `{"profile":"lab","credential_process":["x"]}`)
		_, err := Find(repo)
		if err == nil || !strings.Contains(err.Error(), `.local/agentbus2.json may only select a profile: unexpected key "credential_process"`) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestWriteAndReload(t *testing.T) {
	repo := t.TempDir()
	f, err := Write(repo, "lab")
	if err != nil || f.Profile != "lab" || f.Path != filepath.Join(repo, ".local", "agentbus2.json") {
		t.Fatalf("Write = %+v, %v", f, err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(f.Path)
		if fi.Mode().Perm() != 0o644 {
			t.Fatalf("mode %v", fi.Mode().Perm())
		}
	}
	got, err := Find(repo)
	if err != nil || got == nil || got.Profile != "lab" {
		t.Fatalf("Find = %+v, %v", got, err)
	}
	// A second write replaces the first and leaves no temp file.
	if _, err := Write(repo, "other"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Find(repo); got.Profile != "other" {
		t.Fatalf("second write: %+v", got)
	}
	ents, _ := os.ReadDir(filepath.Join(repo, ".local"))
	if len(ents) != 1 {
		t.Fatalf("temp file left: %v", ents)
	}
	for _, bad := range []string{"", "a/b", ".."} {
		if _, err := Write(repo, bad); err == nil {
			t.Fatalf("Write(%q) must fail", bad)
		}
	}
}
