package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/procs"
)

// newFlagSet returns the flag set of one command: ContinueOnError, output to
// stderr (ported from v1 main.go).
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("agentbus2 "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseExit maps a flag parse error to an exit code: -h is success, anything
// else is a usage error.
func parseExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

// fail prints err as "agentbus2: <code>: <message>" for a *bus.Error and
// "agentbus2: <err>" otherwise, and returns exit code 1.
func fail(stderr io.Writer, err error) int {
	var be *bus.Error
	if errors.As(err, &be) {
		_, _ = fmt.Fprintf(stderr, "agentbus2: %s: %s\n", be.Code, be.Message)
	} else {
		_, _ = fmt.Fprintf(stderr, "agentbus2: %v\n", err)
	}
	return 1
}

// printJSON writes v as one indented document and a newline.
func printJSON(stdout io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(b, '\n'))
	return err
}

// testClientOptions is nil in production. open applies it last, so a test of a
// Run<Name> command can pin PollInterval, Now and Config on the Conn the
// command builds from environ and flags, which it could not reach otherwise.
var testClientOptions func(*client.Options)

// open builds the Conn of a command. A profile, identity or credential
// resolution error is a usage error: it prints through fail and returns exit
// 2. Any other error returns exit 1. Nothing is dialed here in server mode.
func open(ctx context.Context, o client.Options, stderr io.Writer) (*client.Conn, int) {
	if testClientOptions != nil {
		testClientOptions(&o)
	}
	if o.Logger == nil {
		// Warnings (a SQLite file others can read or write) go to stderr, never
		// stdout: results are JSON there.
		o.Logger = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	c, err := client.Open(ctx, o)
	if err != nil {
		fail(stderr, err)
		var re *client.ResolveError
		if errors.As(err, &re) {
			return nil, 2
		}
		return nil, 1
	}
	return c, 0
}

// processTable is the process table that watch, wait and the hooks locate
// their session through. Tests swap it for a fake.
var processTable procs.Table = procs.System

// selfPID is the pid the harness walk starts from. Tests pin it.
var selfPID = os.Getpid
