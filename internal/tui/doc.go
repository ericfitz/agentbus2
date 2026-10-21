// Package tui is the agentbus2 terminal dashboard: a chat-client view of the
// bus that a human reads and posts to. It sees the bus only through the
// bus.Bus interfaces, so it runs unchanged in local mode and in server mode,
// and it has no inbox view (ADR 0006).
//
// Ported from v1 internal/tui (Decision 60, Milestone 8).
package tui

// The blank imports pin the Bubble Tea module graph in go.mod and go.sum from
// Milestone 8 Task 1, so that the feature tasks that run in parallel after it
// never edit those two files. Each is dropped when a real import replaces it.
import (
	_ "github.com/charmbracelet/bubbles/cursor"
	_ "github.com/charmbracelet/bubbles/textarea"
	_ "github.com/charmbracelet/bubbles/textinput"
	_ "github.com/charmbracelet/bubbles/viewport"
	_ "github.com/charmbracelet/bubbletea"
)
