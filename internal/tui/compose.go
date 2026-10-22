package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/ericfitz/agentbus2/internal/bus"
)

// promptState is the one-line inline prompt used by "c" (create channel).
// While active it replaces the compose line and takes every key.
type promptState struct {
	active      bool
	label       string
	input       textinput.Model
	accept      func(m *Model, value string) tea.Cmd
	acceptEmpty bool // enter on an empty line still calls accept (the prompt explains itself); else it just closes
}

const createChannelUsage = "usage: <name> [memory|tasks]"

// submitCompose sends the compose text to the selected channel. On a memory
// channel the server turns the send into a memory. The send carries the
// session id: that is what makes the server stamp the message with the
// server-assigned display name (ADR 0003). The text is kept for ↑ recall, and
// a failed send leaves it there via the toast's hint: the user presses ↑ then
// enter.
func (m *Model) submitCompose() tea.Cmd {
	text := strings.TrimRight(m.compose.Value(), "\n ")
	ch := m.selName()
	if strings.TrimSpace(text) == "" || ch == "" {
		return nil
	}
	in := bus.SendInput{Channel: ch, Content: text, Session: m.c.sessionID()}
	if m.replyTo != nil {
		seq := m.replyTo.Seq
		in.ReplyTo = &seq
	}
	m.lastSent = text
	m.replyTo = nil
	m.compose.Reset()
	m.fitCompose()
	c := m.c
	return func() tea.Msg {
		// The session id is read at send time, not when the key was pressed:
		// a resume after a lapse (resumeLapsed) may have replaced it.
		in.Session = c.sessionID()
		_, err := c.b.Send(c.ctx, in)
		return sentMsg{channel: ch, err: err}
	}
}

func (m *Model) openPrompt(label string, accept func(*Model, string) tea.Cmd) tea.Cmd {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 128
	in.SetStyles(promptStyles())
	m.prompt = promptState{active: true, label: label, input: in, accept: accept}
	return m.prompt.input.Focus() // focus the copy the model keeps, not the local
}

// promptStyles is a text input's styles as Bubbles v1 drew them: no prompt or
// text color, placeholder and suggestion in 240, and a static cursor in the
// terminal's own color. Static, because textinput's Blink mode reschedules
// itself forever (see composeStyles in model.go), which never terminates
// against the fixture's synchronous cmd runner.
func promptStyles() textinput.Styles {
	gray := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	state := textinput.StyleState{Placeholder: gray, Suggestion: gray}
	return textinput.Styles{
		Focused: state,
		Blurred: state,
		Cursor:  textinput.CursorStyle{Color: lipgloss.NoColor{}, Shape: tea.CursorBlock},
	}
}

func (m *Model) updatePrompt(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "esc":
		m.prompt = promptState{}
		return nil
	case "enter":
		v := strings.TrimSpace(m.prompt.input.Value())
		accept, acceptEmpty := m.prompt.accept, m.prompt.acceptEmpty
		m.prompt = promptState{}
		if v == "" && !acceptEmpty {
			return nil
		}
		return accept(m, v)
	}
	var cmd tea.Cmd
	m.prompt.input, cmd = m.prompt.input.Update(msg)
	return cmd
}

// createChannelPrompt asks "<name> [memory|tasks]": the kind is memory,
// tasks, or omitted for ordinary (Decision 90); e.g. "notes memory". Anything
// after the kind is refused here, before the bus is called; an unknown kind
// is the bus's to refuse.
func (m *Model) createChannelPrompt() tea.Cmd {
	cmd := m.openPrompt("new channel <name> [memory|tasks]", func(m *Model, v string) tea.Cmd {
		fields := strings.Fields(v)
		if len(fields) == 0 || len(fields) > 2 {
			return m.showToast(createChannelUsage)
		}
		kind := bus.KindOrdinary
		if len(fields) == 2 {
			kind = bus.Kind(fields[1])
		}
		name := fields[0]
		c := m.c
		// subscribedMsg's success path refreshes the status (and so the rail).
		return func() tea.Msg {
			ch, err := c.b.CreateChannel(c.ctx, bus.CreateChannelInput{Name: name, Kind: kind})
			if err != nil {
				return sentMsg{channel: name, err: err}
			}
			return subscribedMsg{channel: name, err: c.subscribe(ch.Channel, "oldest")}
		}
	})
	m.prompt.acceptEmpty = true
	return cmd
}

// toggleSubscribe unsubscribes the identity from the selected channel (its
// messages stop arriving; the rail still lists it) or subscribes it again
// from now. Subscriptions belong to the identity (Decision 37), so this is
// seen by every session of it.
func (m *Model) toggleSubscribe() tea.Cmd {
	ch := m.selected()
	if ch == nil {
		return nil
	}
	if isTagPane(ch.Name) {
		return m.unfollowTagPane()
	}
	c := m.c
	name := ch.Name
	if c.isSubscribed(name) {
		ctx, cancel := c.callCtx()
		defer cancel()
		if err := c.b.Unsubscribe(ctx, bus.UnsubscribeInput{Channel: name}); err != nil {
			return m.showToast("unsubscribe: " + errText(err))
		}
		c.forget(name)
		return m.showToast("unsubscribed from " + name + " (s to resubscribe)")
	}
	if err := c.subscribe(*ch, "now"); err != nil {
		return m.showToast("subscribe: " + errText(err))
	}
	return nil
}

// updateConfirmChannel answers the delete-channel prompt: y deletes the
// selected channel, any other key cancels. A refusal (an ACL, the general
// channel) is a toast.
func (m *Model) updateConfirmChannel(msg tea.Msg) tea.Cmd {
	k := keyString(msg)
	if k == "" {
		return nil
	}
	m.mode = modeNormal
	ch := m.selected()
	if k != "y" || ch == nil {
		return nil
	}
	c := m.c
	name := ch.Name
	ctx, cancel := c.callCtx()
	defer cancel()
	if err := c.b.DeleteChannel(ctx, bus.DeleteChannelInput{Name: name}); err != nil {
		return m.showToast("delete: " + errText(err))
	}
	c.forget(name)
	delete(m.kinds, name)
	m.channels = slices.DeleteFunc(m.channels, func(x bus.Channel) bool { return x.Name == name })
	m.sel = min(m.sel, len(m.channels)-1)
	return m.showToast("deleted channel " + name)
}

// confirmChannelLine is the compose-row text while modeConfirmChannel is up.
func (m Model) confirmChannelLine() string {
	ch := m.selected()
	if ch == nil {
		return ""
	}
	noun := "messages"
	switch ch.Kind {
	case bus.KindMemory:
		noun = "memories"
	case bus.KindTasks:
		noun = "tasks"
	}
	return m.theme.Style(m.theme.Error).Render(fmt.Sprintf("delete channel %s and PERMANENTLY destroy its %s? no undo · y yes  any other key no", ch.Name, noun))
}
