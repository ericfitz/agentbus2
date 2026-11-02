package mcpcli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// maxTokenFile is the largest enrollment token file (open note:
// mcp-only-client 13).
const maxTokenFile = 4096

// commonFlags registers -server, -identity, -credential and -profile on fs,
// writing into o.
func commonFlags(fs *flag.FlagSet, o *client.Options) {
	fs.StringVar(&o.Server, "server", "", "server URL (default: AGENTBUS2_SERVER, then the profile's server)")
	fs.StringVar(&o.Identity, "identity", "", "identity (default: AGENTBUS2_IDENTITY, then the profile's identity, then the profile's only credential)")
	fs.StringVar(&o.Credential, "credential", "", "private key file that signs, instead of the credential store (default: AGENTBUS2_CREDENTIAL)")
	fs.StringVar(&o.Profile, "profile", "", "profile name (default: AGENTBUS2_PROFILE, then .local/agentbus2.json, then default)")
}

// applyEnv fills an empty Server from AGENTBUS2_SERVER and an empty Credential
// from AGENTBUS2_CREDENTIAL, then makes Credential absolute so a later change
// of the working directory cannot move it.
func applyEnv(o *client.Options, environ []string) {
	if o.Server == "" {
		o.Server, _ = profile.Getenv(environ, "AGENTBUS2_SERVER")
	}
	if o.Credential == "" {
		o.Credential, _ = profile.Getenv(environ, "AGENTBUS2_CREDENTIAL")
	}
	if o.Credential != "" {
		if abs, err := filepath.Abs(o.Credential); err == nil {
			o.Credential = abs
		}
	}
}

// enrollmentToken is the token from, in order, the -enrollment-token flag, the
// -enrollment-token-file file and AGENTBUS2_ENROLLMENT_TOKEN; both flags at
// once is an error. The file is trimmed of surrounding white space and must be
// a regular file, non-empty and at most 4 KiB. No error carries the token.
// open note: mcp-only-client 13
func enrollmentToken(flagValue, file string, environ []string) (string, error) {
	if flagValue != "" && file != "" {
		return "", errors.New("-enrollment-token and -enrollment-token-file are exclusive")
	}
	if flagValue != "" {
		return flagValue, nil
	}
	if file != "" {
		return readTokenFile(file)
	}
	v, _ := profile.Getenv(environ, "AGENTBUS2_ENROLLMENT_TOKEN")
	return v, nil
}

func readTokenFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("enrollment token file %s: %w", path, errors.Unwrap(err))
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("enrollment token file %s: %w", path, err)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("enrollment token file %s is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxTokenFile+1))
	if err != nil {
		return "", fmt.Errorf("enrollment token file %s: %w", path, err)
	}
	if len(b) > maxTokenFile {
		return "", fmt.Errorf("enrollment token file %s is larger than %d bytes", path, maxTokenFile)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("enrollment token file %s is empty", path)
	}
	return tok, nil
}
