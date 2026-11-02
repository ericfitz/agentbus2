// Package mcpcli holds the commands of agentbus2-mcponly: serve (the stdio MCP
// server), register and version. It is vetted, linted and unit-tested like
// internal/cli, and cmd/agentbus2-mcponly/main.go is the signal context and
// os.Exit only.
//
// The package imports client, credential, auth/keys, mcpserver, profile,
// version, osuser and bus, and never internal/cli, internal/gen or
// internal/store: scripts/m10-exit-checks.sh checks that, and the transitive
// set of the binary, so it links no core, server or Store engine.
//
// open note: mcp-only-client 2
package mcpcli
