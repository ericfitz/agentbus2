// Ported from v1 906dec8 internal/tui/tui.go (Run), reduced to a program over a
// bus.Bus the caller opened: no config load, no identity override, no name setting.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// Options is everything Run needs from the entry that opened the bus.
type Options struct {
	Bus      bus.Bus
	Identity string
	// Hint is the session hint, the host's short name (TUI decision 12);
	// "" means "tui".
	Hint     string
	Settings Settings
	Host     Host
	Input    io.Reader // nil: os.Stdin
	Output   io.Writer // nil: os.Stdout
	Stderr   io.Writer // nil: os.Stderr
	// Width and Height are for tests only: a buffer-backed Output never
	// produces a window size, so both above 0 send one. 0 leaves the size to
	// the terminal.
	Width, Height int
	// Environ is for tests only: the environment the icon set is chosen for
	// (TERM_PROGRAM, see LoadIconsFor). nil is os.Environ(). Bubble Tea
	// itself always reads the process environment.
	Environ []string
}

// Run starts the TUI over o.Bus and returns when the user quits, the context
// is canceled or the session is lost. A canceled context is a clean quit.
func Run(ctx context.Context, o Options) error {
	_, err := run(ctx, o)
	return err
}

// run is Run's body, returning the final model for tests.
func run(ctx context.Context, o Options) (Model, error) {
	if o.Bus == nil {
		return Model{}, errors.New("tui: Options.Bus is required")
	}
	if o.Identity == "" {
		return Model{}, errors.New("tui: Options.Identity is required")
	}
	stderr := o.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	theme := LoadTheme(o.Settings, stderr)
	environ := o.Environ
	if environ == nil {
		environ = os.Environ()
	}
	LoadIconsFor(o.Settings, environ, stderr)
	c, err := newClient(ctx, o.Bus, o.Identity, o.Hint)
	if err != nil {
		if ctx.Err() != nil {
			// A cancel that lands inside a start-up bus call surfaces as
			// that call's internal error; newClient already closed any
			// session it opened. A canceled context is a clean quit.
			return Model{}, nil
		}
		return Model{}, err
	}
	opts := []tea.ProgramOption{tea.WithContext(ctx)} // the alternate screen is View's AltScreen
	if o.Input != nil {
		opts = append(opts, tea.WithInput(o.Input))
	}
	if o.Output != nil {
		opts = append(opts, tea.WithOutput(o.Output))
	}
	if o.Width > 0 && o.Height > 0 {
		// The renderer and the model both take this size; a WindowSizeMsg
		// sent after start would reach only the model, and Bubble Tea v2's
		// renderer draws nothing at size 0.
		opts = append(opts, tea.WithWindowSize(o.Width, o.Height))
	}
	p := tea.NewProgram(New(c, theme, o.Host), opts...)
	// Measure cells the way Lip Gloss lays them out (grapheme clusters, as
	// Bubble Tea v1 did by writing the frame as is). Bubble Tea v2 measures
	// with wcwidth until a terminal reports Unicode core mode 2027, so a
	// VS16 emoji (the gear, the stopwatch) would be one cell to the renderer
	// and two to the layout. The report is what v2 acts on when a terminal
	// answers its own 2027 query; it enables the mode, which a terminal that
	// does not know it ignores, and v2 resets it on exit.
	go p.Send(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeSet})
	go c.receiveLoop(func(m any) { p.Send(m) })
	final, err := p.Run()
	_ = c.close()
	m, _ := final.(Model)
	if err != nil {
		if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
			return m, nil
		}
		return m, fmt.Errorf("tui: %w", err)
	}
	if m.lost != nil {
		return m, m.lost
	}
	return m, nil
}
