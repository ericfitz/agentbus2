// Package cli holds the agentbus2 commands. Every command is
// Run<Name>(ctx, args, environ, stdin, stdout, stderr) int: flag.ContinueOnError,
// -h exits 0, a flag error exits 2, a bus.Error prints as
// "agentbus2: <code>: <message>" and exits 1 (2 for watch and wait, whose table
// is spec 3.5), and a result is one JSON document on stdout. The environment
// comes from environ only, so a test never reads the developer's directories.
//
// The package imports internal/client and internal/bus, never internal/gen or
// internal/store: a command works on a bus.Bus.
package cli
