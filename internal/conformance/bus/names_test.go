package bus

import (
	"context"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
)

// Legal but awkward channel names must survive the generated client's path
// escaping and the server's binding unchanged (Review Focus 7, choice 17).
// The same assertions run under local (no wire) and inproc (the wire).
func TestChannelNameRoundTripsOnTheWire(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		pat := h.as("pat")

		for _, name := range []string{"odd name", "100%", "a%2Fb", "what?", "tag#1", "c++", "café"} {
			t.Run(name, func(t *testing.T) {
				if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: name}); err != nil {
					t.Fatalf("CreateChannel: %v", err)
				}
				if err := pat.Subscribe(ctx, ab2bus.SubscribeInput{Channel: name}); err != nil {
					t.Fatalf("Subscribe: %v", err)
				}
				acl, err := pat.ACL(ctx, name)
				if err != nil {
					t.Fatalf("ACL: %v", err)
				}
				if err := pat.SetACL(ctx, ab2bus.SetACLInput{Channel: name, Entries: acl}); err != nil {
					t.Fatalf("SetACL: %v", err)
				}
				other := name + "-2"
				if err := pat.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: name, NewName: other}); err != nil {
					t.Fatalf("Rename to %q: %v", other, err)
				}
				if err := pat.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: other, NewName: name}); err != nil {
					t.Fatalf("Rename back: %v", err)
				}
				sent, err := pat.Send(ctx, ab2bus.SendInput{Channel: name, Content: "hello " + name})
				if err != nil {
					t.Fatalf("Send: %v", err)
				}
				hist, err := pat.History(ctx, ab2bus.HistoryInput{Channel: name})
				if err != nil {
					t.Fatalf("History: %v", err)
				}
				if len(hist) != 1 || hist[0].Seq != sent.Seq || hist[0].Content != "hello "+name {
					t.Fatalf("History = %+v, want the one message at seq %d", hist, sent.Seq)
				}
				chs, err := pat.ListChannels(ctx)
				if err != nil {
					t.Fatalf("ListChannels: %v", err)
				}
				seen := false
				for _, c := range chs {
					if c.Name == name {
						seen = true
					}
				}
				if !seen {
					t.Fatalf("ListChannels lacks %q: %+v", name, chs)
				}
				if err := pat.Unsubscribe(ctx, ab2bus.UnsubscribeInput{Channel: name}); err != nil {
					t.Fatalf("Unsubscribe: %v", err)
				}
				if err := pat.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: name}); err != nil {
					t.Fatalf("DeleteChannel: %v", err)
				}
				// Gone: the name now resolves to nothing.
				if err := pat.Subscribe(ctx, ab2bus.SubscribeInput{Channel: name}); ab2bus.CodeOf(err) != ab2bus.CodeNotFound {
					t.Fatalf("Subscribe after delete = %v, want not_found", err)
				}
			})
		}

		// Names no path can carry: not_found from every operation that
		// takes the name in the path (the origin differs by transport and
		// need not be told apart here), validation from CreateChannel.
		for _, name := range []string{"", "a/b"} {
			t.Run("unaddressable "+name, func(t *testing.T) {
				if err := pat.Subscribe(ctx, ab2bus.SubscribeInput{Channel: name}); ab2bus.CodeOf(err) != ab2bus.CodeNotFound {
					t.Errorf("Subscribe = %v, want not_found", err)
				}
				if _, err := pat.ACL(ctx, name); ab2bus.CodeOf(err) != ab2bus.CodeNotFound {
					t.Errorf("ACL = %v, want not_found", err)
				}
				if err := pat.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: name}); ab2bus.CodeOf(err) != ab2bus.CodeNotFound {
					t.Errorf("DeleteChannel = %v, want not_found", err)
				}
				// Send names the channel in the path too, but an empty name is
				// the core's required-field validation; on the wire the empty
				// segment would be the router's 404, so the remote Bus answers
				// validation itself (pending Eric: Milestone 4 item 6).
				wantSend := ab2bus.CodeNotFound
				if name == "" {
					wantSend = ab2bus.CodeValidation
				}
				if _, err := pat.Send(ctx, ab2bus.SendInput{Channel: name, Content: "x"}); ab2bus.CodeOf(err) != wantSend {
					t.Errorf("Send = %v, want %s", err, wantSend)
				}
				if _, err := pat.History(ctx, ab2bus.HistoryInput{Channel: name}); ab2bus.CodeOf(err) != ab2bus.CodeNotFound {
					t.Errorf("History = %v, want not_found", err)
				}
				if _, err := pat.CreateChannel(ctx, ab2bus.CreateChannelInput{Name: name}); ab2bus.CodeOf(err) != ab2bus.CodeValidation {
					t.Errorf("CreateChannel = %v, want validation", err)
				}
			})
		}
	})
}
