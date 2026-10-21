package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/core/rules"
)

// tagSets is the TUI identity's tag subscriptions.
func (f *fixture) tagSets(t *testing.T) [][]string {
	t.Helper()
	sets, err := f.c.b.TagSubscriptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return sets
}

// typeText types s into the open prompt, then presses enter.
func (f *fixture) typeText(s string) {
	for _, r := range s {
		f.key(string(r))
	}
	f.key("enter")
}

// t on the rail, typing "ENV:*, failed": the prompt trims and the server
// normalizes; the rail then lists the set and the pane shows every loaded
// message in an ordinary or memory channel whose tags satisfy it.
func TestTFollowsTagSet(t *testing.T) {
	if got := splitTags("ENV:*, failed"); !slices.Equal(got, []string{"ENV:*", "failed"}) {
		t.Fatalf("splitTags: %v", got)
	}
	f := newFixture(t)
	f.key("esc")
	f.key("t")
	f.typeText("ENV:*, failed")
	sets := f.tagSets(t)
	if len(sets) != 1 || !slices.Equal(sets[0], []string{"env:*", "failed"}) {
		t.Fatalf("t prompt subscribes a normalized pattern set: %v", sets)
	}
	f.moveTo(t, tagPanePrefix+"env:*,failed")
	rail := stripANSI(f.m.renderRails())
	if !strings.Contains(rail, "tags") || !strings.Contains(rail, "env:*") || !strings.Contains(rail, "failed") {
		t.Fatalf("rail lacks the tags row with its chips:\n%s", rail)
	}
}

func TestTagPaneShowsMatchingLoadedMessages(t *testing.T) {
	f := newFixture(t)
	f.followTags(t, []string{"env:*", "failed"})
	for _, m := range []struct {
		ch, content string
		tags        []string
	}{
		{"dev", "prod failure", []string{"env:prod", "failed"}},
		{"general", "staging failure", []string{"env:staging", "failed"}},
		{"dev", "no env", []string{"failed"}},
		{"dev", "no failed", []string{"env:prod"}},
		{"dev", "flat env", []string{"env", "failed"}},
		{"dev-notes", "memory failure", []string{"env:dev", "failed"}},
	} {
		f.samSend(m.ch, m.content, m.tags...)
	}
	f.receive(t)
	f.key("esc")
	f.moveTo(t, tagPanePrefix+"env:*,failed")
	s := f.stream()
	for _, want := range []string{"prod failure", "staging failure", "memory failure", "env:prod"} {
		if !strings.Contains(s, want) {
			t.Fatalf("pane lacks %q:\n%s", want, s)
		}
	}
	for _, unwanted := range []string{"no env", "no failed", "flat env"} {
		if strings.Contains(s, unwanted) {
			t.Fatalf("pane shows %q, which does not satisfy env:* and failed:\n%s", unwanted, s)
		}
	}
	// Rows keep the channel in their header.
	if !strings.Contains(s, iconArrow+iconChat+"dev") {
		t.Fatalf("rows keep the channel header:\n%s", s)
	}
}

// Every key that would post or reply on a tag pane answers the read-only
// toast and leaves the mode alone.
func TestTagPaneIsReadOnly(t *testing.T) {
	f := newFixture(t)
	f.followTags(t, []string{"bug"})
	f.samSend("dev", "broken", "bug")
	f.receive(t)
	f.key("esc")
	f.moveTo(t, tagPanePrefix+"bug")
	for _, k := range []string{"i", "enter"} {
		f.m.toast = ""
		f.key(k)
		if f.m.mode != modeNormal || f.m.toast != tagReadOnlyToast {
			t.Fatalf("key %q on a tag pane: mode=%v toast=%q", k, f.m.mode, f.m.toast)
		}
	}
	f.key("tab") // stream
	f.m.toast = ""
	f.key("enter")
	if f.m.mode != modeNormal || f.m.replyTo != nil {
		t.Fatalf("enter on a tag pane row must not start a reply: mode=%v replyTo=%v", f.m.mode, f.m.replyTo)
	}
}

// d and s on a tag set unfollow it with no confirmation; the selection goes
// to the next set, else the previous one, else the last channel, and focus
// stays in the rail.
func TestDUnfollowsTagSet(t *testing.T) {
	f := newFixture(t)
	f.followTags(t, []string{"a"}, []string{"b"}, []string{"c"})
	var lastChannel string
	for _, c := range f.m.channels {
		if !isTagPane(c.Name) {
			lastChannel = c.Name
		}
	}
	f.key("esc")
	f.moveTo(t, tagPanePrefix+"b")
	f.key("d")
	if f.m.mode != modeNormal || f.m.pane() != paneChannels || f.m.selName() != tagPanePrefix+"c" {
		t.Fatalf("d on b: mode=%v pane=%v sel=%q, want the next set c with no confirmation", f.m.mode, f.m.pane(), f.m.selName())
	}
	f.key("s")
	if f.m.selName() != tagPanePrefix+"a" {
		t.Fatalf("s on the last set: sel=%q, want the previous set a", f.m.selName())
	}
	f.key("d")
	if f.m.selName() != lastChannel {
		t.Fatalf("d on the only set: sel=%q, want the last channel %q", f.m.selName(), lastChannel)
	}
	if sets := f.tagSets(t); len(sets) != 0 {
		t.Fatalf("every set must be unfollowed: %v", sets)
	}
	for _, c := range f.m.channels {
		if isTagPane(c.Name) {
			t.Fatalf("rail still lists a set: %v", f.m.channels)
		}
	}
}

// t opens the prompt from the rail only: in compose it types, in the stream
// it does nothing.
func TestTKeyOpensThePromptOnlyFromTheRail(t *testing.T) {
	f := newFixture(t)
	f.samSend("dev", "hello")
	f.receive(t)
	f.key("t") // compose has focus: text
	if f.m.prompt.active || f.m.compose.Value() != "t" {
		t.Fatalf("t in compose must type: prompt=%v value=%q", f.m.prompt.active, f.m.compose.Value())
	}
	f.key("ctrl+u")
	f.key("esc") // channel list
	f.key("tab") // stream
	if f.m.pane() != paneStream {
		t.Fatalf("pane = %v, want stream", f.m.pane())
	}
	f.key("t")
	if f.m.prompt.active {
		t.Fatal("t in a message list must not open the tag prompt")
	}
	f.key("shift+tab")
	f.key("shift+tab")
	f.key("home")
	f.key("t")
	if !f.m.prompt.active || !strings.HasPrefix(f.m.prompt.label, "tags") {
		t.Fatalf("t in the channel list opens the tag prompt: active=%v label=%q", f.m.prompt.active, f.m.prompt.label)
	}
}

// A pattern the server rejects is a toast, never a rail row.
func TestTagPromptRejectsInvalidPattern(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.key("t")
	f.typeText("bad tag!")
	if sets := f.tagSets(t); len(sets) != 0 {
		t.Fatalf("an invalid pattern must not subscribe: %v", sets)
	}
	if !strings.Contains(f.m.toast, "subscribe") {
		t.Fatalf("toast = %q, want a subscribe error", f.m.toast)
	}
	for _, c := range f.m.channels {
		if isTagPane(c.Name) {
			t.Fatalf("rail lists a set that was refused: %v", f.m.channels)
		}
	}
}

// unfollowTagPane on anything but a tag set does nothing.
func TestUnfollowTagPaneIgnoresOrdinaryChannels(t *testing.T) {
	f := newFixture(t)
	f.key("esc")
	f.moveTo(t, "dev")
	if cmd := f.m.unfollowTagPane(); cmd != nil {
		t.Fatal("unfollowTagPane on an ordinary channel must be a no-op")
	}
}

// matchTag is a copy of the server's rule; they must agree.
func TestMatchTagAgreesWithTheCore(t *testing.T) {
	patterns := []string{"env", "env*", "env:*", "env:prod", "failed", "a-b*"}
	tags := []string{"env", "environment", "env:prod", "env:staging", "env:", "failed", "fail", "a-b", "a-b:c", "zzz"}
	for _, p := range patterns {
		for _, tag := range tags {
			if got, want := matchTag(p, tag), rules.MatchTag(p, tag); got != want {
				t.Errorf("matchTag(%q, %q) = %v, core says %v", p, tag, got, want)
			}
		}
	}
	if got, want := matchTags([]string{"env:*", "failed"}, []string{"env:prod", "failed"}), true; got != want {
		t.Errorf("matchTags set = %v", got)
	}
	if !matchTags(nil, nil) {
		t.Error("no patterns match everything")
	}
}
