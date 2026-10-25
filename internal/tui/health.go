// Ported from agentbus v1 internal/tui/health.go at 906dec8 (Milestone 8
// Task 7). v1 read its embedding and storage fields from its own config and
// status; v2 shows what a client can know, and the administrator block
// (notices, embeddings, storage) appears only for an admin-flagged identity
// (Decision 73).

package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/ericfitz/agentbus2/internal/bus"
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

// openHealth shows the overlay and refreshes what it reads: the status round
// and, for an administrator, the storage use.
func (m *Model) openHealth() tea.Cmd {
	m.mode = modeHealth
	m.health.scroll = 0
	return tea.Batch(m.statusCmd(), m.usageCmd(), m.healthCmd())
}

// healthCmd asks the server for its health document. A failure is carried in
// the message and shown on the overlay's server line, never as a toast.
func (m *Model) healthCmd() tea.Cmd {
	c := m.c
	return func() tea.Msg {
		h, err := c.b.Health(c.ctx)
		if err != nil {
			return healthMsg{err: err}
		}
		return healthMsg{h: &h}
	}
}

// usageCmd fetches storage use for the admin block of the overlay: nil unless
// the identity is an administrator, so a non-admin never calls Usage. A
// failure (a forbidden among them) is carried in the message and ignored by
// the handler: it is never a toast, and the storage line is simply absent.
func (m *Model) usageCmd() tea.Cmd {
	if !m.admin {
		return nil
	}
	c := m.c
	return func() tea.Msg {
		u, err := c.b.Usage(c.ctx)
		if err != nil {
			return usageMsg{err: err}
		}
		return usageMsg{usage: &u}
	}
}

func (m *Model) updateHealth(msg tea.Msg) tea.Cmd {
	switch keyString(msg) {
	case "esc", "h":
		m.mode = modeNormal
	case "?":
		return m.openHelp()
	case "up":
		m.health.scroll = max(m.health.scroll-1, 0)
	case "down":
		m.health.scroll = min(m.health.scroll+1, max(len(m.healthLines())-1, 0))
	case "o":
		// A GUI $VISUAL runs off the terminal: the command waits for it in
		// the background, the TUI stays live, and the config check toasts
		// when the editor exits (with code --wait, when the tab closes).
		path := m.host.ConfigPath
		if cmd, ok := backgroundEditor(path); ok {
			return func() tea.Msg { return configEditedMsg{err: cmd.Run()} }
		}
		return tea.ExecProcess(editorCommand(path), func(err error) tea.Msg { return configEditedMsg{err: err} })
	}
	return nil
}

// onConfigEdited re-validates the config file after the editor closes,
// through Host.ReloadConfig (the model never imports internal/profile). The
// running bus keeps its old settings; the toast says so.
func (m *Model) onConfigEdited(msg configEditedMsg) tea.Cmd {
	if msg.err != nil {
		return m.showToast(editorErrText(msg.err))
	}
	if m.host.ReloadConfig != nil {
		if err := m.host.ReloadConfig(); err != nil {
			return m.showToast("config: " + errText(err))
		}
	}
	return m.showToast("config saved; restart agentbus2 tui to apply")
}

// tuiConfigView is the tui object as health shows it: lowercase keys, the
// values in effect (the model holds the loaded theme, not the Settings).
type tuiConfigView struct {
	Theme     string `json:"theme"`
	Icons     string `json:"icons"`
	RailWidth int    `json:"rail_width"`
}

// healthLines renders the health overlay's body as lines, shared by
// viewHealth and updateHealth (so scrolling down cannot go past the last
// line).
func (m Model) healthLines() []string {
	th := m.theme
	dim, ok, warn := th.Style(th.Dim), th.Style(th.Health), th.Style(th.Warn)
	var b strings.Builder
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(&b, format, a...) }
	p("%s %s\n", dim.Render("version"), "agentbus2 v"+version.Version)
	p("%s %s\n", dim.Render("mode"), m.host.Describe)
	p("%s %s · %s · session %s\n", dim.Render("identity"), m.c.identity, m.c.displayName(), m.c.sessionID())
	live := 0
	for _, id := range m.identities {
		live += len(id.Sessions)
	}
	var nCh, nMem, nTasks int
	for _, c := range m.channels {
		if isTagPane(c.Name) {
			continue
		}
		nCh++
		switch c.Kind {
		case bus.KindMemory:
			nMem++
		case bus.KindTasks:
			nTasks++
		}
	}
	p("%s %d live · %s %d (%d memory, %d tasks)\n", dim.Render("sessions"), live, dim.Render("channels"), nCh, nMem, nTasks)
	for _, id := range m.identities {
		for _, s := range id.Sessions {
			p("  %s %s\n", s.DisplayName, dim.Render(id.Name))
		}
	}
	recv := ok.Render("long-poll connected")
	if m.receiveErr != nil {
		recv = th.Style(th.Error).Render("err " + errText(m.receiveErr))
	}
	lastB := "none yet"
	if !m.lastBatchAt.IsZero() {
		lastB = m.lastBatchAt.Format("15:04:05")
	}
	p("%s %s · %s %s · %d gaps\n", dim.Render("receive"), recv, dim.Render("last batch"), lastB, m.gapCount)
	m.serverLine(p, dim, ok, warn)
	if m.admin {
		m.adminLines(p, dim, ok, warn)
	} else {
		p("%s no %s\n", dim.Render("admin"), dim.Render("(status and storage are administrator views)"))
	}
	if len(m.host.Warnings) > 0 {
		p("%s\n", dim.Render("warnings"))
		for _, w := range m.host.Warnings {
			p("  %s\n", warn.Render(w))
		}
	}
	p("\n%s %s\n", dim.Render("theme ·"), th.Name)
	for _, kv := range DefaultTheme().Colors() {
		p("  %s %s\n", kv[0], th.Sources[kv[0]])
	}
	p("%s %s\n", dim.Render("icons ·"), th.IconSet)
	p("\n%s %s\n", dim.Render("config ·"), m.host.ConfigPath)
	js, err := json.MarshalIndent(map[string]tuiConfigView{"tui": {Theme: th.Name, Icons: th.IconSet, RailWidth: m.host.RailWidth}}, "  ", "  ")
	if err != nil {
		b.WriteString(err.Error())
	} else {
		b.WriteString("  " + string(js) + "\n")
	}
	return strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
}

// serverLine is the server's own verdict from the health endpoint: its
// version (which may differ from the client's), engine, schema and embedding
// state.
func (m Model) serverLine(p func(string, ...any), dim, ok, warn lipgloss.Style) {
	h := m.healthDoc
	switch {
	case m.healthErr != nil:
		p("%s %s\n", dim.Render("server"), m.theme.Style(m.theme.Error).Render("unavailable · "+errText(m.healthErr)))
		return
	case h == nil:
		p("%s …\n", dim.Render("server"))
		return
	}
	status := ok.Render(string(h.Status))
	if h.Status != bus.HealthOK {
		status = warn.Render(string(h.Status))
	}
	p("%s %s · agentbus2 v%s", dim.Render("server"), status, h.Version)
	if h.Store != nil {
		p(" · %s schema %d", h.Store.Engine, h.Store.SchemaVersion)
	}
	if h.Embeddings != nil {
		p(" · embeddings %s", h.Embeddings.State)
	}
	p("\n")
}

// maintenanceLines is the administrator's view of this process's maintenance
// loop: the lock, the last tick and the failed jobs.
func (m Model) maintenanceLines(p func(string, ...any), dim, warn lipgloss.Style) {
	h := m.healthDoc
	if h == nil || h.Maintenance == nil {
		return
	}
	mt := h.Maintenance
	lock := strings.ReplaceAll(string(mt.Lock), "_", " ")
	last := "none yet"
	if mt.LastTickAt != 0 {
		last = time.UnixMilli(mt.LastTickAt).Format("15:04:05")
	}
	failed := 0
	for _, j := range mt.Jobs {
		if j.Error != "" {
			failed++
		}
	}
	p("%s %s · %s %s · %d jobs, %d failed\n", dim.Render("maintenance"), lock, dim.Render("last tick"), last, len(mt.Jobs), failed)
	for _, j := range mt.Jobs {
		if j.Error != "" {
			p("  %s\n", warn.Render(j.Name+": "+j.Error))
		}
	}
	if mt.LockError != "" {
		p("  %s\n", warn.Render("lock error: "+mt.LockError))
	}
}

// adminLines is the administrator block: capacity notices, the embedding
// pipeline and storage use.
func (m Model) adminLines(p func(string, ...any), dim, ok, warn lipgloss.Style) {
	if !m.statusOK {
		p("%s %s\n", dim.Render("status"), warn.Render("unavailable"))
	} else {
		st := m.status
		if len(st.Notices) == 0 {
			p("%s none\n", dim.Render("notice"))
		}
		for _, n := range st.Notices {
			p("%s %s\n", dim.Render("notice"), warn.Render(n.Message))
		}
		state := ok.Render("ok")
		switch {
		case st.EmbeddingError != "":
			state = warn.Render("failing")
		case m.search.semanticDown:
			state = warn.Render("unreachable")
		}
		p("%s %s · %s %d · %s %d\n", dim.Render("embeddings"), state, dim.Render("backlog"), st.EmbeddingBacklog, dim.Render("rejected"), st.EmbeddingRejected)
		// The last embedding pass's error, from whichever process ran it
		// (ADR 0018), wrapped so a long endpoint message stays readable.
		// ansi.Wrap, not Wordwrap: an error body without spaces (JSON, a
		// URL) is one long word and must still be broken.
		wrap := func(head, text string) {
			p("  %s\n", warn.Render(head))
			for _, l := range strings.Split(ansi.Wrap(text, max(m.width-12, 30), " "), "\n") {
				p("    %s\n", warn.Render(l))
			}
		}
		if st.EmbeddingError != "" {
			wrap("last failure "+time.UnixMilli(st.EmbeddingErrorAt).Format("15:04:05"), st.EmbeddingError)
		}
		if st.EmbeddingRejectedLast != "" {
			wrap(fmt.Sprintf("%d rejected; latest reason", st.EmbeddingRejected), st.EmbeddingRejectedLast)
		}
	}
	lastQ := "none yet"
	if !m.health.lastQueryAt.IsZero() {
		lastQ = m.health.lastQueryAt.Format("15:04:05") + " "
		if m.health.lastQueryOK {
			lastQ += ok.Render("ok")
		} else {
			lastQ += warn.Render("text only")
		}
	}
	p("%s %s\n", dim.Render("last query"), lastQ)
	if u, isUsage := m.usage.(*bus.Usage); isUsage && u != nil {
		pct := 0
		if u.BudgetBytes > 0 {
			pct = int(u.Bytes * 100 / u.BudgetBytes)
		}
		filled := max(min(pct/5, 20), 0)
		bar := strings.Repeat("█", filled) + dim.Render(strings.Repeat("█", 20-filled))
		p("%s %s of %s %s %d%%\n", dim.Render("storage"), fmtBytes(u.Bytes), fmtBytes(u.BudgetBytes), bar, pct)
	}
	m.maintenanceLines(p, dim, warn)
}

func (m Model) viewHealth() string {
	lines := m.healthLines()
	if m.health.scroll < len(lines) {
		lines = lines[m.health.scroll:]
	}
	return m.overlay("health", m.theme.Health, strings.Join(lines, "\n"), m.hints("o", "open config in $EDITOR", "↑↓", "scroll", "?", "help", "esc", "close"))
}

// editorCommand runs $VISUAL, else $EDITOR, else defaultEditor (vi; notepad
// on Windows), on path. A value that
// is itself an existing file is run as-is, so a bare path with spaces
// ("/Applications/Visual Studio Code.app/Contents/MacOS/Code") works;
// splitting it on whitespace used to break it at "/Applications/Visual".
// Anything else goes through the shell the way git runs GIT_EDITOR (cmd.exe
// on Windows, see editor_windows.go), so arguments and quoting work
// ("code --wait"). A blank or whitespace-only
// value falls back the same as unset.
func editorCommand(path string) *exec.Cmd {
	ed, _ := editorSetting()
	if st, err := os.Stat(ed); err == nil && !st.IsDir() {
		return exec.Command(ed, path)
	}
	return shellEditorCommand(ed, path)
}

// terminalEditors need the terminal, so a $VISUAL naming one still blocks.
// Lookups fold case and drop a trailing ".exe" (Windows file names).
// ponytail: fixed list; a terminal editor missing from it would run in the
// background without a terminal, so add names as they come up.
var terminalEditors = map[string]bool{
	"vi": true, "vim": true, "view": true, "nvim": true, "nano": true, "pico": true,
	"emacs": true, "micro": true, "hx": true, "helix": true, "kak": true,
	"joe": true, "ne": true, "mg": true, "ed": true,
}

// backgroundEditor is $VISUAL on path, to run without the terminal while
// the TUI stays live; ok is false when $VISUAL is unset or names a
// terminal editor, and the caller then blocks as before.
func backgroundEditor(path string) (cmd *exec.Cmd, ok bool) {
	ed := strings.TrimSpace(os.Getenv("VISUAL"))
	if ed == "" || terminalEditors[strings.TrimSuffix(strings.ToLower(filepath.Base(editorProgram(ed))), ".exe")] {
		return nil, false
	}
	return editorCommand(path), true
}

// editorProgram is the program an editor setting runs: the whole value when
// it is an existing file (a path with spaces), else its first shell word
// with surrounding quotes removed.
func editorProgram(ed string) string {
	if st, err := os.Stat(ed); err == nil && !st.IsDir() {
		return ed
	}
	if q := ed[0]; q == '\'' || q == '"' {
		if end := strings.IndexByte(ed[1:], q); end >= 0 {
			return ed[1 : end+1]
		}
	}
	prog, _, _ := strings.Cut(ed, " ")
	return prog
}

// editorSetting is the editor editorCommand runs and where it came from
// ("$VISUAL", "$EDITOR", or "default").
func editorSetting() (ed, source string) {
	if ed = strings.TrimSpace(os.Getenv("VISUAL")); ed != "" {
		return ed, "$VISUAL"
	}
	if ed = strings.TrimSpace(os.Getenv("EDITOR")); ed != "" {
		return ed, "$EDITOR"
	}
	return defaultEditor, "default"
}

// editorErrText explains an editor failure for a toast. The shell's "not
// found" and "not executable" exits, or on Windows a program missing from
// PATH (editor_unix.go, editor_windows.go), usually mean a stale $VISUAL or
// $EDITOR, so name the setting.
func editorErrText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		ed, source := editorSetting()
		switch code := ee.ExitCode(); {
		case code == exitCommandNotFound || editorMissing(ed):
			return fmt.Sprintf("editor: command not found: %s (from %s)", ed, source)
		case code == exitNotExecutable:
			return fmt.Sprintf("editor: not executable: %s (from %s)", ed, source)
		}
	}
	return "editor: " + err.Error()
}
