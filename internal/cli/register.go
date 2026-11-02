package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/client"
)

// RunRegister is `agentbus2 register [-profile P] [-identity I] [-server URL]
// [-enrollment-token T] [-algorithm EdDSA|ES256]` (Milestone 6 choice 14;
// Decision (Eric, 2026-10-21), item 6): generate a key, write it to the
// pending file (credential.Store.PendingPath), register its public half and,
// once the server answered 201, save that key as the credential and remove the
// pending file if it still holds it. A
// failed run leaves the pending file and the next run reuses that key. A run
// after success reuses the saved key and registers it again, which the server
// answers idempotently (Milestone 5 choice 8). Overlapping runs for one
// identity race on the pending file: the last write wins and the loser's key
// is no longer the pending one.
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
	algExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "algorithm" {
			algExplicit = true
		}
	})
	o.Server = server
	out, err := client.RegisterIdentity(ctx, client.RegisterRequest{Options: o, EnrollmentToken: token, Algorithm: alg, AlgorithmExplicit: algExplicit})
	if err != nil {
		var re *client.ResolveError
		if errors.As(err, &re) {
			_, _ = fmt.Fprintf(stderr, "agentbus2 register: %v\n", err)
			return 2
		}
		var pc *client.PendingConflictError
		if errors.As(err, &pc) {
			code := fail(stderr, pc.Err)
			_, _ = fmt.Fprintf(stderr, "agentbus2 register: %s\n", pc.Hint())
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
