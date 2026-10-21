// Package mcpserver serves the bus as MCP tools over stdio (Decisions 3, 47,
// 66, 78, 79, 83), ported from agentbus v1 internal/mcpserver/server.go.
//
// The server opens its Bus lazily through internal/client, so a profile or
// identity that cannot be resolved does not stop it from starting: every tool
// then answers the problem as a validation error. register opens a session
// and records it in a state file (internal/sessionstate) that survives the
// server's exit; only close_session removes one, and a restarted server under
// the same harness resumes the primary one.
//
// It imports internal/client, internal/bus, internal/sessionstate,
// internal/procs and internal/profile; never internal/gen or internal/store.
package mcpserver
