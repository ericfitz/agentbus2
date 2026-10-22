package tui

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// rowContents renders rows as their content, one ">" per depth level.
func rowContents(rs []row) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = strings.Repeat(">", r.depth) + r.msg.Content
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

// onChannel selects ch, loads it through history (no peeked replies), and
// puts the stream cursor on seq.
func (f *fixture) onChannel(t *testing.T, ch string, seq int64) {
	t.Helper()
	f.key("esc")
	f.moveTo(t, ch)
	f.run(f.m.loadHistory(ch, nil))
	f.m.placeCursor(seq)
	if r, ok := f.m.cursorRow(); !ok || r.msg.Seq != seq {
		t.Fatalf("cursor not on %d: %+v", seq, r)
	}
}

// Threads sort by newest message; replies stay hidden until expanded, and
// expanding shows only the direct children.
func TestRepliesCollapseUnderRoot(t *testing.T) {
	f := newFixture(t)
	a := f.samSend("dev", "A")
	f.samSend("dev", "B")
	r1 := f.samReply("dev", a.Seq, "A1")
	f.samReply("dev", r1.Seq, "A1a")
	f.run(f.m.loadHistory("dev", nil)) // history load: no peek
	rs := f.m.rows("dev")
	if got := rowContents(rs); !eq(got, []string{"B", "A"}) || rs[1].hidden != 2 || rs[1].latest == 0 {
		t.Fatalf("collapsed rows %v hidden=%d latest=%d", got, rs[1].hidden, rs[1].latest)
	}
	f.m.expanded[a.Seq] = true
	rs = f.m.rows("dev")
	if got := rowContents(rs); !eq(got, []string{"B", "A", ">A1"}) || rs[2].hidden != 1 {
		t.Fatalf("one level expanded %v hidden=%d", got, rs[2].hidden)
	}
	v := f.m.renderStream()
	if !strings.Contains(stripANSI(v), markSel+" ") || !strings.Contains(v, "1 reply") || !strings.Contains(stripANSI(v), markOpen+" ") {
		t.Fatalf("summary line missing:\n%s", v)
	}
}

// A reply whose parent is not loaded is a root.
func TestReplyToUnloadedParentIsRoot(t *testing.T) {
	f := newFixture(t)
	a := f.samSend("dev", "A")
	r := f.samReply("dev", a.Seq, "A1")
	f.send(historyMsg{channel: "dev", msgs: []bus.Message{{Seq: r.Seq, Channel: "dev", Sender: "sam", Content: "A1", ReplyTo: &a.Seq}}})
	rs := f.m.rows("dev")
	if got := rowContents(rs); !eq(got, []string{"A1"}) || rs[0].depth != 0 || rs[0].root != r.Seq {
		t.Fatalf("a reply to an unloaded parent is a root: %v %+v", got, rs)
	}
	if root := f.m.rootSeq("dev", r.Seq); root != r.Seq {
		t.Fatalf("rootSeq = %d, want %d", root, r.Seq)
	}
}

// A reply received live peeks just that reply (and its path) under a
// collapsed thread; a newer reply replaces it; right on the root drops the
// peek and shows its direct children.
func TestPeekShowsNewestReplyWhileCollapsed(t *testing.T) {
	f := newFixture(t)
	a := f.samSend("dev", "A")
	f.receive(t)
	r1 := f.samReply("dev", a.Seq, "A1")
	f.receive(t)
	if got := rowContents(f.m.rows("dev")); !eq(got, []string{"A", ">A1"}) {
		t.Fatalf("peek after live reply: %v", got)
	}
	f.samReply("dev", r1.Seq, "A1a")
	f.receive(t)
	if got := rowContents(f.m.rows("dev")); !eq(got, []string{"A", ">A1", ">>A1a"}) {
		t.Fatalf("newer peek replaces path: %v", got)
	}
	f.samReply("dev", a.Seq, "A2")
	f.receive(t)
	if got := rowContents(f.m.rows("dev")); !eq(got, []string{"A", ">A2"}) {
		t.Fatalf("newest reply is the only peek: %v", got)
	}
	f.key("shift+tab") // compose -> stream directly; cursor on the last row, A2
	f.key("up")        // root A
	f.key("right")
	if got := rowContents(f.m.rows("dev")); !eq(got, []string{"A", ">A1", ">A2"}) {
		t.Fatalf("right on the root shows direct children only: %v", got)
	}
	f.key("down")
	f.key("enter")
	if f.m.replyTo == nil || f.m.replyTo.Content != "A1" {
		t.Fatalf("enter replies to the visible cursor row, got %+v", f.m.replyTo)
	}
}

// Right opens the body, then the direct replies.
func TestRightOpensBodyThenReplies(t *testing.T) {
	f := newFixture(t)
	a := f.samSendSubject("dev", "Deploy plan", "step one")
	f.samReply("dev", a.Seq, "A1")
	f.onChannel(t, "dev", a.Seq)
	if got := rowContents(f.m.rows("dev")); !eq(got, []string{"step one"}) || strings.Contains(f.stream(), "step one") {
		t.Fatalf("starts collapsed: rows %v\n%s", got, f.stream())
	}
	f.key("right")
	if s := f.stream(); !strings.Contains(s, "step one") || !eq(rowContents(f.m.rows("dev")), []string{"step one"}) || !strings.Contains(s, markSel) {
		t.Fatalf("first right opens the body only; replies still hidden:\n%s", s)
	}
	f.key("right")
	if got := rowContents(f.m.rows("dev")); !eq(got, []string{"step one", ">A1"}) || !strings.Contains(f.stream(), markOpen) {
		t.Fatalf("second right shows the replies: %v\n%s", got, f.stream())
	}
}

// Left hides the whole subtree of replies, then closes the body; the next
// right shows direct replies only; space does not toggle replies.
func TestLeftHidesRepliesThenBody(t *testing.T) {
	f := newFixture(t)
	a := f.samSendSubject("dev", "Deploy plan", "step one")
	a1 := f.samReply("dev", a.Seq, "A1")
	f.samReply("dev", a1.Seq, "A11")
	f.onChannel(t, "dev", a.Seq)
	f.key("right") // body
	f.key("right") // direct replies
	f.key("down")
	f.key("right") // A1's body is one line: its replies
	if got := rowContents(f.m.rows("dev")); !eq(got, []string{"step one", ">A1", ">>A11"}) {
		t.Fatalf("right on A1 shows A11: %v", got)
	}
	f.key("up")
	f.key("left")
	if s := f.stream(); !eq(rowContents(f.m.rows("dev")), []string{"step one"}) || !strings.Contains(s, "step one") {
		t.Fatalf("first left hides every reply level and keeps the body open:\n%s", s)
	}
	f.key("right")
	if got := rowContents(f.m.rows("dev")); !eq(got, []string{"step one", ">A1"}) {
		t.Fatalf("expanding after a full collapse shows direct replies only: %v", got)
	}
	f.key("left")
	f.key("left")
	if s := f.stream(); strings.Contains(s, "step one") || !strings.Contains(s, "Deploy plan") || !strings.Contains(s, markSel) {
		t.Fatalf("second left closes the body:\n%s", s)
	}
	f.key(" ")
	if got := rowContents(f.m.rows("dev")); !eq(got, []string{"step one"}) || strings.Contains(f.stream(), "step one") {
		t.Fatalf("space does not show or hide replies: %v\n%s", got, f.stream())
	}
}

// Expanding the last thread at the bottom of the pane must scroll up to
// show the replies it revealed, not just keep its header on screen.
func TestExpandScrollsRevealedRepliesIntoView(t *testing.T) {
	f := newFixture(t)
	for range 30 {
		f.samSend("dev", "filler")
	}
	a := f.samSend("dev", "A")
	for range 3 {
		f.samReply("dev", a.Seq, "reply")
	}
	f.run(f.m.loadHistory("dev", nil))
	f.m.height = 20
	f.m.layout()
	f.key("shift+tab") // into the message pane; the cursor starts on the last row, A
	if r, ok := f.m.cursorRow(); !ok || r.msg.Seq != a.Seq {
		t.Fatalf("cursor not on A: %+v", r)
	}
	f.key("right")
	if got := len(f.m.rows("dev")); got != 34 {
		t.Fatalf("right did not expand A's replies: %d rows", got)
	}
	if bottom := f.m.stream.YOffset() + f.m.stream.Height(); f.m.cursorEnd > bottom || f.m.cursorLine < f.m.stream.YOffset() {
		t.Fatalf("expanded thread not in view: lines %d..%d, window %d..%d", f.m.cursorLine, f.m.cursorEnd, f.m.stream.YOffset(), bottom)
	}
}

// A reply's wrapped lines keep the reply's indent.
func TestReplyIndentAppliesToWrappedLines(t *testing.T) {
	f := newFixture(t)
	a := f.samSend("dev", "A")
	f.samReply("dev", a.Seq, strings.Repeat("word ", 30))
	f.receive(t)
	f.m.width = 60
	f.m.layout()
	f.key("shift+tab") // compose -> stream directly
	f.key("up")
	f.key("right") // A has no body: shows the reply
	f.key("down")
	f.key("right") // the reply's cut first line is its body: open it
	var reply []string
	for _, l := range strings.Split(f.m.renderStream(), "\n") {
		if strings.Contains(l, "word") {
			reply = append(reply, stripANSI(l))
		}
	}
	if len(reply) < 2 {
		t.Fatalf("reply did not wrap: %q", reply)
	}
	for _, l := range reply[1:] {
		if !strings.HasPrefix(l, "    ") {
			t.Fatalf("wrapped reply line lost its indent: %q", l)
		}
	}
}
