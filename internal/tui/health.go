// Stub created by Milestone 8 Task 3 from the call sites in v1 906dec8 model.go, view.go and compose.go; Task 7 replaces this file.
//
// Signatures and the v1 call site each came from:
//
//	type healthState struct{ lastQueryAt, lastQueryOK, scroll }   model.go:122 Model.health
//	recordQuery(ok bool)                      model.go:336 update (searchMsg); real body, Task 7 keeps it
//	openHealth() tea.Cmd                      model.go:611 updateNormal
//	usageCmd() tea.Cmd                        new in v2 (Task 7's openHealth batches it with statusCmd)
//	updateHealth(msg tea.Msg) tea.Cmd         model.go:365 update
//	onConfigEdited(msg configEditedMsg) tea.Cmd   model.go:351 update
//	healthLines() []string                    v1 health.go updateHealth, viewHealth
//	viewHealth() string                       view.go:264 View

package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus2/internal/version"
)

type healthState struct {
	lastQueryAt time.Time
	lastQueryOK bool
	scroll      int
}

func (m *Model) recordQuery(ok bool) {
	m.health.lastQueryAt = time.Now()
	m.health.lastQueryOK = ok
}

func (m *Model) openHealth() tea.Cmd {
	m.mode = modeHealth
	m.health.scroll = 0
	return m.statusCmd()
}

// usageCmd fetches storage use for the admin block of the overlay: none in
// the stub.
func (m *Model) usageCmd() tea.Cmd { return nil } //nolint:unused // stub; Task 7 uses it

func (m *Model) updateHealth(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "esc", "h":
		m.mode = modeNormal
	}
	return nil
}

func (m *Model) onConfigEdited(msg configEditedMsg) tea.Cmd {
	if msg.err != nil {
		return m.showToast("config: " + errText(msg.err))
	}
	return nil
}

func (m Model) healthLines() []string {
	lines := []string{"version agentbus2 v" + version.Version}
	// Task 7 keeps the warnings block (Host.Warnings) in its fuller overlay;
	// the stub carries it so the shared-identity line is shown from the start.
	return append(lines, m.host.Warnings...)
}

func (m Model) viewHealth() string {
	return m.overlay("health", m.theme.Health, strings.Join(m.healthLines(), "\n"), m.hints("esc", "close"))
}
