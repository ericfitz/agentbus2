package slackbridge

import "testing"

func TestMarkdownToMrkdwn(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"bold stars", "a **b** c", "a *b* c"},
		{"bold underscores", "a __b__ c", "a *b* c"},
		{"italic star", "a *b* c", "a _b_ c"},
		{"italic underscore", "a _b_ c", "a _b_ c"},
		{"strike", "~~gone~~", "~gone~"},
		{"link", "see [t](https://x.y/z?a=1&b=2)", "see <https://x.y/z?a=1&amp;b=2|t>"},
		{"link inside bold", "**[t](https://x.y)**", "*<https://x.y|t>*"},
		{"bold inside link text", "[**t**](https://x.y)", "<https://x.y|*t*>"},
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
		{"mention in link text and url", "[<!here>](<@U1>)", "<&lt;@U1&gt;|&lt;!here&gt;>"},
		{"pipe in url", "[t](https://x/a|b)", "<https://x/a%7Cb|t>"},
		{"mention in heading and list", "# <!here>\n- <@U1>", "*&lt;!here&gt;*\n• &lt;@U1&gt;"},
		{"link needs url", "[t] (x)", "[t] (x)"},
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
		{"special", "<!here> now", "@here now"},
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
