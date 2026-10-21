package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/repoconfig"
)

// InitOptions configures Init. Zero values mean "detect".
type InitOptions struct {
	Global  bool   // force the machine-level bootstrap even inside a repo
	Harness string // "", "claude", "codex", or "grok": force one harness
	DryRun  bool   // print actions, write nothing
	// Profile and Identity are the repository step's -profile and -identity.
	Profile, Identity string
	Home              string   // home directory; "": os.UserHomeDir
	Cwd               string   // working directory; "": os.Getwd
	Environ           []string // nil: os.Environ
	// LookPath and Run are the harness CLI seams (exec.LookPath / exec.Command).
	LookPath func(string) (string, error)
	Run      func(name string, args ...string) error
	// OS and Executable are the PATH check's seams (runtime.GOOS /
	// os.Executable).
	OS         string
	Executable func() (string, error)
}

// Init bootstraps Agentbus2 for the harnesses on this machine (global) or for
// the repository containing Cwd. Inside a git repository the repository step
// runs unless Global is set; outside one the global step runs (Milestone 6
// choice 13; ported from v1 internal/cli/init.go).
func Init(o InitOptions, out io.Writer) error {
	if o.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		o.Home = h
	}
	if o.Cwd == "" {
		c, err := os.Getwd()
		if err != nil {
			return err
		}
		o.Cwd = c
	}
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Run == nil {
		o.Run = func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
			return cmd.Run()
		}
	}
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Executable == nil {
		o.Executable = os.Executable
	}
	switch o.Harness {
	case "", "claude", "codex", "grok":
	default:
		return fmt.Errorf("-harness must be claude, codex, or grok, got %q", o.Harness)
	}
	if o.OS == "windows" {
		if err := requireOnPath(o.LookPath, o.Executable); err != nil {
			return err
		}
	}
	in := &initer{InitOptions: o, out: out}
	if root := gitRoot(o.Cwd); root != "" && !o.Global {
		return in.repo(root)
	}
	return in.global()
}

// RunInit is `agentbus2 init [-global] [-harness H] [-dry-run] [-profile P]
// [-identity I]`.
func RunInit(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_, _ = ctx, stdin
	fs := newFlagSet("init", stderr)
	var o InitOptions
	fs.BoolVar(&o.Global, "global", false, "configure the harnesses on this machine even inside a repository")
	fs.StringVar(&o.Harness, "harness", "", "configure only this harness: claude, codex or grok")
	fs.BoolVar(&o.DryRun, "dry-run", false, "print the actions and write nothing")
	fs.StringVar(&o.Profile, "profile", "", "profile for this repository (default: the repository name)")
	fs.StringVar(&o.Identity, "identity", "", "identity of the repository's new profile (default: the repository name)")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "agentbus2 init: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	switch o.Harness {
	case "", "claude", "codex", "grok":
	default:
		_, _ = fmt.Fprintf(stderr, "agentbus2 init: -harness must be claude, codex, or grok, got %q\n", o.Harness)
		return 2
	}
	o.Environ = environ
	if o.Home == "" {
		if h, ok := profile.Getenv(environ, "HOME"); ok {
			o.Home = h
		}
	}
	if err := Init(o, stdout); err != nil {
		var ue *initUsageError
		if errors.As(err, &ue) {
			_, _ = fmt.Fprintf(stderr, "agentbus2 init: %v\n", err)
			return 2
		}
		return fail(stderr, err)
	}
	return 0
}

// initUsageError marks an init failure the operator fixes with a flag.
type initUsageError struct{ msg string }

func (e *initUsageError) Error() string { return e.msg }

// repo is the repository step (Decision (Eric, 2026-10-21), item 5): select
// or create the repository's profile, write .local/agentbus2.json, git-ignore
// .local/ and print the next step.
func (in *initer) repo(root string) error {
	if !in.mcpConfigured() {
		in.say("warning: no agentbus2 MCP entry found in ~/.claude.json, ~/.codex/config.toml, or ~/.grok/config.toml; run `agentbus2 init -global` first")
	}
	base := filepath.Base(root)
	name := in.Profile
	existing, err := repoconfig.Find(root)
	if err != nil {
		return err
	}
	switch {
	case name != "":
	case existing != nil:
		name = existing.Profile
	default:
		name = base
	}
	if err := repoconfig.ValidProfileName(name); err != nil {
		return &initUsageError{fmt.Sprintf("profile name %q is not usable (%v); pass -profile", name, err)}
	}
	cfgDir, err := profile.ConfigDir(in.Environ)
	if err != nil {
		return err
	}
	cfg, err := profile.Load(filepath.Join(cfgDir, "config.json"))
	if err != nil {
		return err
	}
	p, defined := cfg.Profiles[name]
	if !defined {
		id := in.Identity
		if id == "" {
			id = base
			if err := validIdentity(id); err != nil {
				return &initUsageError{fmt.Sprintf("repository name %q is not a valid identity (%v); pass -identity", id, err)}
			}
		} else if err := validIdentity(id); err != nil {
			return &initUsageError{fmt.Sprintf("-identity %q: %v", id, err)}
		}
		p = profile.Profile{Name: name, Mode: profile.ModeLocal, Identity: id}
		if d, ok := cfg.Profiles["default"]; ok && d.Mode == profile.ModeServer {
			p.Mode, p.Server = d.Mode, d.Server
		}
		if err := in.addProfile(cfg, p); err != nil {
			return err
		}
	} else {
		in.say("  profile %s already defined in %s; left unchanged", name, cfg.Path)
	}
	if err := in.repoFile(root, existing, name); err != nil {
		return err
	}
	if err := in.gitignore(filepath.Join(root, ".gitignore")); err != nil {
		return err
	}
	in.nextStep(cfgDir, p)
	return nil
}

// addProfile adds p to config.json, copying the file to config.json.bak first
// when it exists.
func (in *initer) addProfile(cfg profile.Config, p profile.Profile) error {
	if in.DryRun {
		in.say("would add profile %s (mode %s, identity %s) to %s", p.Name, p.Mode, p.Identity, cfg.Path)
		return nil
	}
	if old, err := os.ReadFile(cfg.Path); err == nil {
		if err := os.WriteFile(cfg.Path+".bak", old, 0o600); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	cfg.Profiles[p.Name] = p
	if err := cfg.Save(); err != nil {
		return err
	}
	in.say("added profile %s (mode %s, identity %s) to %s", p.Name, p.Mode, p.Identity, cfg.Path)
	return nil
}

// repoFile writes <root>/.local/agentbus2.json unless it already selects name.
func (in *initer) repoFile(root string, existing *repoconfig.File, name string) error {
	path := filepath.Join(root, filepath.FromSlash(repoconfig.FileName))
	if existing != nil && existing.Profile == name {
		in.say("  %s already selects %s", path, name)
		return nil
	}
	if in.DryRun {
		in.say("would write %s selecting profile %s", path, name)
		return nil
	}
	if _, err := repoconfig.Write(root, name); err != nil {
		return err
	}
	in.say("wrote %s selecting profile %s", path, name)
	return nil
}

// nextStep prints what the operator does next.
func (in *initer) nextStep(cfgDir string, p profile.Profile) {
	if p.Mode == profile.ModeServer {
		cs := credential.Store{Dir: profile.CredentialDir(cfgDir, p.Name)}
		if !cs.Exists(p.Identity) {
			in.say("next: run `agentbus2 register` to create your credential on %s", p.Server)
			return
		}
	}
	in.say("next: restart the harness and call the register tool")
}

func (in *initer) gitignore(path string) error {
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, l := range strings.Split(string(body), "\n") {
		switch strings.TrimSpace(l) {
		case ".local", ".local/", "/.local", "/.local/":
			return nil
		}
	}
	s := string(body)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return in.write(path, []byte(s+".local/\n"))
}

// gitRoot returns the nearest ancestor of dir (inclusive) containing .git, or
// "" when dir is not inside a repository.
func gitRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// validIdentity applies the identity name rule through the profile package, so
// package cli does not import the store (the Task 5 import rule).
func validIdentity(id string) error {
	if id == "" {
		return errors.New("must not be empty")
	}
	err := profile.Profile{Mode: profile.ModeLocal, Identity: id}.Validate()
	if err == nil {
		return nil
	}
	return errors.Unwrap(err)
}
