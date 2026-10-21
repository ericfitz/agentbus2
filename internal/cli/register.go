package cli

import (
	"context"
	"crypto"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// selectedProfile is the profile a command works on, before any identity
// resolution: the config file, the name and its source, and the profile (the
// built-in local profile for an undefined "default").
type selectedProfile struct {
	Config    profile.Config
	ConfigDir string
	Name      string
	Defined   bool // the name has an entry in config.json
	Profile   profile.Profile
}

// selectProfile applies Decision 21's selection order and loads config.json.
// An undefined name other than "default" is returned with Defined false and a
// zero Profile, so register can create it; callers that cannot create it turn
// that into an error with errUndefined.
func selectProfile(o client.Options) (selectedProfile, error) {
	var s selectedProfile
	var err error
	if s.ConfigDir, err = profile.ConfigDir(o.Environ); err != nil {
		return s, &client.ResolveError{Err: err}
	}
	if s.Config, err = profile.Load(filepath.Join(s.ConfigDir, "config.json")); err != nil {
		return s, &client.ResolveError{Err: err}
	}
	cwd := o.Cwd
	if cwd == "" {
		cwd = "."
	}
	env, _ := profile.Getenv(o.Environ, "AGENTBUS2_PROFILE")
	if s.Name, _, err = profile.SelectName(profile.Selection{Flag: o.Profile, Env: env, Cwd: cwd}); err != nil {
		return s, &client.ResolveError{Err: err}
	}
	if p, ok := s.Config.Profiles[s.Name]; ok {
		s.Defined, s.Profile = true, p
		return s, nil
	}
	if p, err := s.Config.Resolve(s.Name); err == nil {
		s.Profile = p // the built-in default
	}
	return s, nil
}

// RunRegister is `agentbus2 register [-profile P] [-identity I] [-server URL]
// [-enrollment-token T] [-algorithm EdDSA|ES256]` (Milestone 6 choice 14;
// Decision (Eric, 2026-10-21), item 6): generate a key, register its public
// half, and save the credential only after the server answered 201. A second
// run reuses the saved key and registers it again, which the server answers
// idempotently (Milestone 5 choice 8).
func RunRegister(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin
	fs := newFlagSet("register", stderr)
	o := client.Options{Environ: environ}
	client.Flags(fs, &o)
	var server, token, algName string
	fs.StringVar(&server, "server", "", "server URL; creates the profile when it is not defined")
	fs.StringVar(&token, "enrollment-token", "", "enrollment token, for a server in enrollment mode")
	fs.StringVar(&algName, "algorithm", string(keys.EdDSA), "key algorithm: EdDSA or ES256")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "agentbus2 register: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	alg := keys.Algorithm(algName)
	if alg != keys.EdDSA && alg != keys.ES256 {
		_, _ = fmt.Fprintf(stderr, "agentbus2 register: -algorithm must be EdDSA or ES256, got %q\n", algName)
		return 2
	}
	algExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "algorithm" {
			algExplicit = true
		}
	})
	usage := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "agentbus2 register: %v\n", err)
		return 2
	}

	sel, err := selectProfile(o)
	if err != nil {
		return usage(err)
	}
	p := sel.Profile
	creating := false
	switch {
	case !sel.Defined && server != "":
		p = profile.Profile{Name: sel.Name, Mode: profile.ModeServer, Server: strings.TrimSuffix(server, "/")}
		creating = true
	case !sel.Defined && sel.Name != "default":
		return usage(fmt.Errorf("profile %q is not defined in %s; pass -server to create it", sel.Name, sel.Config.Path))
	case p.Mode == profile.ModeLocal:
		return usage(errors.New("register applies to server profiles only; a local profile needs no registration"))
	case server != "" && strings.TrimSuffix(server, "/") != p.Server:
		return usage(fmt.Errorf("profile %q uses server %s; -server %s disagrees", p.Name, p.Server, server))
	}
	if err := p.Validate(); err != nil {
		return usage(fmt.Errorf("profile %q: %w", p.Name, err))
	}
	cs := credential.Store{Dir: profile.CredentialDir(sel.ConfigDir, p.Name)}
	var have []string
	if !creating {
		if have, err = cs.List(); err != nil {
			return usage(err)
		}
	}
	envID, _ := profile.Getenv(environ, "AGENTBUS2_IDENTITY")
	identity, _, err := p.ResolveIdentity(profile.IdentitySelection{Flag: o.Identity, Env: envID, Credentials: have})
	if err != nil {
		return usage(err)
	}

	// Reuse the saved key, else generate one. Nothing is written until the
	// server answers 201.
	var signer crypto.Signer
	if cs.Exists(identity) {
		k, have, err := cs.Load(identity)
		if err != nil {
			return usage(err)
		}
		if algExplicit && have != alg {
			return usage(fmt.Errorf("the saved credential for %q is %s, not %s; remove %s to register a new key", identity, have, alg, cs.Path(identity)))
		}
		signer = k
	} else {
		if signer, err = keys.Generate(alg); err != nil {
			return fail(stderr, err)
		}
	}
	res, err := client.Register(ctx, nil, p.Server, identity, signer.Public(), token)
	if err != nil {
		var re *client.ResolveError
		if errors.As(err, &re) {
			return usage(err)
		}
		return fail(stderr, err)
	}
	if err := cs.Save(identity, signer); err != nil {
		return fail(stderr, err)
	}
	if creating {
		sel.Config.Profiles[p.Name] = profile.Profile{Name: p.Name, Mode: profile.ModeServer, Server: p.Server, Identity: identity}
		if err := sel.Config.Save(); err != nil {
			return fail(stderr, err)
		}
	}
	state := "existing key"
	if res.Created {
		state = "created"
	}
	_, _ = fmt.Fprintf(stdout, "registered %s at %s (%s)\n", res.Identity, p.Server, state)
	return 0
}
