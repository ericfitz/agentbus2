package cli

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// TestSkillDeliveryClaims pins what the skill says receive delivers from
// memory channels: a memory on a subscribed memory channel arrives, and a
// tag subscription matches memories on readable memory channels too. The
// skill must not carry the v1 sentences that say otherwise.
func TestSkillDeliveryClaims(t *testing.T) {
	for _, gone := range []string{"Memories are not delivered by `receive`", "never cover memory channels"} {
		if bytes.Contains(SkillMD, []byte(gone)) {
			t.Errorf("the skill still says %q", gone)
		}
	}
	for _, said := range []string{"a memory channel you subscribe to", "match memories on memory channels you may read"} {
		if !bytes.Contains(SkillMD, []byte(said)) {
			t.Errorf("the skill lacks %q", said)
		}
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
		t.Fatalf("received %+v, want the subscribed memory and the tag-matched one", r.Messages)
	}
}
