package client

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
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
	// Maintenance (local mode) runs maintenance.New(c).Tick on a ticker.
	Maintenance bool
	Logger      *slog.Logger
	Now         func() time.Time // tests only

	Notifier     notify.Notifier // tests only; nil: notify.NewPolling(PollInterval)
	PollInterval time.Duration   // 0: 1 s (v1 WaitPollInterval)
	// Config (local mode, tests only) replaces core.Defaults(); core.New
	// validates it.
	Config *core.Config
	// HTTP is the server-mode base client; nil: a client with no Timeout.
	HTTP *http.Client
}

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
	if r.Profile, err = cfg.Resolve(name); err != nil {
		return Resolved{}, resolveErr(err)
	}
	r.ProfileSource = src
	r.Credentials = credential.Store{Dir: profile.CredentialDir(r.ConfigDir, r.Profile.Name)}
	var creds []string
	if r.Profile.Mode == profile.ModeServer {
		if creds, err = r.Credentials.List(); err != nil {
			return Resolved{}, resolveErr(err)
		}
	}
	envIdentity, _ := profile.Getenv(o.Environ, "AGENTBUS2_IDENTITY")
	r.Identity, r.IdentitySource, err = r.Profile.ResolveIdentity(profile.IdentitySelection{Flag: o.Identity, Env: envIdentity, Credentials: creds, Fallback: o.FallbackIdentity})
	if err != nil {
		return Resolved{}, resolveErr(err)
	}
	return r, nil
}
