// Package tui is the agentbus2 terminal dashboard: a chat-client view of the
// bus that a human reads and posts to. It sees the bus only through the
// bus.Bus interfaces, so it runs unchanged in local mode and in server mode,
// and it has no view of any identity's private messages (ADR 0006).
//
// Ported from v1 internal/tui (Decision 60, Milestone 8).
package tui
