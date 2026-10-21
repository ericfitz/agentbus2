package profile

import (
	"fmt"

	"github.com/ericfitz/agentbus2/internal/repoconfig"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Selection holds the inputs of the profile selection order (Decision 21).
type Selection struct {
	Flag string // -profile
	Env  string // the AGENTBUS2_PROFILE value
	Cwd  string // where the nearest .local/agentbus2.json search starts
}

// Where a selected profile name came from.
const (
	SourceFlag    = "flag"
	SourceEnv     = "env"
	SourceRepo    = "repo"
	SourceDefault = "default"
)

// SelectName picks the profile name: the flag, then the environment, then the
// nearest repo file, then "default". The repo file is read only when the
// first two are unset; a malformed or non-profile-only file is an error and
// is never skipped.
func SelectName(sel Selection) (name, source string, err error) {
	if sel.Flag != "" {
		return sel.Flag, SourceFlag, nil
	}
	if sel.Env != "" {
		return sel.Env, SourceEnv, nil
	}
	if sel.Cwd != "" {
		f, err := repoconfig.Find(sel.Cwd)
		if err != nil {
			return "", "", err
		}
		if f != nil {
			return f.Profile, SourceRepo, nil
		}
	}
	return "default", SourceDefault, nil
}

// IdentitySelection holds the inputs of the identity selection order
// (Decision 71). Credentials names the files under credentials/<profile>/.
type IdentitySelection struct {
	Flag, Env   string
	Credentials []string
}

// ResolveIdentity picks the identity: the flag, the environment, the
// profile's identity, then, in server mode only, the one credential file when
// exactly one exists. The source is "flag", "env", "profile" or "credential".
func (p Profile) ResolveIdentity(sel IdentitySelection) (identity, source string, err error) {
	pick := func(id, src string) (string, string, error) {
		if err := store.FileNameRule(id); err != nil {
			return "", "", fmt.Errorf("identity %q (%s): %w", id, src, err)
		}
		return id, src, nil
	}
	switch {
	case sel.Flag != "":
		return pick(sel.Flag, "flag")
	case sel.Env != "":
		return pick(sel.Env, "env")
	case p.Identity != "":
		return pick(p.Identity, "profile")
	case p.Mode == ModeServer && len(sel.Credentials) == 1:
		return pick(sel.Credentials[0], "credential")
	}
	return "", "", fmt.Errorf("identity is required: pass -identity, set AGENTBUS2_IDENTITY, or set identity in profile %q", p.Name)
}
