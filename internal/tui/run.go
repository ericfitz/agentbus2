// Ported from v1 906dec8 internal/tui/tui.go (Run), reduced to a program over a
// bus.Bus the caller opened: no config load, no identity override, no name setting.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// Options is everything Run needs from the entry that opened the bus.
type Options struct {
	Bus      bus.Bus
	Identity string
	Settings Settings
	Host     Host
	Input    io.Reader // nil: os.Stdin
	Output   io.Writer // nil: os.Stdout
	Stderr   io.Writer // nil: os.Stderr
	// Width and Height are for tests only: a buffer-backed Output never
	// produces a window size, so both above 0 send one. 0 leaves the size to
	// the terminal.
	Width, Height int
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
	LoadIcons(o.Settings, stderr)
	c, err := newClient(ctx, o.Bus, o.Identity)
	if err != nil {
		return Model{}, err
	}
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx)}
	if o.Input != nil {
		opts = append(opts, tea.WithInput(o.Input))
	}
	if o.Output != nil {
		opts = append(opts, tea.WithOutput(o.Output))
	}
	p := tea.NewProgram(New(c, theme, o.Host), opts...)
	if o.Width > 0 && o.Height > 0 {
		go p.Send(tea.WindowSizeMsg{Width: o.Width, Height: o.Height})
	}
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
