// Stub created by Milestone 8 Task 3 from the call sites in v1 906dec8 model.go, view.go and compose.go; Task 4 replaces this file.
//
// The pure helpers carry v1's real bodies because the rail and setChannels
// need them; the rest are stubs. Signatures and the v1 call site each came from:
//
//	tagPanePrefix, tagReadOnlyToast, isTagPane, tagPaneSet, splitTags   real (model.go:405, view.go:324, :370, :889; splitTags is v1 tags.go only)
//	tagPaneSources() []string                 model.go:1163 showSelected
//	tagPaneMsgs(ch string) []bus.Message      thread.go:17 paneMsgs
//	tagPrompt() tea.Cmd                       model.go:578 updateNormal
//	unfollowTagPane() tea.Cmd                 model.go:597 updateNormal, compose.go:119 toggleSubscribe

package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus2/internal/bus"
)

// tagPanePrefix names a tag set's rail entry: "tags:" plus the sorted set of
// patterns joined by commas (tags:env:*,failed). A tag may itself carry one
// colon, which is why the prefix is matched only at the start and TrimPrefix
// leaves the set intact. The rail entry is synthetic and never reaches the
// bus.
const tagPanePrefix = "tags:"

const tagReadOnlyToast = "tag views are read-only; select a channel to post"

func isTagPane(ch string) bool { return strings.HasPrefix(ch, tagPanePrefix) }

func tagPaneSet(ch string) []string { return strings.Split(strings.TrimPrefix(ch, tagPanePrefix), ",") }

// splitTags splits a comma-separated tag list, trimming space around each
// tag so typing "a, b" (the natural style) is accepted.
func splitTags(v string) []string { //nolint:unused // stub; Task 4 uses it
	parts := strings.Split(v, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

func (m *Model) tagPaneSources() []string { return nil }

func (m *Model) tagPaneMsgs(string) []bus.Message { return nil }

func (m *Model) tagPrompt() tea.Cmd { return nil }

func (m *Model) unfollowTagPane() tea.Cmd { return nil }
