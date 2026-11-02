package client

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// Options configure Resolve and Open.
type Options struct {
	Environ []string
	Cwd     string // "": os.Getwd
	// Profile and Identity are the -profile and -identity flags.
	Profile, Identity string
	// FallbackIdentity is the identity used when nothing else selects one
	// (Decision 71's last step); only the TUI sets it, to the OS login name.
	FallbackIdentity string
	// HumanIdentity (the TUI only) ignores the profile's identity when the
	// repository file selected the profile, so the human never shares the
	// agent's identity there (TUI decision 11).
	HumanIdentity bool
	// HumanSignIn (server mode) makes the caller a human who signs in through
	// the profile's oidc provider (spec 5.3): no identity is selected and Open
	// obtains the bearer, opening the browser when there is no usable session.
	// The identity is then the bearer's subject (IdentitySource "sign-in").
	HumanSignIn bool
	// HumanOpenBrowser and HumanAnnounce replace the system browser launcher
	// and receive the sign-in URL (HumanSignIn only; nil: the system browser,
	// no announcement).
	HumanOpenBrowser func(url string) error
	HumanAnnounce    func(url string)
	// HumanDeferSignIn (with HumanSignIn) makes Open obtain nothing: the caller
	// drives Conn.Human itself and Resolved.Identity stays empty until it has.
	HumanDeferSignIn bool
	// Maintenance (local mode) runs maintenance.New(c).Tick on a ticker.
	Maintenance bool
	Logger      *slog.Logger
	// OpenLogger receives only the SQLite open warning (local mode). The core,
	// the in-process handler and the maintenance loop never use it, so their
	// warnings cannot reach a terminal (the TUI owns the screen). Logger, when
	// set, receives the open warning too.
	OpenLogger *slog.Logger
	Now        func() time.Time // tests only

	Notifier     notify.Notifier // tests only; nil: notify.NewPolling(PollInterval)
	PollInterval time.Duration   // 0: 1 s (v1 WaitPollInterval)
	// Local opens a local profile; the full binary passes localmode.Open; nil:
	// a local profile is a ResolveError (NoLocalModeError).
	// open note: mcp-only-client 1
	Local LocalOpener
	// Server and Credential are the MCP-only binary's -server and -credential;
	// the full binary sets neither. Server names the server of an undefined
	// profile (nothing is written) or must agree with a defined one; Credential
	// is a private key file that signs instead of the profile's credential
	// store. open note: mcp-only-client 3, 4
	Server, Credential string
	// HTTP is the server-mode base client; nil: a client with no Timeout.
	HTTP *http.Client
}

// LocalOpener builds the Conn of a local (SQLite) profile. It lives outside
// this package so a binary that never opens a local profile links no core,
// server or Store engine (internal/client/localmode is the one implementation).
type LocalOpener func(ctx context.Context, o Options, r Resolved) (*Conn, error)

// Flags registers -profile and -identity on fs, writing into o.
func Flags(fs *flag.FlagSet, o *Options) {
	fs.StringVar(&o.Profile, "profile", "", "profile name (default: AGENTBUS2_PROFILE, then .local/agentbus2.json, then default)")
	fs.StringVar(&o.Identity, "identity", "", "identity (default: AGENTBUS2_IDENTITY, then the profile's identity, then the profile's only credential)")
}

// ResolveError marks a profile, identity or credential resolution failure: a
// usage error the operator fixes by changing a flag, a variable or a file,
// as opposed to a runtime failure.
type ResolveError struct{ Err error }

func (e *ResolveError) Error() string { return e.Err.Error() }
func (e *ResolveError) Unwrap() error { return e.Err }

func resolveErr(err error) error { return &ResolveError{Err: err} }

// NoLocalModeError is the ResolveError for a local profile in a binary that
// has no local mode (Options.Local nil).
func NoLocalModeError(profileName string) error {
	return resolveErr(fmt.Errorf("profile %q is a local profile, and this binary has no local mode: pass -server URL, or use agentbus2", profileName))
}

// Resolved is the outcome of the selection rules.
type Resolved struct {
	Profile                  profile.Profile
	ProfileSource            string
	Identity, IdentitySource string
	ConfigDir, DataDir       string
	Credentials              credential.Store
}

// Resolve applies the directory rules, loads config.json and selects the
// profile (Decision 21) and the identity (Decision 71). It touches no network
// and creates nothing.
func Resolve(o Options) (Resolved, error) {
	var r Resolved
	var err error
	if r.ConfigDir, err = profile.ConfigDir(o.Environ); err != nil {
		return Resolved{}, resolveErr(err)
	}
	if r.DataDir, err = profile.DataDir(o.Environ); err != nil {
		return Resolved{}, resolveErr(err)
	}
	cfg, err := profile.Load(filepath.Join(r.ConfigDir, "config.json"))
	if err != nil {
		return Resolved{}, resolveErr(err)
	}
	cwd := o.Cwd
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return Resolved{}, resolveErr(fmt.Errorf("working directory: %w", err))
		}
	}
	envProfile, _ := profile.Getenv(o.Environ, "AGENTBUS2_PROFILE")
	name, src, err := profile.SelectName(profile.Selection{Flag: o.Profile, Env: envProfile, Cwd: cwd})
	if err != nil {
		return Resolved{}, resolveErr(err)
	}
	if r.Profile, err = resolveProfile(cfg, name, o.Server); err != nil {
		return Resolved{}, err
	}
	if err := checkCredentialFlag(o, r.Profile); err != nil {
		return Resolved{}, err
	}
	r.ProfileSource = src
	r.Credentials = credential.Store{Dir: profile.CredentialDir(r.ConfigDir, r.Profile.Name)}
	var creds []string
	if r.Profile.Mode == profile.ModeServer && !o.HumanSignIn && o.Credential == "" {
		if creds, err = r.Credentials.List(); err != nil {
			return Resolved{}, resolveErr(err)
		}
	}
	if o.HumanSignIn {
		if r.Profile.Mode != profile.ModeServer || r.Profile.OIDC == nil {
			return Resolved{}, resolveErr(fmt.Errorf("profile %q has no oidc setting: on a server profile a human signs in through the corporate provider", r.Profile.Name))
		}
		r.IdentitySource = "sign-in"
		return r, nil
	}
	envIdentity, _ := profile.Getenv(o.Environ, "AGENTBUS2_IDENTITY")
	r.Identity, r.IdentitySource, err = r.Profile.ResolveIdentity(profile.IdentitySelection{Flag: o.Identity, Env: envIdentity, Credentials: creds, Fallback: o.FallbackIdentity, SkipProfile: o.HumanIdentity && src == profile.SourceRepo})
	if err != nil {
		return Resolved{}, resolveErr(err)
	}
	return r, nil
}

// resolveProfile is Config.Resolve plus the -server flag: with server set, an
// undefined name (the built-in default included) is an in-memory server
// profile, a defined server profile must name the same server, and a defined
// local profile is refused.
func resolveProfile(cfg profile.Config, name, server string) (profile.Profile, error) {
	if server == "" {
		p, err := cfg.Resolve(name)
		if err != nil {
			return profile.Profile{}, resolveErr(err)
		}
		return p, nil
	}
	want := strings.TrimSuffix(server, "/")
	p, defined := cfg.Profiles[name]
	switch {
	case !defined:
		p = profile.Profile{Name: name, Mode: profile.ModeServer, Server: want}
		if err := p.Validate(); err != nil {
			return profile.Profile{}, resolveErr(fmt.Errorf("profile %q: %w", name, err))
		}
	case p.Mode == profile.ModeLocal:
		return profile.Profile{}, resolveErr(fmt.Errorf("profile %q is a local profile; -server applies to server profiles", name))
	case p.Server != want:
		return profile.Profile{}, resolveErr(fmt.Errorf("profile %q uses server %s; -server %s disagrees", name, p.Server, server))
	}
	return p, nil
}

// checkCredentialFlag refuses -credential where it has no meaning: a local
// profile, a credential_process profile and a human sign-in. open note:
// mcp-only-client 4 (the flag's key file is read-only; see Options.Credential).
func checkCredentialFlag(o Options, p profile.Profile) error {
	if o.Credential == "" {
		return nil
	}
	switch {
	case p.Mode == profile.ModeLocal:
		return resolveErr(fmt.Errorf("-credential applies to server profiles; profile %q is local", p.Name))
	case len(p.CredentialProcess) > 0:
		return resolveErr(fmt.Errorf("profile %q sets credential_process; -credential cannot be combined with it", p.Name))
	case o.HumanSignIn:
		return resolveErr(errors.New("-credential does not apply to a human sign-in"))
	}
	return nil
}
