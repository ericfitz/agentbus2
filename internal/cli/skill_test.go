package cli

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/ericfitz/agentbus2/internal/agentview"
	"github.com/ericfitz/agentbus2/internal/bus"
)

// TestSkillDeliveryClaims pins what the skill says about memory channels:
// agents never receive them (decision 20). The skill must not carry the
// sentences that said a subscription or a tag match delivers them. The Bus
// itself, which the TUI uses, still delivers them.
func TestSkillDeliveryClaims(t *testing.T) {
	for _, gone := range []string{"Memories are not delivered by `receive`", "never cover memory channels",
		"a memory channel you subscribe to", "match memories on memory channels you may read"} {
		if bytes.Contains(SkillMD, []byte(gone)) {
			t.Errorf("the skill still says %q", gone)
		}
	}
	for _, said := range []string{"Memories are never pushed to you", "Never `subscribe` to a memory channel", "get_memory"} {
		if !bytes.Contains(SkillMD, []byte(said)) {
			t.Errorf("the skill lacks %q", said)
		}
	}

	// Park acts on the harness's primary session, so a subagent must not run it.
	if n := bytes.Count(SkillMD, []byte("a subagent never runs `agentbus2 park`")) + bytes.Count(SkillMD, []byte("never runs `agentbus2 park`")); n < 2 {
		t.Errorf("the skill names the subagent park ban %d times, want the Park and Subagents sections", n)
	}

	e := newTestEnv(t)
	ctx := context.Background()
	sam := e.conn(t, "sam")
	pat := e.conn(t, "pat")
	for _, name := range []string{"mem", "mem2"} {
		if _, err := sam.Bus.CreateChannel(ctx, bus.CreateChannelInput{Name: name, Kind: bus.KindMemory}); err != nil {
			t.Fatal(err)
		}
	}
	if err := pat.Bus.Subscribe(ctx, bus.SubscribeInput{Channel: "mem"}); err != nil {
		t.Fatal(err)
	}
	if err := pat.Bus.SubscribeTags(ctx, bus.TagSubscribeInput{Patterns: []string{"area:go"}}); err != nil {
		t.Fatal(err)
	}
	s, err := pat.Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []bus.SendInput{{Channel: "mem", Content: "subscribed"}, {Channel: "mem2", Content: "tagged", Tags: []string{"area:go"}}} {
		if _, err := sam.Bus.Send(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	r, err := pat.Bus.Receive(ctx, bus.ReceiveInput{Session: s.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 2 || r.Messages[0].Content != "subscribed" || r.Messages[1].Content != "tagged" ||
		!slices.Equal(r.Messages[1].MatchedTags, []string{"area:go"}) || r.Messages[0].MemoryID == nil {
		t.Fatalf("bus receive %+v, want the subscribed memory and the tag-matched one (the TUI path)", r.Messages)
	}
	// The agent path drops both and acknowledges the batch.
	got, err := agentview.NewKindIndex().ReceiveAgent(ctx, pat.Bus, bus.ReceiveInput{Session: s.ID, Ack: r.Batch})
	if err != nil || len(got.Messages) != 0 {
		t.Fatalf("agent receive %+v, %v", got, err)
	}
}
