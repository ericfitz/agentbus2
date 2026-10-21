package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// samSendSubject posts content with a subject as sam's session.
func (f *busFixture) samSendSubject(ch, subject, content string) bus.SendResult {
	f.t.Helper()
	r, err := f.sam.Send(context.Background(), bus.SendInput{Channel: ch, Subject: subject, Content: content, Session: f.samSession.ID})
	if err != nil {
		f.t.Fatalf("samSendSubject %q: %v", ch, err)
	}
	return r
}

// A collapsed row shows the subject, or the first line when there's none,
// and hides the rest of the content behind the marker.
func TestCollapsedRowShowsSubjectElseFirstLine(t *testing.T) {
	f := newFixture(t)
	f.samSendSubject("dev", "Deploy plan", "step one\nstep two")
	f.samSend("dev", "first line\nsecond line")
	f.receive(t)
	s := f.stream()
	for _, want := range []string{"Deploy plan", "first line"} {
		if !strings.Contains(s, want) {
			t.Fatalf("row line %q missing:\n%s", want, s)
		}
	}
	for _, hidden := range []string{"step one", "second line"} {
		if strings.Contains(s, hidden) {
			t.Fatalf("body %q must stay hidden:\n%s", hidden, s)
		}
	}
	if strings.Count(s, markSel) != 2 {
		t.Fatalf("both rows have a body to open:\n%s", s)
	}
}

// A one-line message with no subject that fits the width has no body: no
// marker, and right has nothing to open.
func TestOneLineMessageWithoutSubjectHasNoBody(t *testing.T) {
	f := newFixture(t)
	a := f.samSend("dev", "hello")
	f.onChannel(t, "dev", a.Seq)
	if s := f.stream(); strings.Contains(s, markSel) || strings.Contains(s, markOpen) {
		t.Fatalf("nothing to expand:\n%s", s)
	}
	f.key("right")
	if f.m.bodyOpen[a.Seq] {
		t.Fatal("right must not open an empty body")
	}
}

// A first line wider than the pane is cut with an ellipsis and counts as a
// body: right shows the full text.
func TestCutFirstLineOpensAsBody(t *testing.T) {
	f := newFixture(t)
	a := f.samSend("dev", strings.Repeat("word ", 30))
	f.m.width = 60
	f.m.layout()
	f.onChannel(t, "dev", a.Seq)
	if s := f.stream(); !strings.Contains(s, "…") || !strings.Contains(s, markSel) {
		t.Fatalf("a cut first line is openable:\n%s", s)
	}
	f.key("right")
	if s := f.stream(); strings.Count(s, "word") != 30 || !strings.Contains(s, markOpen) {
		t.Fatalf("the open body shows the whole text:\n%s", s)
	}
}

// Stepping memory versions shows each version's subject and, with the body
// open, its content; the body stays open across steps.
func TestBodyStaysOpenAcrossMemoryVersions(t *testing.T) {
	f := newFixture(t)
	first := f.samSendSubject("dev-notes", "Release v1", "notes one")
	v2 := "Release v2"
	if _, err := f.sam.Edit(context.Background(), bus.EditMemoryInput{ID: *first.MemoryID, Subject: &v2, Content: "notes two"}); err != nil {
		t.Fatal(err)
	}
	f.key("esc")
	f.moveTo(t, "dev-notes")
	f.run(f.m.loadHistory("dev-notes", nil))
	for _, r := range f.m.rows("dev-notes") {
		if r.msg.MemoryID != nil && *r.msg.MemoryID == *first.MemoryID {
			f.m.placeCursor(r.msg.Seq)
		}
	}
	f.streamHas(t, "Release v2")
	if strings.Contains(f.stream(), "notes two") {
		t.Fatal("starts collapsed")
	}
	f.key("right")
	f.streamHas(t, "Release v2", "notes two")
	f.key(".")
	f.streamHas(t, "Release v1", "notes one", " r1 · 2 kept")
	if s := f.stream(); strings.Contains(s, "notes two") || strings.Contains(s, "Release v2") {
		t.Fatalf("the selected version only:\n%s", s)
	}
	f.key(",")
	f.streamHas(t, "Release v2", "notes two", " r2 · 2 kept")
}
