package cli

import (
	"bytes"
	"context"
	"testing"

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
	e.subscribeRaw(t, "pat", "mem")
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
	// The server drops memory channels for an agent (decision 20, access
	// Deliver): neither the subscribed memory nor the tag-matched one arrives.
	if len(r.Messages) != 0 {
		t.Fatalf("bus receive %+v, want none for an agent", r.Messages)
	}
	// An ordinary message is delivered beside them, so the ack is real: the
	// server dropped both memories and the batch it acknowledges holds only
	// the ordinary one.
	if _, err := sam.Bus.Send(ctx, bus.SendInput{Channel: "general", Content: "ordinary"}); err != nil {
		t.Fatal(err)
	}
	b, err := pat.Bus.Receive(ctx, bus.ReceiveInput{Session: s.ID})
	if err != nil || len(b.Messages) != 1 || b.Messages[0].Channel != "general" || b.Batch == "" {
		t.Fatalf("agent receive %+v, %v, want the ordinary message only", b, err)
	}
	again, err := pat.Bus.Receive(ctx, bus.ReceiveInput{Session: s.ID, Ack: b.Batch})
	if err != nil || again.AckIgnored || len(again.Messages) != 0 {
		t.Fatalf("ack %+v, %v", again, err)
	}
}

// TestSkillNamesHumanSender pins the paragraph that tells an agent a message
// whose sender_kind is human is operator steering (spec 7).
func TestSkillNamesHumanSender(t *testing.T) {
	for _, said := range []string{"`sender_kind`", "`human`", "operator instruction", "`reply_to`"} {
		if !bytes.Contains(SkillMD, []byte(said)) {
			t.Errorf("the skill lacks %q", said)
		}
	}
}

// TestSkillThreadReplies pins the "Replies from people" paragraph: the
// slack_after hint, the history thread read and the same-channel rule.
func TestSkillThreadReplies(t *testing.T) {
	for _, said := range []string{"`slack_after`", "same channel", "`thread`"} {
		if !bytes.Contains(SkillMD, []byte(said)) {
			t.Errorf("the skill lacks %q", said)
		}
	}
}
