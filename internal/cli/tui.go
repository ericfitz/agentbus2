package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/tui"
)

// testTUIOptions is nil in production. RunTUI applies it last, so a test can
// pin Width and Height (a buffer-backed Output never gets a WindowSizeMsg)
// exactly as testClientOptions pins the Conn.
var testTUIOptions func(*tui.Options)

// RunTUI is `agentbus2 tui [-profile P] [-identity I]`. Ported from v1
// main.go's tui case at 906dec8, without -config and -as: the identity is
// chosen by Decision 71 like every command. It runs the TUI over the Bus of
// client.Open (local or server mode) and runs the maintenance loop in local
// mode. Exit 0 on a clean quit, 1 with `agentbus2: <err>` on stderr, 2 on a
// usage or resolution error.
func RunTUI(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("tui", stderr)
	o := client.Options{Environ: environ, Maintenance: true}
	client.Flags(fs, &o)
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "agentbus2 tui: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	// Decision 11: with no identity chosen, the TUI is the OS login name.
	o.FallbackIdentity = osUserName()
	conn, code := open(ctx, o, stderr)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()

	r := conn.Resolved
	configPath := filepath.Join(r.ConfigDir, "config.json")
	cfg, err := profile.Load(configPath)
	if err != nil {
		return fail(stderr, err)
	}
	opts := tui.Options{
		Bus:      conn.Bus,
		Identity: r.Identity,
		Hint:     hostHint(),
		Input:    stdin,
		Output:   stdout,
		Stderr:   stderr,
		Host: tui.Host{
			ConfigPath:    configPath,
			Describe:      describe(r),
			SaveRailWidth: func(w int) error { return profile.SetTUIRailWidth(configPath, w) },
			ReloadConfig: func() error {
				_, err := profile.Load(configPath)
				return err
			},
		},
	}
	if cfg.TUI != nil {
		opts.Settings = tuiSettings(*cfg.TUI)
		opts.Host.RailWidth = cfg.TUI.RailWidth
	}
	// pending Eric: Milestone 8 item 2. Other live sessions of this identity share
	// its subscriptions and acked mark. Listed before this TUI opens its own.
	if ids, err := conn.Bus.Identities(ctx); err == nil {
		for _, id := range ids {
			if id.Name == r.Identity && len(id.Sessions) > 0 {
				w := fmt.Sprintf("agentbus2 tui: identity %s has %d other live session(s); subscriptions and acks are shared with them (pass -identity for a separate name)", r.Identity, len(id.Sessions))
				_, _ = fmt.Fprintln(stderr, w)
				opts.Host.Warnings = append(opts.Host.Warnings, w)
			}
		}
	}
	if testTUIOptions != nil {
		testTUIOptions(&opts)
	}
	if err := tui.Run(ctx, opts); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// describe is the health overlay's mode line.
func describe(r client.Resolved) string {
	if r.Profile.Mode == profile.ModeServer {
		return "server · " + r.Profile.Server
	}
	return "local · " + profile.SQLitePath(r.Profile, r.DataDir)
}

// tuiSettings maps the config file's tui object to the TUI's own Settings
// (internal/tui imports neither internal/profile nor internal/client).
func tuiSettings(s profile.TUISettings) tui.Settings {
	out := tui.Settings{Theme: s.Theme, Icons: s.Icons, IconMap: s.IconMap, RailWidth: s.RailWidth}
	for _, t := range s.Themes {
		out.Themes = append(out.Themes, tui.ColorTheme(t))
	}
	return out
}
