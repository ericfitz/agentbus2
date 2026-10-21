package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// streamHas fails unless the rendered stream contains every want.
func (f *fixture) streamHas(t *testing.T, want ...string) {
	t.Helper()
	v := f.stream()
	for _, w := range want {
		if !strings.Contains(v, w) {
			t.Fatalf("stream lacks %q:\n%s", w, v)
		}
	}
}

// onMemory opens dev-notes with a three-revision memory plus a second
// memory, and puts the stream cursor on the three-revision one.
func onMemory(t *testing.T) (f *fixture, id int64) {
	t.Helper()
	f = newFixture(t)
	first := f.samSend("dev-notes", "Release v1")
	f.samSend("dev-notes", "Reviewer checklist")
	for _, c := range []string{"Release v2", "Release v3"} {
		if _, err := f.sam.Edit(context.Background(), bus.EditMemoryInput{ID: *first.MemoryID, Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	f.key("esc")
	f.moveTo(t, "dev-notes")
	f.run(f.m.loadHistory("dev-notes", nil))
	for _, r := range f.m.rows("dev-notes") {
		if r.msg.MemoryID != nil && *r.msg.MemoryID == *first.MemoryID {
			f.m.placeCursor(r.msg.Seq)
		}
	}
	if r, ok := f.m.cursorRow(); !ok || r.msg.Content != "Release v3" {
		t.Fatalf("cursor not on the edited memory: %+v", r)
	}
	return f, *first.MemoryID
}

// . steps to older revisions and , to newer, stopping at both ends; the row
// starts on the latest revision.
func TestDotStepsToOlderRevision(t *testing.T) {
	f, _ := onMemory(t)
	f.key(",")
	f.streamHas(t, "Release v3", " r3")
	f.key(".")
	f.streamHas(t, "Release v2", " r2 · 3 kept")
	f.key(".")
	f.streamHas(t, "Release v1", " r1 · 3 kept")
	f.key(".")
	f.streamHas(t, "Release v1", " r1 · 3 kept")
	f.key(",")
	f.streamHas(t, "Release v2", " r2 · 3 kept")
	f.key(",")
	f.key(",")
	f.streamHas(t, "Release v3", " r3 · 3 kept")
}

// Any other key (here, moving the cursor away and back) returns the row to
// its latest revision.
func TestVersionResetsOnOtherKey(t *testing.T) {
	f, _ := onMemory(t)
	f.key(".")
	f.streamHas(t, "Release v2", " r2 · 3 kept")
	f.key("down")
	f.key("up")
	f.key("down")
	if r, _ := f.m.cursorRow(); r.msg.MemoryID == nil {
		t.Fatal("cursor left the memory channel")
	}
	f.key("up")
	f.streamHas(t, "Release v3", " r3")
	if strings.Contains(f.stream(), "kept") {
		t.Fatal("version view must reset to latest")
	}
}

// The version keys do nothing on an ordinary message.
func TestVersionKeysIgnoreOrdinaryRows(t *testing.T) {
	f := newFixture(t)
	a := f.samSend("dev", "hello")
	f.onChannel(t, "dev", a.Seq)
	f.key(".")
	if f.m.mem.seq != 0 {
		t.Fatal(". on an ordinary message must not start a version view")
	}
	f.key(",")
	if f.m.mem.seq != 0 {
		t.Fatal(", on an ordinary message must not start a version view")
	}
}

// A revisions answer for a row the cursor already left is ignored, and an
// error resets the view with a toast.
func TestApplyRevisionsIgnoresStaleAndReportsErrors(t *testing.T) {
	f, _ := onMemory(t)
	f.m.mem = memView{seq: 12345}
	f.send(revisionsMsg{seq: 999, revs: []bus.Message{{Seq: 1}, {Seq: 2}}})
	if f.m.mem.seq != 12345 || f.m.mem.revs != nil {
		t.Fatalf("a stale answer must not change the view: %+v", f.m.mem)
	}
	f.send(revisionsMsg{seq: 12345, err: &bus.Error{Code: bus.CodeNotFound, Message: "gone"}})
	if f.m.mem.seq != 0 || !strings.Contains(f.m.toast, "memory:") {
		t.Fatalf("an error resets the view and toasts: mem=%+v toast=%q", f.m.mem, f.m.toast)
	}
}
