package cli

// These tests configure harnesses only under temp dirs: Home is always
// newTestEnv's temp home, never the real one. No package-level helper is
// added (the fixture is testenv_test.go), so each test builds its fake
// harness CLI from closures.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/profile"
)

func TestInitGlobalClaude(t *testing.T) {
	e := newTestEnv(t)
	if err := os.Remove(filepath.Join(e.ConfigDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	var calls []string
	o := InitOptions{
		Harness: "claude", Home: e.Home, Cwd: t.TempDir(), Environ: e.Environ, OS: "linux",
		LookPath: func(n string) (string, error) { return "/fake/" + n, nil },
		Run:      func(n string, a ...string) error { calls = append(calls, n+" "+strings.Join(a, " ")); return nil },
	}
	var out bytes.Buffer
	if err := Init(o, &out); err != nil {
		t.Fatalf("Init: %v\n%s", err, out.String())
	}
	if len(calls) != 2 || calls[0] != "claude mcp remove -s user agentbus2" || calls[1] != "claude mcp add -s user agentbus2 -- agentbus2 mcp" {
		t.Fatalf("calls = %q", calls)
	}
	settings := filepath.Join(e.Home, ".claude", "settings.json")
	body, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string }
		}
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Hooks) != 2 {
		t.Fatalf("want exactly the Stop and SubagentStart events (no SessionStart), got %s", body)
	}
	if h := parsed.Hooks["Stop"]; len(h) != 1 || len(h[0].Hooks) != 1 || h[0].Hooks[0].Command != "agentbus2 stop-hook" {
		t.Fatalf("Stop: %s", body)
	}
	if h := parsed.Hooks["SubagentStart"]; len(h) != 1 || len(h[0].Hooks) != 1 || h[0].Hooks[0].Command != "agentbus2 subagent-hook" {
		t.Fatalf("SubagentStart: %s", body)
	}
	skill, err := os.ReadFile(filepath.Join(e.Home, ".claude", "skills", "using-agentbus2", "SKILL.md"))
	if err != nil || !bytes.Equal(skill, SkillMD) || len(SkillMD) == 0 {
		t.Fatalf("skill differs from SkillMD: %v", err)
	}
	cfg, err := profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := cfg.Profiles["default"]; !ok || d.Mode != profile.ModeLocal || len(cfg.Profiles) != 1 {
		t.Fatalf("config = %+v", cfg.Profiles)
	}

	// A second run changes nothing: no file is rewritten, no backup appears,
	// and an existing config.json is left alone.
	snap := func() map[string]string {
		m := map[string]string{}
		_ = filepath.Walk(e.Home, func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() {
				b, _ := os.ReadFile(p)
				m[p] = string(b)
			}
			return nil
		})
		cb, _ := os.ReadFile(filepath.Join(e.ConfigDir, "config.json"))
		m["config"] = string(cb)
		return m
	}
	before := snap()
	out.Reset()
	if err := Init(o, &out); err != nil {
		t.Fatal(err)
	}
	after := snap()
	if len(before) != len(after) {
		t.Fatalf("second run changed the file set: %d -> %d files", len(before), len(after))
	}
	for p, b := range before {
		if after[p] != b {
			t.Fatalf("second run rewrote %s", p)
		}
	}
	for p := range after {
		if strings.HasSuffix(p, ".bak") {
			t.Fatalf("second run left a backup: %s", p)
		}
	}
	if !strings.Contains(out.String(), "already present") {
		t.Fatalf("second run output:\n%s", out.String())
	}

	// A config.json that exists is left alone, even with other profiles.
	e.writeProfile(t, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "https://h:8443"})
	if err := Init(o, &out); err != nil {
		t.Fatal(err)
	}
	cfg, _ = profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if _, ok := cfg.Profiles["lab"]; !ok {
		t.Fatal("config.json was rewritten")
	}
}

func TestInitGlobalClaudeBacksUpAnExistingSettingsFile(t *testing.T) {
	e := newTestEnv(t)
	dir := filepath.Join(e.Home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"other-tool"}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	o := InitOptions{Harness: "claude", Home: e.Home, Cwd: t.TempDir(), Environ: e.Environ, OS: "linux",
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}
	var out bytes.Buffer
	if err := Init(o, &out); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(filepath.Join(dir, "settings.json.bak"))
	if err != nil || string(bak) != original {
		t.Fatalf("backup = %q, %v", bak, err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	for _, want := range []string{`"theme": "dark"`, "other-tool", "agentbus2 stop-hook", "agentbus2 subagent-hook"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("settings.json lacks %q:\n%s", want, body)
		}
	}
	if !strings.Contains(out.String(), "claude CLI not on PATH") {
		t.Fatalf("a missing CLI must print the snippet:\n%s", out.String())
	}
}

func TestInitGlobalCodex(t *testing.T) {
	e := newTestEnv(t)
	var calls []string
	o := InitOptions{
		Harness: "codex", Home: e.Home, Cwd: t.TempDir(), Environ: e.Environ, OS: "linux",
		LookPath: func(n string) (string, error) { return "/fake/" + n, nil },
		Run: func(n string, a ...string) error {
			calls = append(calls, n+" "+strings.Join(a, " "))
			if n == "codex" && a[1] == "add" { // the real CLI writes config.toml
				p := filepath.Join(e.Home, ".codex", "config.toml")
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				old, _ := os.ReadFile(p)
				return os.WriteFile(p, append(old, "\n[mcp_servers.agentbus2]\ncommand = \"agentbus2\"\nargs = [\"mcp\"]\n"...), 0o644)
			}
			return nil
		},
	}
	var out bytes.Buffer
	for i := 0; i < 2; i++ { // the second run must converge to the same files
		if err := Init(o, &out); err != nil {
			t.Fatal(err)
		}
	}
	if calls[1] != "codex mcp add agentbus2 -- agentbus2 mcp" {
		t.Fatalf("calls = %q", calls)
	}
	toml, _ := os.ReadFile(filepath.Join(e.Home, ".codex", "config.toml"))
	if strings.Count(string(toml), "tool_timeout_sec = 300") != 1 {
		t.Fatalf("config.toml:\n%s", toml)
	}
	hooks, _ := os.ReadFile(filepath.Join(e.Home, ".codex", "hooks.json"))
	if strings.Count(string(hooks), "agentbus2 stop-hook") != 1 || strings.Contains(string(hooks), "SessionStart") {
		t.Fatalf("hooks.json:\n%s", hooks)
	}
	prompt, _ := os.ReadFile(filepath.Join(e.Home, ".codex", "prompts", "agentbus2.md"))
	if !strings.Contains(string(prompt), "agentbus2 $ARGUMENTS") || !strings.Contains(string(prompt), "agentbus2 register") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	skill, err := os.ReadFile(filepath.Join(e.Home, ".agents", "skills", "using-agentbus2", "SKILL.md"))
	if err != nil || !bytes.Equal(skill, SkillMD) {
		t.Fatalf("skill: %v", err)
	}
}

func TestInitGlobalGrok(t *testing.T) {
	e := newTestEnv(t)
	o := InitOptions{Harness: "grok", Home: e.Home, Cwd: t.TempDir(), Environ: e.Environ, OS: "linux",
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}
	var out bytes.Buffer
	if err := Init(o, &out); err != nil {
		t.Fatal(err)
	}
	hooks, err := os.ReadFile(filepath.Join(e.Home, ".grok", "hooks", "agentbus2.json"))
	if err != nil || !strings.Contains(string(hooks), "agentbus2 stop-hook") || strings.Contains(string(hooks), "SessionStart") {
		t.Fatalf("hooks: %v\n%s", err, hooks)
	}
	rule, err := os.ReadFile(filepath.Join(e.Home, ".grok", "rules", "agentbus2.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(rule)), "identity") || strings.Contains(string(rule), "agentbus2 identity") {
		t.Fatalf("the rules file must not mention identity:\n%s", rule)
	}
	if _, err := os.Stat(filepath.Join(e.Home, ".grok", "commands", "agentbus2.md")); err != nil {
		t.Fatal(err)
	}
	if skill, err := os.ReadFile(filepath.Join(e.Home, ".grok", "skills", "using-agentbus2", "SKILL.md")); err != nil || !bytes.Equal(skill, SkillMD) {
		t.Fatalf("skill: %v", err)
	}
	// No harness forced and none present: nothing is written, and it says so.
	e2 := newTestEnv(t)
	out.Reset()
	if err := Init(InitOptions{Home: e2.Home, Cwd: t.TempDir(), Environ: e2.Environ, OS: "linux"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no harness found") {
		t.Fatalf("output:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(e2.Home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("an undetected harness must not be configured: %v", err)
	}
}

func TestInitDryRun(t *testing.T) {
	e := newTestEnv(t)
	if err := os.Remove(filepath.Join(e.ConfigDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	ran := 0
	o := InitOptions{Harness: "claude", DryRun: true, Home: e.Home, Cwd: t.TempDir(), Environ: e.Environ, OS: "linux",
		LookPath: func(n string) (string, error) { return "/fake/" + n, nil },
		Run:      func(string, ...string) error { ran++; return nil }}
	var out bytes.Buffer
	if err := Init(o, &out); err != nil {
		t.Fatal(err)
	}
	if ran != 0 {
		t.Fatalf("a dry run ran %d harness commands", ran)
	}
	entries, _ := os.ReadDir(e.Home)
	if len(entries) != 0 {
		t.Fatalf("a dry run wrote under home: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(e.ConfigDir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote config.json: %v", err)
	}
	for _, want := range []string{"would have run claude mcp add -s user agentbus2", "would write", "would write " + filepath.Join(e.ConfigDir, "config.json")} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestHookIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	in := &initer{out: &bytes.Buffer{}}
	for i := 0; i < 3; i++ {
		if err := in.hook(path, "Stop", stopHookCommand, ""); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := os.ReadFile(path)
	if n := strings.Count(string(body), "agentbus2 stop-hook"); n != 1 {
		t.Fatalf("%d hooks after three runs:\n%s", n, body)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("a first write has nothing to back up: %v", err)
	}
	// A different command that merely starts with ours is not ours.
	other := filepath.Join(dir, "other.json")
	if err := os.WriteFile(other, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"agentbus2 stop-hook-x"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.hook(other, "Stop", stopHookCommand, ""); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(other)
	if !strings.Contains(string(body), `"agentbus2 stop-hook"`) {
		t.Fatalf("the look-alike suppressed our hook:\n%s", body)
	}
	// A settings file that is not JSON is refused and left untouched.
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.hook(bad, "Stop", stopHookCommand, ""); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(bad); string(b) != "{not json" {
		t.Fatalf("bad file rewritten: %q", b)
	}
}

// TestHookLeavesTheUserFileAlone: a file that already runs our hooks is not
// rewritten (no reformatting, no .bak), an added hook does not HTML-escape the
// user's other commands, and a "hooks" value of an unrecognized shape is
// refused rather than replaced.
func TestHookLeavesTheUserFileAlone(t *testing.T) {
	dir := t.TempDir()
	in := &initer{out: &bytes.Buffer{}}
	present := filepath.Join(dir, "present.json")
	orig := "{\n    \"model\": \"opus\",\n    \"hooks\": {\n        \"Stop\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"agentbus2 stop-hook\"}]}],\n        \"SubagentStart\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"agentbus2 subagent-hook\"}]}]\n    }\n}\n"
	if err := os.WriteFile(present, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.hooks(present, hookSpec{"Stop", stopHookCommand, ""}, hookSpec{"SubagentStart", subagentHookCommand, ""}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(present); string(b) != orig {
		t.Fatalf("a file already holding both hooks was rewritten:\n%s", b)
	}
	if _, err := os.Stat(present + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("an unchanged file got a backup: %v", err)
	}

	amp := filepath.Join(dir, "amp.json")
	if err := os.WriteFile(amp, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"cd /x && make >/dev/null"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.hook(amp, "Stop", stopHookCommand, ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(amp); !strings.Contains(string(b), `"cd /x && make >/dev/null"`) {
		t.Fatalf("the user's command was escaped:\n%s", b)
	}

	// Numbers keep their text: a float64 round trip would rewrite them.
	nums := filepath.Join(dir, "nums.json")
	if err := os.WriteFile(nums, []byte(`{"big":12345678901234567891,"exp":1e3,"dec":1.10}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := in.hook(nums, "Stop", stopHookCommand, ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(nums)
	for _, want := range []string{`"big": 12345678901234567891`, `"exp": 1e3`, `"dec": 1.10`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lost %s:\n%s", nums, want, b)
		}
	}

	for name, body := range map[string]string{
		"hooks-array.json":  `{"hooks":[]}`,
		"hooks-string.json": `{"hooks":"x"}`,
		"event-object.json": `{"hooks":{"Stop":{"command":"x"}}}`,
		"null.json":         `null`,
		"top-array.json":    `[]`,
		"top-string.json":   `"x"`,
		"trailing.json":     `{} {}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := in.hook(path, "Stop", stopHookCommand, ""); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if b, _ := os.ReadFile(path); string(b) != body {
			t.Fatalf("%s rewritten: %q", name, b)
		}
	}
}

func TestRequireOnPath(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "self", "agentbus2.exe")
	other := filepath.Join(dir, "other", "agentbus2.exe")
	for _, p := range []string{self, other} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, onPath, want string }{
		{"missing", "", "agentbus2.exe is not on your PATH"},
		{"other copy", other, "is not this agentbus2.exe"},
		{"this binary", self, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			var out bytes.Buffer
			o := InitOptions{DryRun: true, Home: e.Home, Cwd: t.TempDir(), Environ: e.Environ, OS: "windows",
				Executable: func() (string, error) { return self, nil },
				LookPath: func(n string) (string, error) {
					if tc.onPath == "" {
						return "", exec.ErrNotFound
					}
					return tc.onPath, nil
				}}
			err := Init(o, &out)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Init: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "install.ps1") {
				t.Fatalf("Init error = %v, want %q and install.ps1", err, tc.want)
			}
			if out.Len() != 0 {
				t.Fatalf("Init wrote before refusing:\n%s", out.String())
			}
		})
	}
	// Off Windows the check is skipped.
	e := newTestEnv(t)
	o := InitOptions{DryRun: true, Home: e.Home, Cwd: t.TempDir(), Environ: e.Environ, OS: "darwin",
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}
	if err := Init(o, &bytes.Buffer{}); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

func TestInitRejectsUnknownHarnessAndFlags(t *testing.T) {
	e := newTestEnv(t)
	var out, errb bytes.Buffer
	if code := RunInit(t.Context(), []string{"-harness", "vim"}, e.Environ, nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "claude, codex, or grok") {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	errb.Reset()
	if code := RunInit(t.Context(), []string{"-nope"}, e.Environ, nil, &out, &errb); code != 2 {
		t.Fatalf("code %d", code)
	}
	errb.Reset()
	if code := RunInit(t.Context(), []string{"extra"}, e.Environ, nil, &out, &errb); code != 2 {
		t.Fatalf("code %d", code)
	}
	if err := Init(InitOptions{Harness: "vim", Home: e.Home, Cwd: t.TempDir(), Environ: e.Environ}, &out); err == nil {
		t.Fatal("Init accepted an unknown harness")
	}
}

func TestInitPromptsAvoidRetiredVocabulary(t *testing.T) {
	for name, text := range map[string]string{"InitPrompt": InitPrompt, "codexPrompt": codexPrompt, "grokRule": grokRule} {
		for _, bad := range []string{"dm/", "-filter", "@<", "memory/", "tasks/", "general/", "persistent", "agentbus identity", "name parameter"} {
			if strings.Contains(text, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
	}
}
