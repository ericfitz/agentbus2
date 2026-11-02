package client

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// SelectedProfile is the profile a command works on, before any identity
// resolution: the config file, the name and its source, and the profile (the
// built-in local profile for an undefined "default").
type SelectedProfile struct {
	Config    profile.Config
	ConfigDir string
	Name      string
	Defined   bool // the name has an entry in config.json
	Profile   profile.Profile
}

// SelectProfile applies Decision 21's selection order and loads config.json.
// An undefined name other than "default" is returned with Defined false and a
// zero Profile, so register can create it; callers that cannot create it turn
// that into an error.
func SelectProfile(o Options) (SelectedProfile, error) {
	var s SelectedProfile
	var err error
	if s.ConfigDir, err = profile.ConfigDir(o.Environ); err != nil {
		return s, resolveErr(err)
	}
	if s.Config, err = profile.Load(filepath.Join(s.ConfigDir, "config.json")); err != nil {
		return s, resolveErr(err)
	}
	cwd := o.Cwd
	if cwd == "" {
		cwd = "."
	}
	env, _ := profile.Getenv(o.Environ, "AGENTBUS2_PROFILE")
	if s.Name, _, err = profile.SelectName(profile.Selection{Flag: o.Profile, Env: env, Cwd: cwd}); err != nil {
		return s, resolveErr(err)
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

// RegisterRequest is RegisterIdentity's input. Options.Server names the server
// (creating the profile when it is undefined); Options.Credential, when set,
// registers that file's key instead of one from the credential store.
type RegisterRequest struct {
	Options         Options
	EnrollmentToken string
	Algorithm       keys.Algorithm
	// AlgorithmExplicit: the caller chose Algorithm, so a saved, pending or
	// file key of another algorithm is refused instead of reused.
	AlgorithmExplicit bool
}

// RegisterOutcome is what RegisterIdentity did. Created is the server's answer
// (false: it already held this key); ProfileCreated is true when the profile
// was added to config.json.
type RegisterOutcome struct {
	Profile, Server, Identity string
	Created, ProfileCreated   bool
}

// PendingConflictError is a registration refused with a conflict while the key
// came from the pending file a failed run left: the server holds another key.
// Err is the server's error; Hint is the sentence that tells the operator what
// to do (the one place it lives).
type PendingConflictError struct {
	Path, Identity string
	Err            error
}

func (e *PendingConflictError) Error() string { return e.Err.Error() }
func (e *PendingConflictError) Unwrap() error { return e.Err }

// Hint is the operator's next step, without a command prefix.
func (e *PendingConflictError) Hint() string {
	return fmt.Sprintf("the pending key %s differs from the key the server holds: remove it, or ask an administrator to reset the credential for %q", e.Path, e.Identity)
}

// RegisterIdentity is the registration both binaries share (Milestone 6 choice
// 14; Decision (Eric, 2026-10-21), item 6): generate a key, write it to the
// pending file (credential.Store.PendingPath), register its public half and,
// once the server answered 201, save that key as the credential and remove the
// pending file if it still holds it. A failed run leaves the pending file and
// the next run reuses that key. A run after success reuses the saved key and
// registers it again, which the server answers idempotently (Milestone 5
// choice 8). Overlapping runs for one identity race on the pending file: the
// last write wins and the loser's key is no longer the pending one.
//
// With Options.Credential the key is the named file's: no pending file, no
// credential file, and config.json never records the path (open note:
// mcp-only-client 4). Every usage error is a *ResolveError.
func RegisterIdentity(ctx context.Context, req RegisterRequest) (RegisterOutcome, error) {
	var out RegisterOutcome
	o := req.Options
	alg := req.Algorithm
	if alg != keys.EdDSA && alg != keys.ES256 {
		return out, resolveErr(fmt.Errorf("-algorithm must be EdDSA or ES256, got %q", string(alg)))
	}
	sel, err := SelectProfile(o)
	if err != nil {
		return out, err
	}
	p := sel.Profile
	creating := false
	server := strings.TrimSuffix(o.Server, "/")
	switch {
	case !sel.Defined && o.Server != "":
		p = profile.Profile{Name: sel.Name, Mode: profile.ModeServer, Server: server}
		creating = true
	case !sel.Defined && sel.Name != "default":
		return out, resolveErr(fmt.Errorf("profile %q is not defined in %s; pass -server to create it", sel.Name, sel.Config.Path))
	case p.Mode == profile.ModeLocal:
		return out, resolveErr(errors.New("register applies to server profiles only; a local profile needs no registration"))
	case o.Server != "" && server != p.Server:
		return out, resolveErr(fmt.Errorf("profile %q uses server %s; -server %s disagrees", p.Name, p.Server, o.Server))
	}
	if err := p.Validate(); err != nil {
		return out, resolveErr(fmt.Errorf("profile %q: %w", p.Name, err))
	}
	if err := checkCredentialFlag(o, p); err != nil {
		return out, err
	}
	cs := credential.Store{Dir: profile.CredentialDir(sel.ConfigDir, p.Name)}
	var have []string
	if !creating && o.Credential == "" {
		if have, err = cs.List(); err != nil {
			return out, resolveErr(err)
		}
	}
	envID, _ := profile.Getenv(o.Environ, "AGENTBUS2_IDENTITY")
	identity, _, err := p.ResolveIdentity(profile.IdentitySelection{Flag: o.Identity, Env: envID, Credentials: have})
	if err != nil {
		return out, resolveErr(err)
	}

	// Key choice: the -credential file, else the saved credential, else the
	// pending key a failed run left, else a new one written to the pending file
	// before the request. A crash or a lost 201 after the server committed then
	// keeps the key, and the retry registers the same key, which the server
	// answers idempotently without consulting the (now used) enrollment token.
	var signer crypto.Signer
	fromPending := false
	switch {
	case o.Credential != "":
		k, have, err := credential.LoadFile(o.Credential)
		if err != nil {
			return out, resolveErr(err)
		}
		if req.AlgorithmExplicit && have != alg {
			return out, resolveErr(fmt.Errorf("the key in %s is %s, not %s", o.Credential, have, alg))
		}
		signer = k
	case cs.Exists(identity):
		k, have, err := cs.Load(identity)
		if err != nil {
			return out, resolveErr(err)
		}
		if req.AlgorithmExplicit && have != alg {
			return out, resolveErr(fmt.Errorf("the saved credential for %q is %s, not %s; remove %s to register a new key", identity, have, alg, cs.Path(identity)))
		}
		signer = k
		// A credential wins over a pending key left by an earlier run.
		if err := cs.DiscardPending(identity); err != nil {
			return out, err
		}
	default:
		k, have, ok, err := cs.LoadPending(identity)
		if err != nil {
			return out, resolveErr(err)
		}
		if ok {
			if req.AlgorithmExplicit && have != alg {
				return out, resolveErr(fmt.Errorf("the pending key for %q is %s, not %s; remove %s to register a new key", identity, have, alg, cs.PendingPath(identity)))
			}
			signer, fromPending = k, true
			break
		}
		if signer, err = keys.Generate(alg); err != nil {
			return out, err
		}
		if err := cs.SavePending(identity, signer); err != nil {
			return out, err
		}
		fromPending = true
	}
	res, err := Register(ctx, o.HTTP, p.Server, identity, signer.Public(), req.EnrollmentToken)
	if err != nil {
		if fromPending && bus.CodeOf(err) == bus.CodeConflict {
			return out, &PendingConflictError{Path: cs.PendingPath(identity), Identity: identity, Err: err}
		}
		return out, err
	}
	if fromPending {
		if err := cs.CommitPending(identity, signer); err != nil {
			return out, err
		}
	}
	if creating {
		if sel.Config.Profiles == nil {
			sel.Config.Profiles = map[string]profile.Profile{}
		}
		sel.Config.Profiles[p.Name] = profile.Profile{Name: p.Name, Mode: profile.ModeServer, Server: p.Server, Identity: identity}
		if err := sel.Config.Save(); err != nil {
			return out, err
		}
	}
	return RegisterOutcome{Profile: p.Name, Server: p.Server, Identity: res.Identity, Created: res.Created, ProfileCreated: creating}, nil
}
