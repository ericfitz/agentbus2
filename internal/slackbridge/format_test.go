package slackbridge

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/slackapi"
)

func TestSlackChannelName(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "pai-job-12345", want: "pai-job-12345"},
		{in: "Job_1 Alpha", want: "job_1-alpha"},
		{in: strings.Repeat("a", 100), want: strings.Repeat("a", 80)},
		{in: "日本", wantErr: true},
		{in: "", wantErr: true},
		{in: "---", wantErr: true},
		{in: "a.b", want: "a-b"},
	}
	for _, c := range cases {
		got, err := SlackChannelName(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: want error, got %q", c.in, got)
			} else if !strings.Contains(err.Error(), "-slack-name") || !strings.Contains(err.Error(), c.in) {
				t.Errorf("%q: error %q is not actionable", c.in, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.in, got, err, c.want)
		}
		if !slackapi.ValidChannelName(got) {
			t.Errorf("%q: %q is not a valid Slack name", c.in, got)
		}
	}
}

func TestShortName(t *testing.T) {
	cases := [][3]string{
		{"pai-job-1-coder", "pai-job-1", "coder"},
		{"pai-job-1", "pai-job-1", "pai-job-1"},
		{"coder", "pai-job-1", "coder"},
		{"pai-job-1-", "pai-job-1", "pai-job-1-"},
		{"pai-job-10-coder", "pai-job-1", "pai-job-10-coder"},
		{"x", "", "x"},
	}
	for _, c := range cases {
		if got := ShortName(c[0], c[1]); got != c[2] {
			t.Errorf("ShortName(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestIconEmojiStable(t *testing.T) {
	for _, id := range []string{"a", "coder", "pai-job-1-coder", "alice@corp.com", ""} {
		e := IconEmoji(id)
		if e != IconEmoji(id) {
			t.Errorf("%q: unstable", id)
		}
		if !slices.Contains(iconEmojis, e) {
			t.Errorf("%q: %q not in list", id, e)
		}
	}
	seen := map[string]bool{}
	for i := range 200 {
		seen[IconEmoji(fmt.Sprintf("pai-job-%d-coder", i))] = true
	}
	if len(seen) < 4 {
		t.Errorf("only %d distinct emoji over 200 identities", len(seen))
	}
}

// runes counts the characters Slack limits.
func runes(s string) int { return utf8.RuneCountInString(s) }

const note = "(truncated; the full message is seq 7 on agentbus2 channel c)"

func TestTruncateKeepsMarkup(t *testing.T) {
	const max = 200
	check := func(t *testing.T, name, in string) string {
		t.Helper()
		got := Truncate(in, max, note)
		if runes(got) > max {
			t.Errorf("%s: %d runes > %d", name, runes(got), max)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: invalid UTF-8", name)
		}
		if !strings.HasSuffix(got, note) {
			t.Errorf("%s: no note", name)
		}
		if strings.Count(got, "```")%2 != 0 {
			t.Errorf("%s: open fence in %q", name, got)
		}
		body := strings.TrimSuffix(got, note)
		if strings.Count(body, "<") != strings.Count(body, ">") {
			t.Errorf("%s: cut inside a link: %q", name, got)
		}
		return got
	}

	t.Run("short input unchanged", func(t *testing.T) {
		for _, in := range []string{"", "hello", strings.Repeat("é", max)} {
			if got := Truncate(in, max, note); got != in {
				t.Errorf("changed %q", in)
			}
		}
	})
	t.Run("counts runes not bytes", func(t *testing.T) {
		in := strings.Repeat("日本語", 100)
		got := check(t, "cjk", in)
		// every rune of the budget is used: the cut is by runes, not bytes.
		want := max - runes("\n"+note)
		if runes(strings.TrimSuffix(got, "\n"+note)) != want {
			t.Errorf("kept %d runes, want %d", runes(strings.TrimSuffix(got, "\n"+note)), want)
		}
	})
	t.Run("emoji", func(t *testing.T) {
		in := strings.Repeat("👍🏽x🎉", 100)
		check(t, "emoji", in)
	})
	t.Run("exactly at limit is unchanged", func(t *testing.T) {
		in := strings.Repeat("😀", max)
		if got := Truncate(in, max, note); got != in {
			t.Error("changed an input of exactly max runes")
		}
		if got := Truncate(in+"x", max, note); got == in+"x" {
			t.Error("did not truncate max+1 runes")
		}
	})
	t.Run("link near the cut moves before it", func(t *testing.T) {
		budget := max - runes("\n"+note)
		link := "<https://example.com/a/very/long/path|label text>"
		// every start offset from before the link to inside the link
		for off := budget - len([]rune(link)) - 2; off <= budget+2; off++ {
			in := strings.Repeat("é", off) + link + strings.Repeat("é", 300)
			got := check(t, "link", in)
			if off+len([]rune(link)) > budget && strings.Contains(got, "<") {
				t.Errorf("off %d: link kept though it crosses the cut: %q", off, got)
			}
			if off+len([]rune(link)) <= budget && !strings.Contains(got, link) {
				t.Errorf("off %d: whole link dropped", off)
			}
		}
	})
	t.Run("open fence is closed", func(t *testing.T) {
		budget := max - runes("\n"+note)
		for off := 0; off <= budget+3; off++ {
			in := strings.Repeat("a", off) + "\n```\ncode line\n" + strings.Repeat("c", 400) + "\n```\n"
			check(t, "fence", in)
		}
		in := "```\n" + strings.Repeat("c", 400) + "\n```"
		got := check(t, "fence at start", in)
		if !strings.Contains(got, "\n```\n"+note) {
			t.Errorf("fence not closed before the note: %q", got)
		}
	})
	t.Run("a closed fence stays closed", func(t *testing.T) {
		in := "```\nx\n```\n" + strings.Repeat("y", 400)
		got := check(t, "closed", in)
		if !strings.HasPrefix(got, "```\nx\n```\n") {
			t.Errorf("closed fence damaged: %q", got)
		}
	})
	t.Run("angle bracket in a fence is not a link", func(t *testing.T) {
		in := "```\nif a < b {\n" + strings.Repeat("c", 400) + "\n```"
		got := Truncate(in, max, note)
		if !strings.HasPrefix(got, "```\nif a < b {\n") {
			t.Errorf("code before the cut was dropped: %q", got)
		}
	})
	t.Run("entity is not split", func(t *testing.T) {
		budget := max - runes("\n"+note)
		for off := budget - 6; off <= budget; off++ {
			in := strings.Repeat("a", off) + "&amp;&lt;" + strings.Repeat("b", 300)
			got := check(t, "entity", in)
			body := strings.TrimSuffix(got, "\n"+note)
			if i := strings.LastIndex(body, "&"); i >= 0 && !strings.Contains(body[i:], ";") {
				t.Errorf("off %d: entity split: %q", off, body[len(body)-min(10, len(body)):])
			}
		}
	})
	t.Run("partial fence marker is dropped", func(t *testing.T) {
		budget := max - runes("\n"+note)
		in := strings.Repeat("a", budget-2) + "\n```go\ncode\n" + strings.Repeat("c", 100)
		check(t, "marker", in)
	})
	t.Run("note nearly fills the limit", func(t *testing.T) {
		long := strings.Repeat("n", 190)
		got := Truncate(strings.Repeat("x", 500), max, long)
		if runes(got) > max || !strings.HasSuffix(got, long) {
			t.Errorf("bad result: %d runes", runes(got))
		}
		got = Truncate(strings.Repeat("x", 500), max, strings.Repeat("n", 199))
		if runes(got) > max {
			t.Errorf("%d runes", runes(got))
		}
		// a note longer than the whole limit is itself cut to fit.
		got = Truncate(strings.Repeat("x", 500), max, strings.Repeat("n", 300))
		if runes(got) != max {
			t.Errorf("%d runes, want %d", runes(got), max)
		}
		// open fence plus a note that leaves less room than the closing fence.
		got = Truncate("```\n"+strings.Repeat("c", 500), max, strings.Repeat("n", 195))
		if runes(got) > max {
			t.Errorf("%d runes", runes(got))
		}
	})
	t.Run("inline code is not left open", func(t *testing.T) {
		budget := max - runes("\n"+note)
		for off := 0; off <= budget+3; off++ {
			in := strings.Repeat("a", off) + " `" + strings.Repeat("c", 400) + "` tail"
			got := check(t, "code", in)
			body := strings.TrimSuffix(got, note)
			if strings.Count(body, "`")%2 != 0 {
				t.Fatalf("off %d: unbalanced inline code in %q", off, got)
			}
		}
	})
	t.Run("a closed inline code span stays whole", func(t *testing.T) {
		in := "x `code` " + strings.Repeat("y", 400)
		got := check(t, "closed code", in)
		if !strings.HasPrefix(got, "x `code` y") {
			t.Errorf("closed span damaged: %q", got)
		}
	})
	t.Run("fuzz-ish sweep", func(t *testing.T) {
		parts := []string{"é", "日", "😀", "<http://x.y/z|t>", "```\n", "```", "\n", "`c`", "&amp;", "a < b", "> q", "**b**"}
		seed := uint32(1)
		for range 3000 {
			var b strings.Builder
			for range 80 {
				seed = seed*1664525 + 1013904223
				b.WriteString(parts[int(seed>>16)%len(parts)])
			}
			in := b.String()
			got := Truncate(in, max, note)
			if runes(in) > max && runes(got) > max {
				t.Fatalf("over limit: %d for %q", runes(got), in)
			}
			if runes(in) > max && !strings.HasSuffix(got, note) {
				t.Fatalf("no note for %q", in)
			}
		}
	})
}

func TestFormatPost(t *testing.T) {
	m := bus.Message{
		Seq: 42, Channel: "pai-job-1", Sender: "pai-job-1-coder",
		Subject: "Build <done>", Content: "**ok** see [log](https://x.y/z)",
		Tags: []string{"build", "ci"},
	}
	p := FormatPost(m, "pai-job-1", 3000, "")
	if p.Seq != 42 || p.Username != "coder" {
		t.Errorf("seq/username: %+v", p)
	}
	if p.IconEmoji != IconEmoji("pai-job-1-coder") {
		t.Errorf("icon %q", p.IconEmoji)
	}
	wantSection := "*Build &lt;done&gt;*\n*ok* see <https://x.y/z|log>"
	if p.Section != wantSection {
		t.Errorf("section %q, want %q", p.Section, wantSection)
	}
	if !slices.Equal(p.Context, []string{"`build`", "`ci`"}) {
		t.Errorf("context %v", p.Context)
	}
	if p.Fallback == "" || strings.Contains(p.Fallback, "\n") || runes(p.Fallback) > 150 {
		t.Errorf("fallback %q", p.Fallback)
	}

	// no subject, no tags, with a quote
	q := FormatPost(bus.Message{Seq: 1, Channel: "c", Sender: "a", Content: "hi"}, "", 3000, "first\nsecond <x>")
	if q.Section != "> first\n> second &lt;x&gt;\nhi" {
		t.Errorf("quote section %q", q.Section)
	}
	if len(q.Context) != 0 {
		t.Errorf("context %v", q.Context)
	}

	// truncated note names the seq and channel; within the limit
	big := FormatPost(bus.Message{Seq: 9, Channel: "chan", Sender: "a", Subject: "s", Content: strings.Repeat("日", 5000)}, "", 300, "")
	if runes(big.Section) > 300 || !strings.HasSuffix(big.Section, "(truncated; the full message is seq 9 on agentbus2 channel chan)") {
		t.Errorf("truncation: %d runes, tail %q", runes(big.Section), big.Section[len(big.Section)-70:])
	}
	if !strings.HasPrefix(big.Section, "*s*\n") {
		t.Errorf("subject lost: %q", big.Section[:20])
	}

	// a very long fallback is capped by runes
	f := FormatPost(bus.Message{Seq: 1, Channel: "c", Sender: "a", Content: strings.Repeat("😀", 400)}, "", 3000, "")
	if runes(f.Fallback) != 150 {
		t.Errorf("fallback %d runes", runes(f.Fallback))
	}
}

func TestFormatPostNeverMentions(t *testing.T) {
	evil := "<!channel> <!here> <@U123> <https://x|y>"
	p := FormatPost(bus.Message{Seq: 1, Channel: "c", Sender: "a", Subject: evil, Content: evil, Tags: nil}, "", 3000, evil)
	const esc = "&lt;!channel&gt; &lt;!here&gt; &lt;@U123&gt; &lt;https://x|y&gt;"
	for name, got := range map[string]string{"section": p.Section, "fallback": p.Fallback} {
		if strings.ContainsAny(strings.ReplaceAll(got, "\n> ", ""), "<>") {
			t.Errorf("%s has raw angle brackets: %q", name, got)
		}
		if !strings.Contains(got, esc) {
			t.Errorf("%s lacks the escaped text: %q", name, got)
		}
	}
	if want := "*" + esc + "*\n> " + esc + "\n" + esc; p.Section != want {
		t.Errorf("section %q, want %q", p.Section, want)
	}
}

// Plain @here, @channel, @everyone and #name carry no markup, so the
// formatter leaves them as written; the Slack client sends every text object
// verbatim (and parse=none), which is what keeps them from becoming a live
// mention or link (slackgo TestPostParams).
func TestFormatPostPlainMentionsPassThrough(t *testing.T) {
	plain := "@here @channel @everyone #general"
	p := FormatPost(bus.Message{Seq: 1, Channel: "c", Sender: "a", Subject: plain, Content: plain}, "", 3000, plain)
	for name, got := range map[string]string{"section": p.Section, "fallback": p.Fallback} {
		if !strings.Contains(got, plain) {
			t.Errorf("%s changed plain mention text: %q", name, got)
		}
		if strings.ContainsAny(strings.ReplaceAll(got, "\n> ", ""), "<>") {
			t.Errorf("%s has angle brackets: %q", name, got)
		}
	}
}

// Channel mentions typed by an agent never become a live channel link.
func TestFormatPostNoChannelLink(t *testing.T) {
	in := "#general <#C123|general> <#C123>"
	p := FormatPost(bus.Message{Seq: 1, Channel: "c", Sender: "a", Subject: in, Content: in}, "", 3000, in)
	for name, got := range map[string]string{"section": p.Section, "fallback": p.Fallback} {
		if strings.ContainsAny(strings.ReplaceAll(got, "\n> ", ""), "<>") || strings.Contains(got, "<#") {
			t.Errorf("%s has a live channel link: %q", name, got)
		}
		if !strings.Contains(got, "#general") || !strings.Contains(got, "&lt;#C123|general&gt; &lt;#C123&gt;") {
			t.Errorf("%s lacks the escaped text: %q", name, got)
		}
	}
}

func TestFormatPostLinkSpecials(t *testing.T) {
	for _, in := range []string{"[x](!channel)", "[x](@U123)", "[](!subteam^S1)", "[x](!here)", "[x](#C1)", "[](!channel)"} {
		p := FormatPost(bus.Message{Seq: 1, Channel: "c", Sender: "a", Subject: in, Content: in}, "", 3000, in)
		for name, got := range map[string]string{"section": p.Section, "fallback": p.Fallback} {
			if strings.ContainsAny(strings.ReplaceAll(got, "\n> ", ""), "<>") {
				t.Errorf("%q %s: live markup %q", in, name, got)
			}
		}
	}
}

// A long channel name makes the note nearly as long as the limit; the post
// then uses a shorter note so some of the message still shows.
func TestFormatPostLongNoteLeavesContent(t *testing.T) {
	long := strings.Repeat("c", 150)
	for _, maxChars := range []int{200, 250, 300} {
		p := FormatPost(bus.Message{Seq: 9, Channel: long, Sender: "a", Content: strings.Repeat("word ", 400)}, "", maxChars, "")
		if runes(p.Section) > maxChars {
			t.Fatalf("max %d: %d runes", maxChars, runes(p.Section))
		}
		if !strings.Contains(p.Section, "seq 9") || !strings.Contains(p.Section, "truncated") {
			t.Errorf("max %d: note lost: %q", maxChars, p.Section)
		}
		if n := strings.Count(p.Section, "word"); n < 5 {
			t.Errorf("max %d: only %d words of content kept: %q", maxChars, n, p.Section)
		}
	}
	// A short channel name keeps the full note.
	p := FormatPost(bus.Message{Seq: 9, Channel: "chan", Sender: "a", Content: strings.Repeat("word ", 400)}, "", 200, "")
	if !strings.HasSuffix(p.Section, "(truncated; the full message is seq 9 on agentbus2 channel chan)") {
		t.Errorf("full note dropped: %q", p.Section)
	}
}
