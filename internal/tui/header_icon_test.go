package tui

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// #30: the messages pane title shows the channel's type icon before its
// name, in the channel type's color (a color emoji ignores it), in every
// icon mode. Cutting the title never leaves an icon's cursor escape behind.
// The kind picks the icon (ADR 0008): a channel named like a kind prefix but
// of ordinary kind gets the chat icon.
func TestPaneTitleShowsChannelTypeIcon(t *testing.T) {
	setColorProfileANSI(t)
	t.Cleanup(func() { setIcons(emojiIcons) })
	for _, mode := range []string{"emoji", "nerdfont"} {
		LoadIcons(Settings{Icons: mode}, io.Discard)
		f := newFixture(t)
		ctx := context.Background()
		for _, in := range []bus.CreateChannelInput{
			{Name: "notes", Kind: bus.KindMemory},
			{Name: "work", Kind: bus.KindTasks},
			{Name: "tasks-but-ordinary"},
		} {
			if _, err := f.sam.CreateChannel(ctx, in); err != nil {
				t.Fatal(err)
			}
		}
		f.run(f.m.statusCmd())
		f.key("esc")
		seen := map[bus.Kind]bool{}
		for i := 0; i <= len(f.m.railPositions()); i++ {
			ch := f.m.selected()
			if ch == nil {
				break
			}
			head := f.m.renderHeader()
			var want string
			switch ch.Kind {
			case bus.KindTasks:
				want = f.m.chanStyle(*ch).Render(iconTasks + ch.Name)
			case bus.KindMemory:
				want = f.m.chanStyle(*ch).Render(iconMem + ch.Name)
			default:
				want = f.m.chanStyle(*ch).Render(iconChat + ch.Name)
			}
			if !strings.HasPrefix(head, want) {
				t.Errorf("%s: %s title = %q, want prefix %q", mode, ch.Name, head, want)
			}
			seen[ch.Kind] = true
			f.key("down")
		}
		if !seen[bus.KindMemory] || !seen[bus.KindOrdinary] || !seen[bus.KindTasks] {
			t.Fatalf("%s: covered kinds %v", mode, seen)
		}
	}
}
