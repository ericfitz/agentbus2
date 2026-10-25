package slackbridge

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestMarkdownToMrkdwn(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"bold stars", "a **b** c", "a *b* c"},
		{"bold underscores", "a __b__ c", "a *b* c"},
		{"italic star", "a *b* c", "a _b_ c"},
		{"italic underscore", "a _b_ c", "a _b_ c"},
		{"strike", "~~gone~~", "~gone~"},
		{"link", "see [t](https://x.y/z?a=1&b=2)", "see <https://x.y/z?a=1&amp;b=2|t>"},
		{"link inside bold", "**[t](https://x.y)**", "*<https://x.y|t>*"},
		{"link text is plain", "[**t** <!here>](https://x.y)", "<https://x.y|**t** &lt;!here&gt;>"},
		{"nested link", "[a [b](http://z)](http://y)", "<http://y|a [b](http://z)>"},
		{"channel as url", "[x](!channel)", "[x](!channel)"},
		{"user as url", "[x](@U123)", "[x](@U123)"},
		{"subteam as url", "[](!subteam^S1)", "[](!subteam^S1)"},
		{"here as url", "[x](!here)", "[x](!here)"},
		{"channel ref as url", "[x](#C1)", "[x](#C1)"},
		{"empty text", "[](https://x.y)", "<https://x.y|>"},
		{"mixed case scheme", "[x](HTTPS://X.Y)", "<HTTPS://X.Y|x>"},
		{"mailto", "[m](mailto:a@b.c)", "<mailto:a@b.c|m>"},
		{"ftp refused", "[x](ftp://a)", "[x](ftp://a)"},
		{"bare scheme refused", "[x](http://)", "[x](http://)"},
		{"fence trailing mention", "```a```<!here>\n```", "```a```&lt;!here&gt;\n```"},
		{"fence leading mention", "``` <!channel>\nx\n```", "``` &lt;!channel&gt;\nx\n```"},
		{"heading keeps hash", "# Learn C#", "*Learn C#*"},
		{"heading closing hashes", "## Title ##", "*Title*"},
		{"h1", "# Title", "*Title*"},
		{"h3", "### Title **x**", "*Title x*"},
		{"dash list", "- a\n- b", "• a\n• b"},
		{"star list", "* a\n* b", "• a\n• b"},
		{"nested list", "- a\n  - b", "• a\n  • b"},
		{"escapes", "a < b & c > d", "a &lt; b &amp; c &gt; d"},
		{"inline code escaped only", "x `**a** <b>` y", "x `**a** &lt;b&gt;` y"},
		{"fence unchanged", "t\n```\n**a** <b> # c\n- d\n```\n**e**", "t\n```\n**a** &lt;b&gt; # c\n- d\n```\n*e*"},
		{"fence with lang", "```go\n# x\n```", "```go\n# x\n```"},
		{"unclosed fence stays literal", "```\n**a**", "```\n**a**"},
		{"snake_case untouched", "use snake_case_name here", "use snake_case_name here"},
		{"lone star", "2 * 3 * 4", "2 * 3 * 4"},
		{"unclosed bold", "a **b", "a **b"},
		{"multibyte", "**日本** é", "*日本* é"},
		{"empty", "", ""},
		{"channel mention", "hi <!channel> and <!here>", "hi &lt;!channel&gt; and &lt;!here&gt;"},
		{"user mention", "ping <@U123>", "ping &lt;@U123&gt;"},
		{"raw link", "<https://x|y>", "&lt;https://x|y&gt;"},
		{"mention in code", "`<!channel>`\n```\n<@U123>\n```", "`&lt;!channel&gt;`\n```\n&lt;@U123&gt;\n```"},
		{"mention in link text and url", "[<!here>](<@U1>)", "[&lt;!here&gt;](&lt;@U1&gt;)"},
		{"pipe in url", "[t](https://x/a|b)", "<https://x/a%7Cb|t>"},
		{"mention in heading and list", "# <!here>\n- <@U1>", "*&lt;!here&gt;*\n• &lt;@U1&gt;"},
		{"link needs url", "[t] (x)", "[t] (x)"},
		{"paren in url", "[w](https://en.wikipedia.org/wiki/Go_(language)) x", "<https://en.wikipedia.org/wiki/Go_(language)|w> x"},
		{"link inside parens", "(see [a](https://x.y))", "(see <https://x.y|a>)"},
		{"unbalanced paren in url", "[a](https://x.y/(z)", "<https://x.y/(z|a>"},
		{"mention in paren url", "[a](https://x.y/(<!here>))", "<https://x.y/(&lt;!here&gt;)|a>"},
		{"blockquote", "> a **b**\n> c", "> a *b*\n> c"},
		{"blockquote no space", ">x", "> x"},
		{"nested blockquote", "> > x\n>> y", "> x\n> y"},
		{"empty blockquote line", "a\n>\nb", "a\n> \nb"},
		{"blockquote mention", "> <!here> <@U1>", "> &lt;!here&gt; &lt;@U1&gt;"},
		{"blockquote list", "> - a", "> • a"},
		{"gt mid line", "a > b", "a &gt; b"},
		{"indented gt is not a quote", "    > x", "    &gt; x"},
		{"gt in fence", "```\n> x\n```", "```\n&gt; x\n```"},
	}
	for _, c := range cases {
		if got := MarkdownToMrkdwn(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSlackToMarkdown(t *testing.T) {
	name := func(id string) string {
		if id == "U1" {
			return "alice"
		}
		return id
	}
	cases := []struct{ name, in, want string }{
		{"link with text", "see <https://x.y/z|label>", "see [label](https://x.y/z)"},
		{"bare link", "see <https://x.y/z>", "see https://x.y/z"},
		{"mention", "hi <@U1>", "hi @alice"},
		{"mention with label", "hi <@U1|al>", "hi @alice"},
		{"unknown mention", "hi <@U9>", "hi @U9"},
		{"channel", "in <#C1|general>", "in #general"},
		{"channel without name", "in <#C15077GCW>", "in #channel"},
		{"channel empty label", "in <#C1|>", "in #channel"},
		{"private channel", "in <#G1|secret>", "in #secret"},
		{"private channel without name", "in <#G15077GCW>", "in #channel"},
		{"here", "<!here> now", "@here now"},
		{"channel broadcast", "<!channel> now", "@channel now"},
		{"everyone", "<!everyone> now", "@everyone now"},
		{"subteam without label", "<!subteam^S1> go", "@subteam^S1 go"},
		{"subteam label", "<!subteam^S1|@team> go", "@team go"},
		{"date label", "<!date^1700000000^{date}|Nov 14>", "Nov 14"},
		{"entities", "a &lt; b &amp;&amp; c &gt; d", "a < b && c > d"},
		{"double escaped stays single", "&amp;lt;", "&lt;"},
		{"entity in link text", "<https://x.y|a &amp; b>", "[a & b](https://x.y)"},
		{"mailto", "<mailto:a@b.c|a@b.c>", "[a@b.c](mailto:a@b.c)"},
		{"plain", "hello", "hello"},
		{"unterminated", "a <b", "a <b"},
	}
	for _, c := range cases {
		if got := SlackToMarkdown(c.in, name); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := SlackToMarkdown("<@U1>", nil); got != "@U1" {
		t.Errorf("nil name func: %q", got)
	}
}

// generatedLink matches the only angle-bracket form the formatter may emit.
var generatedLink = regexp.MustCompile(`(?i)<(?:https?://|mailto:)[^<>|\s]+\|[^<>]*>`)

func TestNoLiveMarkupFromAgentText(t *testing.T) {
	parts := []string{
		"<!channel>", "<!here>", "<@U123>", "<#C1>", "<!subteam^S1>", "<https://x|y>", "<",
		"[x](!channel)", "[x](@U1)", "[](#C1)", "[a](https://ok.example/p)", "[b [c](!here)](http://z)",
		"```", "```a```<!here>", "``` <!channel>", "`", "**", "*", "_", "__", "~~", "# ", "- ", "\n", " ",
		"é", "😀", "(", ")", "[", "]", "|", ">", "&",
	}
	seed := uint32(7)
	for n := range 20000 {
		var b strings.Builder
		for range 12 {
			seed = seed*1664525 + 1013904223
			b.WriteString(parts[int(seed>>16)%len(parts)])
		}
		in := b.String()
		for _, out := range []string{MarkdownToMrkdwn(in), FormatPost(bus.Message{Seq: 1, Channel: "c", Subject: in, Content: in}, "", 3000, in).Section} {
			rest := generatedLink.ReplaceAllString(out, "")
			rest = strings.ReplaceAll(rest, "\n> ", "\n")
			rest = strings.TrimPrefix(rest, "> ")
			if strings.ContainsAny(rest, "<>") {
				t.Fatalf("case %d: live markup in %q from %q", n, out, in)
			}
		}
	}
}
