package mcpcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/mcpserver"
)

// runRegister is `agentbus2-mcponly register [-server URL] [-identity I]
// [-credential FILE] [-profile P] [-enrollment-token T |
// -enrollment-token-file FILE] [-algorithm EdDSA|ES256]`: client.RegisterIdentity
// with this binary's flags. A second run registers the same key again and
// prints `(existing key)`; with -credential nothing is written locally, so a
// failed run leaves nothing; without it a failed run leaves the pending key
// file and the next run reuses it; overlapping runs race on the pending file
// (the last write wins). The token is never printed.
func runRegister(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("register", stderr)
	o := client.Options{Environ: environ}
	commonFlags(fs, &o)
	var token, tokenFile, algName string
	fs.StringVar(&token, "enrollment-token", "", "enrollment token, for a server in enrollment mode (default: AGENTBUS2_ENROLLMENT_TOKEN)")
	fs.StringVar(&tokenFile, "enrollment-token-file", "", "file holding the enrollment token")
	fs.StringVar(&algName, "algorithm", string(keys.EdDSA), "key algorithm: EdDSA or ES256")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "%s register: unexpected argument %q\n", Binary, fs.Arg(0))
		return 2
	}
	applyEnv(&o, environ)
	if tokenFile != "" {
		if abs, err := filepath.Abs(tokenFile); err == nil {
			tokenFile = abs
		}
	}
	tok, err := enrollmentToken(token, tokenFile, environ)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s register: %v\n", Binary, err)
		return 2
	}
	algExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "algorithm" {
			algExplicit = true
		}
	})
	// Decision 16: a key for the OS login name could never serve, so refuse it
	// before anything is created. (The full binary's register has no such guard.)
	if err := mcpserver.IdentityGuard(o, osUserName(), Binary+" register"); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	out, err := client.RegisterIdentity(ctx, client.RegisterRequest{Options: o, EnrollmentToken: tok, Algorithm: keys.Algorithm(algName), AlgorithmExplicit: algExplicit})
	if err != nil {
		var re *client.ResolveError
		if errors.As(err, &re) {
			_, _ = fmt.Fprintf(stderr, "%s register: %v\n", Binary, err)
			return 2
		}
		var pc *client.PendingConflictError
		if errors.As(err, &pc) {
			code := fail(stderr, pc.Err)
			_, _ = fmt.Fprintf(stderr, "%s register: %s\n", Binary, pc.Hint())
			return code
		}
		return fail(stderr, err)
	}
	state := "existing key"
	if out.Created {
		state = "created"
	}
	_, _ = fmt.Fprintf(stdout, "registered %s at %s (%s)\n", out.Identity, out.Server, state)
	return 0
}
