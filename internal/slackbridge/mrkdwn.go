package slackbridge

import (
	"regexp"
	"strings"
	"unicode"
)

// escapeSlack escapes the three characters Slack treats as markup.
func escapeSlack(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

var (
	headingRE = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	listRE    = regexp.MustCompile(`^(\s*)[-*]\s+`)
)

// isFenceLine reports whether the line opens or closes a fenced code block.
func isFenceLine(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), "```")
}

// MarkdownToMrkdwn converts agent Markdown to Slack mrkdwn (spec 6.2).
// "&", "<" and ">" are escaped everywhere except in the links this function
// builds, code included, so agent text can never become a Slack mention or
// link (<!channel>, <@U123>, <url|text> arrive literally); an unclosed emphasis marker stays literal.
func MarkdownToMrkdwn(md string) string {
	lines := strings.Split(md, "\n")
	inFence := false
	for i, line := range lines {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			lines[i] = escapeSlack(line)
			continue
		}
		if m := headingRE.FindStringSubmatch(line); m != nil {
			h := strings.NewReplacer("**", "", "__", "").Replace(m[1])
			lines[i] = "*" + inlineToMrkdwn(h) + "*"
			continue
		}
		prefix := ""
		if m := listRE.FindStringSubmatch(line); m != nil {
			prefix = m[1] + "• "
			line = line[len(m[0]):]
		}
		lines[i] = prefix + inlineToMrkdwn(line)
	}
	return strings.Join(lines, "\n")
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// inlineToMrkdwn converts one line (or a span of it) of Markdown.
func inlineToMrkdwn(s string) string {
	r := []rune(s)
	var b strings.Builder
	// find returns the index of the first occurrence of marker in r[from:], or -1.
	find := func(marker string, from int) int {
		m := []rune(marker)
	outer:
		for i := from; i+len(m) <= len(r); i++ {
			for j := range m {
				if r[i+j] != m[j] {
					continue outer
				}
			}
			return i
		}
		return -1
	}
	for i := 0; i < len(r); {
		c := r[i]
		switch c {
		case '`':
			if end := find("`", i+1); end > i+1 {
				b.WriteString(escapeSlack(string(r[i : end+1])))
				i = end + 1
				continue
			}
		case '*', '_', '~':
			// double marker: bold (**, __) or strike (~~)
			if i+1 < len(r) && r[i+1] == c {
				marker := string([]rune{c, c})
				if end := find(marker, i+2); end > i+2 {
					out := "*"
					if c == '~' {
						out = "~"
					}
					b.WriteString(out + inlineToMrkdwn(string(r[i+2:end])) + out)
					i = end + 2
					continue
				}
			} else if c != '~' && i+1 < len(r) && r[i+1] != ' ' && r[i+1] != '\t' {
				if c == '_' && i > 0 && isWordRune(r[i-1]) {
					break // snake_case, not emphasis
				}
				end := find(string(c), i+1)
				for end > i+1 && end+1 < len(r) && r[end+1] == c {
					end = find(string(c), end+2) // skip a doubled marker inside
				}
				if end > i+1 && r[end-1] != ' ' && r[end-1] != '\t' &&
					(c == '*' || end+1 == len(r) || !isWordRune(r[end+1])) {
					b.WriteString("_" + inlineToMrkdwn(string(r[i+1:end])) + "_")
					i = end + 1
					continue
				}
			}
		case '[':
			if text, url, next, ok := parseLink(r, i); ok {
				b.WriteString("<" + escapeSlack(strings.ReplaceAll(url, "|", "%7C")) + "|" + inlineToMrkdwn(text) + ">")
				i = next
				continue
			}
		}
		switch c {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(c)
		}
		i++
	}
	return b.String()
}

// parseLink parses "[text](url)" starting at r[i] == '['. next is the index
// after the closing parenthesis.
func parseLink(r []rune, i int) (text, url string, next int, ok bool) {
	depth := 0
	j := i
	for ; j < len(r); j++ {
		if r[j] == '[' {
			depth++
		} else if r[j] == ']' {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	if j >= len(r) || j+1 >= len(r) || r[j+1] != '(' {
		return "", "", 0, false
	}
	k := j + 2
	for ; k < len(r) && r[k] != ')'; k++ {
		if unicode.IsSpace(r[k]) {
			return "", "", 0, false
		}
	}
	if k >= len(r) || k == j+2 {
		return "", "", 0, false
	}
	return string(r[i+1 : j]), string(r[j+2 : k]), k + 1, true
}

var slackTagRE = regexp.MustCompile(`<([^<>]*)>`)

var unescapeSlack = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

// SlackToMarkdown converts Slack message text to Markdown for the bus: links,
// user mentions and the three entities (spec 6.3). name resolves a Slack user
// ID to a display name; a nil name, or one that returns "", leaves the ID.
func SlackToMarkdown(text string, name func(userID string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range slackTagRE.FindAllStringSubmatchIndex(text, -1) {
		b.WriteString(unescapeSlack.Replace(text[last:m[0]]))
		b.WriteString(slackTag(text[m[2]:m[3]], name))
		last = m[1]
	}
	b.WriteString(unescapeSlack.Replace(text[last:]))
	return b.String()
}

func slackTag(inner string, name func(string) string) string {
	target, label, hasLabel := strings.Cut(inner, "|")
	switch {
	case strings.HasPrefix(target, "@"):
		id := target[1:]
		if name != nil {
			if n := name(id); n != "" {
				return "@" + n
			}
		}
		return "@" + id
	case strings.HasPrefix(target, "#"):
		if hasLabel && label != "" {
			return "#" + unescapeSlack.Replace(label)
		}
		return "#" + target[1:]
	case strings.HasPrefix(target, "!"):
		return "@" + strings.TrimPrefix(target, "!")
	}
	url := unescapeSlack.Replace(target)
	if hasLabel && label != "" {
		return "[" + unescapeSlack.Replace(label) + "](" + url + ")"
	}
	return url
}
