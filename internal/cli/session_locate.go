package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/procs"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

// located is the session a watch, a wait or a hook works on (choice 9).
type located struct {
	Session string
	// DisplayName, ResumeToken, Profile, Identity and HarnessClient are ""
	// when the session came from -session with no state file.
	DisplayName, ResumeToken, Profile, Identity, HarnessClient string
	// FromFile: a state file was read, so Profile and Identity bind the Conn.
	FromFile bool
	// Parked is the state file's mark (decision 18).
	Parked  bool
	Harness procs.Ref
	// HarnessFound is false on a non-ErrGone lookup error: the harness watch
	// is disabled for the run (v1 startWaiter).
	HarnessFound bool
}

// bindProfile applies choice 9: a session read from a state file belongs to
// the file's profile and identity. Empty flags take the file's values; a flag
// that disagrees is an error.
func bindProfile(o *client.Options, l located) error {
	if !l.FromFile {
		return nil
	}
	if o.Profile == "" {
		o.Profile = l.Profile
	} else if o.Profile != l.Profile {
		return fmt.Errorf("session %s belongs to profile %q as %q; -profile %q disagrees", l.Session, l.Profile, l.Identity, o.Profile)
	}
	if o.Identity == "" {
		o.Identity = l.Identity
	} else if o.Identity != l.Identity {
		return fmt.Errorf("session %s belongs to profile %q as %q; -identity %q disagrees", l.Session, l.Profile, l.Identity, o.Identity)
	}
	return nil
}

func withFile(l located, f sessionstate.File) located {
	l.Session = f.Session
	l.DisplayName, l.ResumeToken, l.Profile, l.Identity, l.HarnessClient = f.DisplayName, f.ResumeToken, f.Profile, f.Identity, f.HarnessClient
	l.FromFile, l.Parked = true, f.Parked
	return l
}

// locateSession finds the session without a bus call. The harness lookup
// comes first for every caller: ErrGone is errHarnessGone at once (an
// orphaned process has no harness to outlive); any other lookup error leaves
// HarnessFound false and the command proceeds. Then -session names the
// session (its state file, when one exists, supplies the rest), or the state
// file of the harness does.
func locateSession(dataDir, flagSession string, t procs.Table, self int) (located, error) {
	var l located
	h, err := procs.FindHarness(t, self)
	switch {
	case errors.Is(err, procs.ErrGone):
		return located{}, errHarnessGone
	case err == nil:
		l.Harness, l.HarnessFound = h, true
	}
	if flagSession != "" {
		l.Session = flagSession
		f, err := sessionstate.Read(dataDir, flagSession)
		switch {
		case err == nil:
			return withFile(l, f), nil
		case errors.Is(err, fs.ErrNotExist):
			return l, nil
		default:
			return located{}, err
		}
	}
	if l.HarnessFound {
		f, ok, err := sessionstate.ForHarness(dataDir, l.Harness)
		if err != nil {
			return located{}, err
		}
		if ok {
			return withFile(l, f), nil
		}
	}
	return located{}, errors.New("no session for this harness; pass -session or start agentbus2 mcp")
}

// displayNameFor is the display name for a session that came without a state
// file: the session's entry in Identities, else the identity name.
func displayNameFor(ctx context.Context, conn *client.Conn, l located) string {
	if l.DisplayName != "" {
		return l.DisplayName
	}
	if ids, err := conn.Bus.Identities(ctx); err == nil {
		for _, id := range ids {
			for _, s := range id.Sessions {
				if s.ID == l.Session && s.DisplayName != "" {
					return s.DisplayName
				}
			}
		}
	}
	return conn.Resolved.Identity
}

// resumeIfLapsed answers a session_lapsed error with one Open{ResumeToken}
// when the state file supplied a token. True means the caller re-arms.
func resumeIfLapsed(ctx context.Context, s bus.Sessions, l located, err error) (bool, error) {
	if bus.CodeOf(err) != bus.CodeSessionLapsed || l.ResumeToken == "" {
		return false, err
	}
	if _, oerr := s.Open(ctx, bus.OpenSessionInput{ResumeToken: l.ResumeToken}); oerr != nil {
		return false, oerr
	}
	return true, nil
}
