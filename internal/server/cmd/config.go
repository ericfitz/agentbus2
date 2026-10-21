package cmd

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/server/config"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/postgres"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// flags holds the flag values `server` and `server admin` share (choice 14).
type flags struct {
	Config, Listen, Registration, SQLite, Postgres string
	Set                                            []string // each "name=value"
}

type setFlag struct{ f *flags }

func (s setFlag) String() string { return "" }
func (s setFlag) Set(v string) error {
	if name, _, ok := strings.Cut(v, "="); !ok || name == "" {
		return fmt.Errorf("%q is not name=value", v)
	}
	s.f.Set = append(s.f.Set, v)
	return nil
}

// flagSet defines the shared flags. -listen and -registration exist only when
// withListen (the server); `server admin` rejects them as unknown.
func flagSet(name string, f *flags, withListen bool) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&f.Config, "config", "", "configuration file (JSON)")
	fs.StringVar(&f.SQLite, "sqlite", "", "SQLite database file (exactly one of sqlite or postgres)")
	fs.StringVar(&f.Postgres, "postgres", "", "Postgres DSN (exactly one of sqlite or postgres)")
	fs.Var(setFlag{f}, "set", "override a setting, name=value (repeatable); settings: "+strings.Join(config.Settings(), ", "))
	if withListen {
		fs.StringVar(&f.Listen, "listen", "", "address to listen on: host:port or unix:<path>")
		fs.StringVar(&f.Registration, "registration", "", "registration mode: open, enrollment or closed")
	}
	return fs
}

// overrides turns the flags that were set into config overrides, then the
// -set pairs (split at the first '=').
func (f *flags) overrides(fs *flag.FlagSet) (config.Overrides, error) {
	o := config.Overrides{}
	named := map[string]string{
		"listen": f.Listen, "registration": f.Registration,
		"sqlite": f.SQLite, "postgres": f.Postgres,
	}
	fs.Visit(func(fl *flag.Flag) {
		if v, ok := named[fl.Name]; ok {
			o[fl.Name] = v
		}
	})
	for _, kv := range f.Set {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("-set %q is not name=value", kv)
		}
		o[name] = value
	}
	return o, nil
}

// configError marks a flag or configuration problem (exit 2), as opposed to
// a store open error (exit 1).
type configError struct{ error }

func (e *configError) Unwrap() error { return e.error }

// openFromConfig loads the configuration and opens the store it names. It
// never reads cfg.Listen. Flag and configuration errors are *configError.
func openFromConfig(ctx context.Context, f *flags, fs *flag.FlagSet, environ []string) (cfg config.Config, st store.Store, cc core.Config, err error) {
	cfg, cc, err = loadConfig(f, fs, environ)
	if err != nil {
		return cfg, nil, cc, err
	}
	st, err = openStore(ctx, cfg)
	if err != nil {
		return cfg, nil, cc, err
	}
	return cfg, st, cc, nil
}

// loadConfig merges the file, the environment and the flags and maps the
// result onto the core. It opens nothing. Errors are *configError.
func loadConfig(f *flags, fs *flag.FlagSet, environ []string) (cfg config.Config, cc core.Config, err error) {
	o, err := f.overrides(fs)
	if err != nil {
		return cfg, cc, &configError{err}
	}
	cfg, err = config.Load(f.Config, environ, o)
	if err != nil {
		return cfg, cc, &configError{err}
	}
	cc, err = cfg.Core()
	if err != nil {
		return cfg, cc, &configError{err}
	}
	return cfg, cc, nil
}

// openStore opens the store cfg names (Validate guarantees exactly one).
func openStore(ctx context.Context, cfg config.Config) (st store.Store, err error) {
	if cfg.SQLite != "" {
		st, err = sqlite.Open(ctx, cfg.SQLite, sqlite.Options{})
		if err != nil {
			err = fmt.Errorf("opening the sqlite store: %w", err)
		}
	} else {
		st, err = postgres.Open(ctx, cfg.Postgres, postgres.Options{})
		if err != nil {
			err = fmt.Errorf("opening the postgres store: %w", err)
		}
	}
	if err != nil {
		return nil, err
	}
	return st, nil
}
