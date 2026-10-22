package cli

// The harness configuration of `agentbus2 init --global`: the MCP entry, the
// hooks, the skill and the prompts. Ported from v1 internal/cli/init.go
// (initer, write with .bak, run, wants, global, claude, codex, grok, mcpEntry,
// codexTimeout, hook, hasHookCommand, mcpConfigured, requireOnPath) with the
// agentbus2 names, no SessionStart hook (Decision 14) and no identity file.
//
// Every edit is idempotent: a file whose content would not change is not
// written, and a file that does change is first copied to <file>.bak.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericfitz/agentbus2/internal/profile"
)

const (
	stopHookCommand = "agentbus2 stop-hook"
	// subagentHookCommand is Claude Code only (v1 ADR 0016): Codex subagents get
	// their own MCP process, and no other harness has a SubagentStart prompt.
	subagentHookCommand = "agentbus2 subagent-hook"
	// unparkHookCommand is the UserPromptSubmit hook of every harness
	// (decision 18): a prompt from the user unparks the session.
	unparkHookCommand = "agentbus2 unpark"
)

// InitPrompt is what the `init` MCP prompt (Claude Code) and the Codex custom
// prompt tell the agent to do. It names no file step: the repository's profile
// is already written by `agentbus2 init`.
const InitPrompt = initPromptHead + initStepWait + initPromptTail

const initPromptHead = `Set up Agentbus2 for this repository:
1. Run ` + "`agentbus2 init`" + ` in a shell. Inside a git repository it selects a profile
   for the repository and prints the next step: ` + "`agentbus2 register`" + ` when the
   profile talks to a server and holds no credential yet.
2. Call the register tool, then pass the returned "session" on every later
   Agentbus2 call. Call receive right after registering, whenever you finish
   a task and before you ask the user a question; pass each batch's token as
   ack on your next receive.
`

// initStepWait is step 3 for every harness except Codex; initStepCodex is
// Codex's (no idle wake, so no background wait).
const initStepWait = `3. So new messages wake you while idle, run ` + "`agentbus2 wait`" + ` with Bash
   run_in_background; no timeout is needed. When it exits 0, call receive,
   handle what concerns you, and start it again; on exit 3 or 4 do nothing; on
   exit 2 report the error. The using-agentbus2 skill is the full protocol.
`

const initStepCodex = `3. Codex has no idle wake: call receive at the end of each task. The
   using-agentbus2 skill is the full protocol.
`

const initPromptTail = `If the register tool is missing, tell the user to run ` + "`agentbus2 init --global`" + `
from a shell and restart the harness.`

// codexPrompt is the in-session agentbus2 command. Codex does not surface MCP
// prompts, so init writes it to ~/.codex/prompts/agentbus2.md
// (/prompts:agentbus2). Codex has no idle wake, so its step 3 is receive at
// the end of each task. Grok Build gets grokCommand at
// ~/.grok/commands/agentbus2.md; its command files follow Claude's $ARGUMENTS
// placeholder.
const codexPrompt = "# Agentbus2\n\nThe user asked for: agentbus2 $ARGUMENTS\n\n" +
	"For `init`:\n" + initPromptHead + initStepCodex + initPromptTail + "\n\n" +
	"For anything else, run `agentbus2 $ARGUMENTS` in a shell and report the output.\n"

// grokCommand is codexPrompt with the background-wait step 3.
const grokCommand = "# Agentbus2\n\nThe user asked for: agentbus2 $ARGUMENTS\n\n" +
	"For `init`:\n" + InitPrompt + "\n\n" +
	"For anything else, run `agentbus2 $ARGUMENTS` in a shell and report the output.\n"

// grokRule is written to ~/.grok/rules/agentbus2.md. Grok loads every file
// there into the session.
const grokRule = "# Agentbus2\n\n" +
	"At the start of a session, call the Agentbus2 register tool and pass the\n" +
	"returned \"session\" on every later Agentbus2 call. So new messages wake you\n" +
	"while idle, run `agentbus2 wait` in a background shell (run_in_background);\n" +
	"when it exits 0, call receive, handle what concerns you, and start it again;\n" +
	"on exit 3 or 4 do nothing; on exit 2 report the error. The using-agentbus2 skill\n" +
	"is the full protocol. If the register tool is missing, tell the user to run\n" +
	"`agentbus2 init --global` and restart Grok.\n"

// installPS1 is the Windows installer one-liner; it puts agentbus2.exe on the
// user PATH.
const installPS1 = "irm https://github.com/ericfitz/agentbus2/releases/latest/download/install.ps1 | iex"

// requireOnPath refuses unless the agentbus2 that PATH resolves is this
// binary. The harness configs and hooks start "agentbus2" by name, and an
// unzipped agentbus2.exe is on no PATH, so on Windows init would otherwise
// configure harnesses that cannot start it (or that start an older copy).
func requireOnPath(lookPath func(string) (string, error), executable func() (string, error)) error {
	found, err := lookPath("agentbus2")
	if err != nil {
		return fmt.Errorf("agentbus2.exe is not on your PATH, so the harnesses could not start it. Install it with:\n  %s\nthen open a new terminal and run agentbus2 init again", installPS1)
	}
	self, err := executable()
	if err != nil {
		return fmt.Errorf("finding this agentbus2.exe: %w", err)
	}
	if !sameFile(found, self) {
		return fmt.Errorf("the agentbus2.exe on your PATH (%s) is not this agentbus2.exe (%s), so the harnesses would start that one. Install with:\n  %s\nor put this one's directory first on PATH, then open a new terminal and run agentbus2 init again", found, self, installPS1)
	}
	return nil
}

// sameFile reports whether a and b name the same file.
func sameFile(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

type initer struct {
	InitOptions
	out io.Writer
}

func (in *initer) say(format string, a ...any) { _, _ = fmt.Fprintf(in.out, format+"\n", a...) }

// write writes path (creating parents). A file that already holds data is left
// alone (nothing written, no backup); one that differs is copied to path.bak
// first.
func (in *initer) write(path string, data []byte) error {
	old, readErr := os.ReadFile(path)
	exists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if exists && string(old) == string(data) {
		in.say("  unchanged %s", path)
		return nil
	}
	if in.DryRun {
		if exists {
			in.say("would update %s (backup at %s.bak)", path, path)
		} else {
			in.say("would write %s", path)
		}
		return nil
	}
	if exists {
		if err := os.WriteFile(path+".bak", old, 0o600); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	if exists {
		in.say("updated %s (backup at %s.bak)", path, path)
	} else {
		in.say("wrote %s", path)
	}
	return nil
}

// run invokes a harness CLI, or only reports it under DryRun.
func (in *initer) run(name string, args ...string) error {
	if in.DryRun {
		in.say("would have run %s %s", name, strings.Join(args, " "))
		return nil
	}
	return in.Run(name, args...)
}

// wants reports whether harness h should be configured: forced by -harness,
// else detected by its home directory existing.
func (in *initer) wants(h, dir string) bool {
	if in.Harness != "" {
		return in.Harness == h
	}
	_, err := os.Stat(dir)
	return err == nil
}

func (in *initer) global() error {
	claudeDir := filepath.Join(in.Home, ".claude")
	codexDir := filepath.Join(in.Home, ".codex")
	grokDir := filepath.Join(in.Home, ".grok")
	did := false
	if in.wants("claude", claudeDir) {
		did = true
		if err := in.claude(claudeDir); err != nil {
			return err
		}
	}
	if in.wants("codex", codexDir) {
		did = true
		if err := in.codex(codexDir); err != nil {
			return err
		}
	}
	if in.wants("grok", grokDir) {
		did = true
		if err := in.grok(grokDir); err != nil {
			return err
		}
	}
	if !did {
		in.say("no harness found (no %s, %s, or %s); use -harness claude|codex|grok to force one", claudeDir, codexDir, grokDir)
		return nil
	}
	if err := in.defaultProfile(); err != nil {
		return err
	}
	in.say("done: restart the harness, then run `agentbus2 init` inside each repository")
	return nil
}

// defaultProfile writes config.json with profiles.default = {mode: local} when
// the file is absent, and leaves an existing file alone.
func (in *initer) defaultProfile() error {
	dir, err := profile.ConfigDir(in.Environ)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "config.json")
	if _, err := os.Stat(path); err == nil {
		in.say("  %s already present", path)
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if in.DryRun {
		in.say("would write %s", path)
		return nil
	}
	cfg := profile.Config{Path: path, Profiles: map[string]profile.Profile{"default": {Name: "default", Mode: profile.ModeLocal}}}
	if err := cfg.Save(); err != nil {
		return err
	}
	in.say("wrote %s", path)
	return nil
}

func (in *initer) claude(dir string) error {
	in.say("Claude Code:")
	if err := in.mcpEntry("claude", []string{"mcp", "remove", "-s", "user", "agentbus2"}, []string{"mcp", "add", "-s", "user", "agentbus2", "--", "agentbus2", "mcp"},
		`{ "mcpServers": { "agentbus2": { "command": "agentbus2", "args": ["mcp"] } } }`+" in ~/.claude.json"); err != nil {
		return err
	}
	if err := in.hooks(filepath.Join(dir, "settings.json"),
		hookSpec{"Stop", stopHookCommand, ""}, hookSpec{"SubagentStart", subagentHookCommand, ""}, hookSpec{"UserPromptSubmit", unparkHookCommand, ""}); err != nil {
		return err
	}
	return in.write(filepath.Join(dir, skillPath), SkillMD)
}

func (in *initer) codex(dir string) error {
	in.say("Codex:")
	if err := in.mcpEntry("codex", []string{"mcp", "remove", "agentbus2"}, []string{"mcp", "add", "agentbus2", "--", "agentbus2", "mcp"},
		"[mcp_servers.agentbus2]\n  command = \"agentbus2\"\n  args = [\"mcp\"]\n  tool_timeout_sec = 300\n  in ~/.codex/config.toml"); err != nil {
		return err
	}
	if err := in.codexTimeout(filepath.Join(dir, "config.toml")); err != nil {
		return err
	}
	if err := in.hooks(filepath.Join(dir, "hooks.json"), hookSpec{"Stop", stopHookCommand, ""}, hookSpec{"UserPromptSubmit", unparkHookCommand, ""}); err != nil {
		return err
	}
	if err := in.write(filepath.Join(dir, "prompts", "agentbus2.md"), []byte(codexPrompt)); err != nil {
		return err
	}
	if err := in.write(filepath.Join(in.Home, ".agents", skillPath), SkillMD); err != nil {
		return err
	}
	in.say("  Codex asks you to trust the Stop hook the first time it runs; accept it")
	return nil
}

func (in *initer) grok(dir string) error {
	in.say("Grok Build:")
	// Default tool_timeout_sec is 6000, already above any receive wait.
	if err := in.mcpEntry("grok",
		[]string{"mcp", "remove", "--scope", "user", "agentbus2"},
		[]string{"mcp", "add", "--scope", "user", "agentbus2", "--", "agentbus2", "mcp"},
		"[mcp_servers.agentbus2]\n  command = \"agentbus2\"\n  args = [\"mcp\"]\n  in ~/.grok/config.toml"); err != nil {
		return err
	}
	// Grok reads Stop hook stdout, and ~/.grok/hooks/*.json is trusted.
	if err := in.hooks(filepath.Join(dir, "hooks", "agentbus2.json"), hookSpec{"Stop", stopHookCommand, ""}, hookSpec{"UserPromptSubmit", unparkHookCommand, ""}); err != nil {
		return err
	}
	if err := in.write(filepath.Join(dir, skillPath), SkillMD); err != nil {
		return err
	}
	if err := in.write(filepath.Join(dir, "commands", "agentbus2.md"), []byte(grokCommand)); err != nil {
		return err
	}
	return in.write(filepath.Join(dir, "rules", "agentbus2.md"), []byte(grokRule))
}

// mcpEntry registers the MCP server through the harness's own CLI so the
// entry lands in the right file and format; without the CLI it prints the
// snippet to add by hand. Remove-then-add leaves the same entry on every run.
func (in *initer) mcpEntry(cli string, remove, add []string, snippet string) error {
	if _, err := in.LookPath(cli); err != nil {
		in.say("  %s CLI not on PATH; add this yourself:\n  %s", cli, snippet)
		return nil
	}
	_ = in.run(cli, remove...) // absent entry is fine
	if err := in.run(cli, add...); err != nil {
		return fmt.Errorf("%s %s: %w", cli, strings.Join(add, " "), err)
	}
	if !in.DryRun {
		in.say("  registered the agentbus2 MCP server via %s %s", cli, strings.Join(add, " "))
	}
	return nil
}

// codexTimeout ensures tool_timeout_sec = 300 under [mcp_servers.agentbus2] (a
// text edit; no TOML parser). A long receive wait must not be cut off by
// Codex's default tool timeout.
func (in *initer) codexTimeout(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // codex mcp add did not run (CLI missing); the snippet covers it
		}
		return err
	}
	lines := strings.Split(string(body), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "[mcp_servers.agentbus2]" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	for i := start + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "[") {
			break
		}
		if strings.HasPrefix(t, "tool_timeout_sec") {
			return nil
		}
	}
	out := append([]string{}, lines[:start+1]...)
	out = append(out, "tool_timeout_sec = 300")
	out = append(out, lines[start+1:]...)
	return in.write(path, []byte(strings.Join(out, "\n")))
}

// hookSpec is one event hook for in.hooks.
type hookSpec struct{ event, cmd, matcher string }

// hook merges one event hook; see hooks.
func (in *initer) hook(path, event, cmd, matcher string) error {
	return in.hooks(path, hookSpec{event, cmd, matcher})
}

// hooks merges event hooks running their commands into a Claude Code
// settings.json, Codex hooks.json, or Grok hooks file, leaving everything else
// in the file alone (Go's encoder re-sorts object keys and re-indents; numbers
// keep their text through json.Number). A hook
// already present is not added again; a file that already runs every hook is
// not rewritten at all; otherwise the file is written once, so its .bak holds
// the file as it was before init touched it. A "hooks" value, or an event's
// value, of a shape other than Claude Code's is refused rather than replaced.
func (in *initer) hooks(path string, specs ...hookSpec) error {
	doc := map[string]any{}
	if body, err := os.ReadFile(path); err == nil {
		if doc, err = decodeObject(body); err != nil {
			return fmt.Errorf("%s: %w (fix or remove it, then rerun)", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	hooks := map[string]any{}
	if v, ok := doc["hooks"]; ok && v != nil {
		m, isMap := v.(map[string]any)
		if !isMap {
			return fmt.Errorf("%s: \"hooks\" is not an object (fix or remove it, then rerun)", path)
		}
		hooks = m
	}
	added := false
	for _, sp := range specs {
		var entries []any
		if v, ok := hooks[sp.event]; ok && v != nil {
			list, isList := v.([]any)
			if !isList {
				return fmt.Errorf("%s: hooks.%s is not an array (fix or remove it, then rerun)", path, sp.event)
			}
			entries = list
		}
		present := false
		for _, s := range entries {
			if hasHookCommand(s, sp.cmd) {
				present = true
				break
			}
		}
		if present {
			in.say("  %s hook already present in %s", sp.event, path)
			continue
		}
		entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": sp.cmd}}}
		if sp.matcher != "" {
			entry["matcher"] = sp.matcher
		}
		hooks[sp.event] = append(entries, entry)
		in.say("  %s hook: %s", sp.event, sp.cmd)
		added = true
	}
	if !added {
		return nil
	}
	doc["hooks"] = hooks
	// No HTML escaping: the user's other commands keep their && and >.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	return in.write(path, buf.Bytes())
}

// decodeObject decodes one JSON object, numbers as json.Number so that a
// rewrite keeps their text. Anything else at the top level (null, an array, a
// scalar) or trailing data is refused.
func decodeObject(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON object")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}

// hasHookCommand reports whether a hook entry already runs cmd (as the whole
// command or followed by arguments, so "agentbus2 stop-hook" does not match
// "agentbus2 stop-hook-x").
func hasHookCommand(entry any, cmd string) bool {
	m, _ := entry.(map[string]any)
	inner, _ := m["hooks"].([]any)
	for _, h := range inner {
		hm, _ := h.(map[string]any)
		c, _ := hm["command"].(string)
		if c == cmd || strings.HasPrefix(c, cmd+" ") {
			return true
		}
	}
	return false
}

// mcpConfigured is a cheap check that some harness knows about agentbus2.
func (in *initer) mcpConfigured() bool {
	for _, p := range []string{
		filepath.Join(in.Home, ".claude.json"),
		filepath.Join(in.Home, ".codex", "config.toml"),
		filepath.Join(in.Home, ".grok", "config.toml"),
	} {
		if body, err := os.ReadFile(p); err == nil && strings.Contains(string(body), "agentbus2") {
			return true
		}
	}
	return false
}
