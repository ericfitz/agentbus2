package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus2/internal/bus"
)

// The sender icon is drawn in the sender's color (user for the TUI's own
// identity, agent for everyone else) in every icon mode, and styling it
// never changes a width: the gear's CSI escapes stay invisible to lipgloss.
func TestSenderIconColoredInEveryIconMode(t *testing.T) {
	t.Cleanup(func() { setIcons(emojiIcons) })
	for _, mode := range []string{"emoji", "nerdfont", "custom"} {
		s := Settings{Icons: mode}
		if mode == "custom" {
			s.IconMap = map[string]string{"agent": "A", "user": "U"}
		}
		LoadIcons(s, io.Discard)
		f := newFixture(t)
		th := f.m.theme
		agent := th.Style(th.Agent).Render(iconAgent)
		user := th.Style(th.User).Render(iconUser)
		if agent == iconAgent || user == iconUser {
			t.Fatalf("%s: theme renders no color", mode)
		}
		other := bus.Message{Seq: 1, Channel: "dev", Sender: "sam", Display: "sam"}
		own := bus.Message{Seq: 2, Channel: "dev", Sender: f.c.identity, Display: f.c.displayName()}
		if got := f.m.agentLabel(other); !strings.HasPrefix(got, agent) {
			t.Errorf("%s: agent header icon uncolored: %q", mode, got)
		}
		if got := f.m.agentLabel(own); !strings.HasPrefix(got, user) {
			t.Errorf("%s: own header icon uncolored: %q", mode, got)
		}
		if w, want := lipgloss.Width(agent), lipgloss.Width(ansi.Strip(iconAgent)); w != want {
			t.Errorf("%s: styled agent icon width %d, want %d", mode, w, want)
		}
		f.run(f.m.statusCmd())
		rail := f.m.renderRails()
		if !strings.Contains(rail, agent) || !strings.Contains(rail, user) {
			t.Errorf("%s: sessions rail icons uncolored: %q", mode, rail)
		}
		msg := bus.Message{Seq: 1, Channel: "dev", Sender: "sam", CreatedAt: time.Now().UnixMilli()}
		colored := lipgloss.Width(f.m.header(msg, "dev", "", lipgloss.NewStyle(), 200))
		f.m.theme = noColorTheme(th)
		head := f.m.header(msg, "dev", "", lipgloss.NewStyle(), 200)
		f.m.theme = th
		if plain := lipgloss.Width(head); colored != plain {
			t.Errorf("%s: header width %d colored vs %d plain", mode, colored, plain)
		}
	}
}

// On the selected stream row, the colored sender icon ends with a reset;
// Highlight must re-apply the selection background right after it (and
// after every other inner reset), or the rest of the line loses it.
func TestSelectedRowReappliesBackgroundAfterSenderIcon(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev", "hello from sam")
	f.receive(t)
	f.key("shift+tab") // compose -> stream directly: cursor lands on the last message
	th := f.m.theme
	pre, _, _ := strings.Cut(lipgloss.NewStyle().Background(th.Sel).Render("\x00"), "\x00")
	if pre == "" {
		t.Fatal("selection background renders no escape")
	}
	icon := th.Style(th.Agent).Render(iconAgent)
	first := strings.SplitN(f.m.renderStream(), "\n", 2)[0]
	if !strings.Contains(first, icon+pre) {
		t.Fatalf("selection background not re-applied after the sender icon: %q", first)
	}
	// Lip Gloss v2 closes the padding and then the whole line, each with its
	// own reset: those trailing resets end the row and re-apply nothing.
	body := first
	for strings.HasSuffix(body, ansi.ResetStyle) {
		body = strings.TrimSuffix(body, ansi.ResetStyle)
	}
	if n, m := strings.Count(body, ansi.ResetStyle), strings.Count(body, ansi.ResetStyle+pre); n != m {
		t.Fatalf("%d of %d inner resets drop the selection background: %q", n-m, n, first)
	}
}

// A message's sender icon follows its sender_kind: a human sender is drawn
// with the user icon in the user color, an agent with the agent icon in the
// agent color; the TUI's own messages keep the user color.
func TestHumanSenderLabel(t *testing.T) {
	f := newFixture(t)
	th := f.m.theme
	agent := th.Style(th.Agent).Render(iconAgent)
	user := th.Style(th.User).Render(iconUser)
	human := bus.Message{Seq: 1, Channel: "dev", Sender: "eric@corp.example", Display: "eric@corp.example", SenderKind: bus.IdentityHuman}
	bot := bus.Message{Seq: 2, Channel: "dev", Sender: "sam", Display: "sam", SenderKind: bus.IdentityAgent}
	if got := f.m.agentLabel(human); !strings.HasPrefix(got, user) {
		t.Errorf("human sender label %q lacks the user icon %q", got, user)
	}
	if got := f.m.agentLabel(bot); !strings.HasPrefix(got, agent) {
		t.Errorf("agent sender label %q lacks the agent icon %q", got, agent)
	}
	if got := f.m.messageIcon(human); got != user {
		t.Errorf("messageIcon(human) = %q, want %q", got, user)
	}
}
