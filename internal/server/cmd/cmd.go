package cmd

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/ericfitz/agentbus2/internal/server"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/postgres"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// Run is `agentbus2 server`. Interim flags until Decision 85's configuration
// file (Milestone 5; Decision (Eric, 2026-10-21): Milestone 4 item 3). Exit codes: 0 clean
// shutdown or -h, 1 runtime error, 2 flag error. Ported from v1 main.go
// (run's subcommand dispatch, flag.ContinueOnError, parseExit).
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return run(ctx, args, stdout, stderr, Serve)
}

// run is Run with the serving function injected, so a test can look at the
// Config the flags produce without binding a port.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, serve func(context.Context, Config) error) int {
	fs := flag.NewFlagSet("agentbus2 server", flag.ContinueOnError)
	var usage bytes.Buffer
	fs.SetOutput(&usage)
	listen := fs.String("listen", "", "address to listen on, host:port (required; port 0 picks a free port)")
	sqlitePath := fs.String("sqlite", "", "SQLite database file (exactly one of -sqlite or -postgres)")
	pgDSN := fs.String("postgres", "", "Postgres DSN (exactly one of -sqlite or -postgres)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.Copy(stdout, &usage)
			return 0
		}
		_, _ = io.Copy(stderr, &usage)
		return 2
	}
	bad := false
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "agentbus2 server: unexpected argument %q\n", fs.Arg(0))
		bad = true
	}
	if *listen == "" {
		_, _ = fmt.Fprintln(stderr, "agentbus2 server: -listen is required, for example 127.0.0.1:0")
		bad = true
	}
	if (*sqlitePath == "") == (*pgDSN == "") {
		_, _ = fmt.Fprintln(stderr, "agentbus2 server: exactly one of -sqlite or -postgres is required")
		bad = true
	}
	if bad {
		return 2
	}

	var (
		s      store.Store
		err    error
		engine string
	)
	if *sqlitePath != "" {
		engine = "sqlite"
		s, err = sqlite.Open(ctx, *sqlitePath, sqlite.Options{})
	} else {
		engine = "postgres"
		s, err = postgres.Open(ctx, *pgDSN, postgres.Options{})
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agentbus2 server: opening the %s store: %v\n", engine, err)
		return 1
	}
	defer func() { _ = s.Close() }()

	err = serve(ctx, Config{
		Listen: *listen,
		Store:  s,
		Auth:   server.RefuseAll{},
		Logger: slog.New(slog.NewTextHandler(stderr, nil)),
		Ready: func(a net.Addr) {
			_, _ = fmt.Fprintf(stdout, "agentbus2 server listening on %s (%s)\n", a, engine)
		},
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agentbus2 server: %v\n", err)
		return 1
	}
	return 0
}
