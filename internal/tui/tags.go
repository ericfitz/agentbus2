// Ported from v1 906dec8 internal/tui/tags.go (Decision 90 kinds, SubscribeTags{Patterns}).

package tui

import (
	"cmp"
	"slices"
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

// tagPaneSources is the channels a tag pane draws from: every ordinary and
// memory channel in the rail; never task lists or other tag panes.
func (m *Model) tagPaneSources() []string {
	var out []string
	for _, c := range m.channels {
		if c.Kind == bus.KindOrdinary || c.Kind == bus.KindMemory {
			out = append(out, c.Name)
		}
	}
	return out
}

// matchTag is the tag-pattern rule of the server (core/rules.MatchTag): an
// exact tag, or a prefix followed by one * that matches every tag starting
// with the prefix (env* matches env, environment and env:prod; env:* only
// env:<value>). It is a copy so the TUI imports no core package;
// TestMatchTagAgreesWithTheCore keeps the two in step.
func matchTag(pattern, tag string) bool {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return tag >= p && tag <= p+"~"
	}
	return tag == pattern
}

// matchTags reports whether every pattern matches at least one of tags.
func matchTags(patterns, tags []string) bool {
	return !slices.ContainsFunc(patterns, func(p string) bool {
		return !slices.ContainsFunc(tags, func(t string) bool { return matchTag(p, t) })
	})
}

// tagPaneMsgs is the loaded messages of every tagPaneSources channel whose
// tags satisfy all of the pane's patterns (env:* matches env:prod),
// ascending by seq.
func (m *Model) tagPaneMsgs(ch string) []bus.Message {
	set := tagPaneSet(ch)
	var out []bus.Message
	for _, c := range m.tagPaneSources() {
		for _, x := range m.msgs[c] {
			if matchTags(set, x.Tags) {
				out = append(out, x)
			}
		}
	}
	slices.SortFunc(out, func(a, b bus.Message) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

// splitTags splits a comma-separated tag list, trimming space around each
// tag so typing "a, b" (the natural style) does not fail the server's
// pattern check, which rejects a tag carrying its own leading or trailing
// space.
func splitTags(v string) []string {
	parts := strings.Split(v, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// tagPrompt asks for a comma-separated set of tags or prefix patterns (env:*)
// and subscribes to it; the status refresh that follows adds it to the rail
// under its normalized name.
func (m *Model) tagPrompt() tea.Cmd {
	return m.openPrompt("tags <tag|prefix*>[,...]", func(m *Model, v string) tea.Cmd {
		c := m.c
		patterns := splitTags(v)
		return func() tea.Msg {
			err := c.b.SubscribeTags(c.ctx, bus.TagSubscribeInput{Patterns: patterns})
			return subscribedMsg{channel: tagPanePrefix + v, err: err}
		}
	})
}

// unfollowTagPane (s or d on a tag set's rail row) unsubscribes the selected
// set with no confirmation (re-following is one t away) and moves the
// selection to the next tag set, else the previous one, else the last
// channel. Tag panes sit at the end of m.channels, so once index i is
// deleted the row now at i is the next set and the row at i-1 is whichever
// of the other two exists. The status refresh that follows re-syncs the rail
// by name.
func (m *Model) unfollowTagPane() tea.Cmd {
	ch := m.selected()
	if ch == nil || !isTagPane(ch.Name) {
		return nil
	}
	name := ch.Name // ch points into m.channels, which is edited below
	if err := m.c.b.UnsubscribeTags(m.c.ctx, bus.TagSubscribeInput{Patterns: tagPaneSet(name)}); err != nil {
		return m.showToast("unsubscribe: " + errText(err))
	}
	m.channels = slices.Delete(m.channels, m.sel, m.sel+1)
	return tea.Batch(
		m.selectChannel(min(m.sel, len(m.channels)-1)),
		m.showToast("unfollowed tags "+strings.TrimPrefix(name, tagPanePrefix)),
		m.statusCmd(),
	)
}
