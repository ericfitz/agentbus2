package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/maintenance"
	"github.com/ericfitz/agentbus2/internal/version"
)

func healthBody(f *fixture) string { return stripANSI(strings.Join(f.m.healthLines(), "\n")) }

func TestHOpensHealthWithLines(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	f := newFixtureWith(t, nil, Host{Describe: "local · /tmp/x/bus.sqlite", ConfigPath: cfg})
	f.key("esc")
	f.key("h")
	if f.m.mode != modeHealth {
		t.Fatal("h opens health")
	}
	body := healthBody(f)
	for _, want := range []string{
		"version agentbus2 v" + version.Version,
		"mode local · /tmp/x/bus.sqlite",
		"identity eric · eric/tui · session " + f.c.sessionID(),
		"sessions 2 live · channels 3 (1 memory, 0 tasks)",
		"  sam ",
		"receive long-poll connected · last batch",
		"server ok · agentbus2 v" + version.Version + " · protocol " + version.Protocol + " · sqlite schema ",
		" · embeddings off",
		"admin no (status and storage are administrator views)",
		"theme · default",
		"agent cyan",
		"icons · ",
		"config · " + cfg,
		`"tui"`, `"rail_width"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("health lacks %q:\n%s", want, body)
		}
	}
	for _, not := range []string{"notice", "embeddings ok · backlog", "\nstorage ", "maintenance "} {
		if strings.Contains("\n"+body, not) {
			t.Errorf("a non-admin's health shows %q:\n%s", not, body)
		}
	}
	f.key("esc")
	if f.m.mode != modeNormal {
		t.Fatal("esc closes")
	}
}

func TestHealthAdminLines(t *testing.T) {
	f := newFixture(t)
	setAdmin(t, f.core, "eric")
	f.run(f.m.statusCmd()) // the periodic round has told the model eric is an administrator
	f.key("esc")
	f.key("h") // runs the status round and the usage call
	if !f.m.admin || !f.m.statusOK {
		t.Fatalf("setup: admin=%v statusOK=%v", f.m.admin, f.m.statusOK)
	}
	body := healthBody(f)
	for _, want := range []string{"notice none", "embeddings ok · backlog 0 · rejected 0", "last query none yet"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin health lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "admin no") {
		t.Errorf("an admin's health says admin no:\n%s", body)
	}
	if !strings.Contains(body, "storage ") || !strings.Contains(body, " of ") {
		t.Errorf("admin health lacks the storage line (Admin.Usage is on main):\n%s", body)
	}
}

// The health half of v1 TestEmbeddingFailureShownInStatusBarAndHealth: the
// error, when it happened, the rejected count and reason, wrapped.
func TestEmbeddingFailureShownInHealth(t *testing.T) {
	f := newFixture(t)
	setAdmin(t, f.core, "eric")
	f.run(f.m.statusCmd())
	f.m.width = 100
	f.m.status.EmbeddingError = "embeddings endpoint returned 401 Unauthorized (no API key in this process)"
	f.m.status.EmbeddingErrorAt = time.Now().UnixMilli()
	f.m.status.EmbeddingRejected = 2
	f.m.status.EmbeddingRejectedLast = "input too long"
	f.m.status.EmbeddingBacklog = 5
	body := healthBody(f)
	for _, want := range []string{"embeddings failing · backlog 5 · rejected 2", "last failure ", "401 Unauthorized", "2 rejected; latest reason", "input too long"} {
		if !strings.Contains(body, want) {
			t.Errorf("health lacks %q:\n%s", want, body)
		}
	}
	f.m.status.EmbeddingError = ""
	f.m.search.semanticDown = true
	if body := healthBody(f); !strings.Contains(body, "embeddings unreachable") {
		t.Errorf("health lacks the unreachable state:\n%s", body)
	}
	f.m.status.Notices = []bus.Notice{{Kind: "capacity", Message: "store at 95%"}}
	if body := healthBody(f); !strings.Contains(body, "notice store at 95%") {
		t.Errorf("health lacks the notice:\n%s", body)
	}
}

// A forbidden Usage answer prints nothing: no toast, no storage line.
func TestHealthUsageForbiddenPrintsNothing(t *testing.T) {
	f := newFixture(t)
	f.m.admin = true // forced: the server knows eric is not an administrator
	f.run(f.m.usageCmd())
	if f.m.toast != "" || f.m.usage != nil {
		t.Fatalf("toast %q usage %v", f.m.toast, f.m.usage)
	}
	if body := healthBody(f); strings.Contains(body, "storage") {
		t.Fatalf("storage line without usage:\n%s", body)
	}
	f.m.admin = false
	if f.m.usageCmd() != nil {
		t.Fatal("a non-admin must not call Usage")
	}
}

func TestHealthStorageLine(t *testing.T) {
	f := newFixture(t)
	f.m.admin = true
	f.m.usage = &bus.Usage{Bytes: 3 << 30, BudgetBytes: 2 << 30, DiskBytes: 6 << 30, DiskRatio: 2, Tier: bus.PressureHard}
	if body := healthBody(f); !strings.Contains(body, "storage 3.0 GiB of 2.0 GiB") || !strings.Contains(body, "150%") || !strings.Contains(body, "disk 6.0 GiB, 2.00x, hard") {
		t.Fatalf("over-budget storage line:\n%s", body)
	}
}

func TestHealthScrollStopsAtEnd(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("h")
	for range 200 {
		f.key("down")
	}
	if want := len(f.m.healthLines()) - 1; f.m.health.scroll != want {
		t.Fatalf("scroll = %d, want clamped at %d", f.m.health.scroll, want)
	}
	if v := f.m.View().Content; !strings.Contains(v, "}") {
		t.Fatalf("scrolled to the clamp the config JSON's closing brace must show:\n%s", v)
	}
	f.key("?")
	if f.m.mode != modeHelp {
		t.Fatal("? inside health opens help")
	}
}

func TestOOpensEditorAndToastsOnReturn(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "true")
	f := newFixtureWith(t, nil, Host{ConfigPath: cfg})
	f.key("esc")
	f.key("h")
	_, cmd := f.m.Update(keyPress("o"))
	if cmd == nil {
		t.Fatal("o must start the editor")
	}
	f.send(configEditedMsg{})
	if f.m.toast != "config saved; restart agentbus2 tui to apply" {
		t.Fatalf("toast = %q", f.m.toast)
	}
	f.m.host.ReloadConfig = func() error { return errors.New("tui.rail_width must be between 0 and 1000, got 1001") }
	f.send(configEditedMsg{})
	if f.m.toast != "config: tui.rail_width must be between 0 and 1000, got 1001" {
		t.Fatalf("toast = %q", f.m.toast)
	}
	f.m.host.ReloadConfig = func() error { return nil }
	f.send(configEditedMsg{})
	if !strings.Contains(f.m.toast, "restart agentbus2 tui") {
		t.Fatalf("toast = %q", f.m.toast)
	}
}

// A GUI $VISUAL runs in the background: o returns at once and the TUI stays
// live; the config check toasts when the editor exits. A terminal editor
// still gets the terminal (blocking).
func TestBackgroundEditorForGUI(t *testing.T) {
	for _, tc := range []struct {
		visual string
		bg     bool
	}{
		{"code --wait", true},
		{"'/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code' --wait", true},
		{"vim", false},
		{"/usr/bin/nano -w", false},
		{"'/opt/homebrew/bin/nvim'", false},
		{"vim.exe", false},
		{"VIM.EXE", false},
		{"", false},
	} {
		t.Setenv("VISUAL", tc.visual)
		if _, ok := backgroundEditor("x.json"); ok != tc.bg {
			t.Errorf("VISUAL=%q: background=%v, want %v", tc.visual, ok, tc.bg)
		}
	}
}

// healthFailBus answers Health with a fixed error and passes the rest on.
type healthFailBus struct {
	bus.Bus
	err error
}

func (b healthFailBus) Health(context.Context) (bus.Health, error) { return bus.Health{}, b.err }

func TestHealthServerLineOnError(t *testing.T) {
	f := newFixtureWith(t, func(b bus.Bus) bus.Bus {
		return healthFailBus{Bus: b, err: &bus.Error{Code: bus.CodeUnavailable, Message: "store unreachable", Retryable: true}}
	}, Host{})
	f.key("esc")
	f.key("h")
	body := healthBody(f)
	if !strings.Contains(body, "server unavailable · unavailable: store unreachable") {
		t.Errorf("health lacks the unavailable server line:\n%s", body)
	}
	if strings.Contains(body, "maintenance ") {
		t.Errorf("an unanswered health shows a maintenance line:\n%s", body)
	}
	if f.m.toast != "" {
		t.Errorf("opening health toasted %q", f.m.toast)
	}
}

func TestHealthMaintenanceLines(t *testing.T) {
	f := newFixture(t)
	setAdmin(t, f.core, "eric")
	f.run(f.m.statusCmd())
	f.key("esc")
	f.key("h")
	if body := healthBody(f); !strings.Contains(body, "maintenance unknown · last tick none yet · 0 jobs, 0 failed") {
		t.Errorf("health before a tick:\n%s", body)
	}
	svc := maintenance.New(f.core)
	svc.Tick(context.Background())
	n := len(svc.Jobs())
	f.key("esc")
	f.key("h")
	body := healthBody(f)
	for _, want := range []string{"maintenance held · last tick ", fmt.Sprintf(" · %d jobs, 0 failed", n)} {
		if !strings.Contains(body, want) {
			t.Errorf("health lacks %q:\n%s", want, body)
		}
	}
}

func TestOpenHealthClearsStaleAnswer(t *testing.T) {
	f := newFixture(t)
	f.m.healthDoc = &bus.Health{}
	f.m.healthErr = errors.New("old failure")
	_ = f.m.openHealth()
	if f.m.healthDoc != nil || f.m.healthErr != nil {
		t.Errorf("openHealth kept the old answer: doc=%v err=%v", f.m.healthDoc, f.m.healthErr)
	}
}
