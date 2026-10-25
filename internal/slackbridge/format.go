// Package slackbridge holds the Slack bridge: pure formatting rules (this
// file, mrkdwn.go, permalink.go) and, in later tasks, the runner. It never
// imports internal/server, internal/gen, the slack-go adapter or slack-go.
package slackbridge

import (
	"fmt"
	"hash/fnv"
	"strings"
	"unicode/utf8"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/slackapi"
)

// SlackChannelName derives the Slack channel name for a bus channel (spec
// 6.1 step 1): lowercase, every character outside a-z, 0-9, "-" and "_"
// becomes "-", cut to 80 bytes.
func SlackChannelName(busName string) (string, error) {
	var b strings.Builder
	for _, r := range strings.ToLower(busName) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := b.String()
	if len(name) > 80 {
		name = name[:80]
	}
	if strings.Trim(name, "-") == "" {
		return "", fmt.Errorf("channel %q has no usable Slack name; pass -slack-name", busName)
	}
	return name, nil
}

// ShortName is the Slack username for an identity: the identity without the
// prefix scope + "-" when it starts with it and something remains (spec 6.2).
func ShortName(identity, scope string) string {
	if scope == "" {
		return identity
	}
	if rest, ok := strings.CutPrefix(identity, scope+"-"); ok && rest != "" {
		return rest
	}
	return identity
}

// iconEmojis is the fixed list IconEmoji picks from (a builder's choice).
var iconEmojis = []string{
	":robot_face:", ":gear:", ":hammer_and_wrench:", ":mag:",
	":memo:", ":satellite:", ":test_tube:", ":jigsaw:",
}

// IconEmoji picks a stable emoji for an identity (FNV-32a over the list).
func IconEmoji(identity string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(identity))
	return iconEmojis[int(h.Sum32()%uint32(len(iconEmojis)))]
}

// scanState reports, for the prefix r[:c], whether a fenced block is open,
// the index of an unterminated "<" link start (-1 for none), the index of a
// partial fence marker ending the prefix (-1 for none) and the index of the
// backtick opening an inline code span that the cut leaves open (-1 for
// none). A "<" inside a fenced block is code, not a link.
func scanState(r []rune, c int) (inFence bool, linkStart, partial, codeStart int) {
	linkStart, partial, codeStart = -1, -1, -1
	// cutMid: the cut falls inside a line, so a span open at its end may close
	// after the cut.
	cutMid := c < len(r) && r[c] != '\n'
	for ls := 0; ls < c; {
		le := ls
		for le < c && r[le] != '\n' {
			le++
		}
		// r[ls:le] is the line, cut at c.
		p := ls
		for p < le && (r[p] == ' ' || r[p] == '\t') {
			p++
		}
		ticks := 0
		for p+ticks < le && r[p+ticks] == '`' && ticks < 3 {
			ticks++
		}
		switch {
		case ticks == 3:
			inFence = !inFence
		case ticks > 0 && p+ticks == le && le == c:
			partial = ls
		case !inFence:
			for i := ls; i < le; i++ {
				switch r[i] {
				case '<':
					if i+1 < len(r) && r[i+1] != ' ' && r[i+1] != '\n' {
						linkStart = i
					}
				case '>':
					linkStart = -1
				}
			}
			if le == c && cutMid {
				end := ls
				for end < len(r) && r[end] != '\n' {
					end++
				}
				codeStart = openCodeSpan(r[ls:end], le-ls)
				if codeStart >= 0 {
					codeStart += ls
				}
			}
		}
		ls = le + 1
	}
	return inFence, linkStart, partial, codeStart
}

// openCodeSpan returns the index in line of the backtick that opens a span the
// cut at index cut leaves open (its closing backtick lies at or after cut), or
// -1. line is the whole line. It mirrors inlineToMrkdwn: a span runs from a
// backtick to the next one with at least one rune between; a backtick with no
// closer is literal and opens nothing.
func openCodeSpan(line []rune, cut int) int {
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		j := i + 1
		for j < len(line) && line[j] != '`' {
			j++
		}
		switch {
		case j >= len(line):
			return -1
		case j == i+1:
			i++ // "``" is no span; the second backtick may open one
		case i < cut && j >= cut:
			return i
		default:
			i = j + 1
		}
	}
	return -1
}

// Truncate shortens s to at most max runes including "\n" + note. A string of
// max runes or fewer is returned unchanged. The cut counts runes, never lands
// inside a "<url|text>" link or an entity, and closes a fenced block it
// leaves open; an inline code span the cut leaves open is closed. A note too
// long to fit is itself cut to max runes.
func Truncate(s string, max int, note string) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	n := []rune(note)
	budget := max - 1 - len(n)
	if budget <= 0 {
		if len(n) > max {
			n = n[:max]
		}
		return string(n)
	}
	const closer = "\n```"
	r := []rune(s)
	c := budget
	inFence, inCode := false, false
	for c > 0 {
		var linkStart, partial, codeStart int
		inFence, linkStart, partial, codeStart = scanState(r, c)
		inCode = false
		if partial >= 0 {
			c = partial
			continue
		}
		if linkStart >= 0 {
			c = linkStart
			continue
		}
		if i := entityStart(r, c); i >= 0 {
			c = i
			continue
		}
		if inFence && c+len(closer) > budget {
			c = budget - len(closer)
			continue
		}
		if codeStart >= 0 {
			if c+1 > budget {
				c = budget - 1 // room for the closing backtick
				continue
			}
			inCode = true
		}
		break
	}
	// A cut right after a quote prefix would leave a bare "> " line.
	if !inFence {
		ls := c
		for ls > 0 && r[ls-1] != '\n' {
			ls--
		}
		if strings.TrimSpace(string(r[ls:c])) == ">" {
			c = ls - 1
			if c < 0 {
				c = 0
			}
			inCode = false
		}
	}
	if c <= 0 {
		return note
	}
	out := string(r[:c])
	if inFence {
		out += closer
	}
	if inCode {
		out += "`"
	}
	return out + "\n" + note
}

// entityStart returns the index of an "&" that starts an unterminated entity
// ending at c, or -1.
func entityStart(r []rune, c int) int {
	for i := c - 1; i >= 0 && i >= c-8; i-- {
		switch r[i] {
		case ';', ' ', '\n', '\t':
			return -1
		case '&':
			return i
		}
	}
	return -1
}

const quoteMaxRunes = 200

// minContentRunes is the room for message text a truncation note must leave.
const minContentRunes = 40

// truncationNote is the note that ends a truncated post: the full text, or,
// when a long channel name would leave under minContentRunes for the message,
// a shorter one that drops the channel (then the wording).
func truncationNote(m bus.Message, maxChars int) string {
	notes := []string{
		fmt.Sprintf("(truncated; the full message is seq %d on agentbus2 channel %s)", m.Seq, m.Channel),
		fmt.Sprintf("(truncated; the full message is seq %d)", m.Seq),
		fmt.Sprintf("(truncated; seq %d)", m.Seq),
	}
	for _, n := range notes {
		if maxChars-1-utf8.RuneCountInString(n) >= minContentRunes {
			return n
		}
	}
	return notes[len(notes)-1]
}

// FormatPost builds the Slack post for a bus message (spec 6.2). maxChars is
// slack_max_message_chars. parentQuote, when set, is the text of a parent that
// has no Slack mapping, posted as a quote line. Channel and ThreadTS are the
// caller's.
func FormatPost(m bus.Message, scope string, maxChars int, parentQuote string) slackapi.PostInput {
	var parts []string
	if m.Subject != "" {
		parts = append(parts, "*"+escapeSlack(m.Subject)+"*")
	}
	if parentQuote != "" {
		q := []rune(parentQuote)
		suffix := ""
		if len(q) > quoteMaxRunes {
			q, suffix = q[:quoteMaxRunes], "…"
		}
		lines := strings.Split(escapeSlack(string(q))+suffix, "\n")
		for i, l := range lines {
			lines[i] = "> " + l
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if m.Content != "" {
		parts = append(parts, MarkdownToMrkdwn(m.Content))
	}
	note := truncationNote(m, maxChars)
	var ctx []string
	for _, t := range m.Tags {
		ctx = append(ctx, "`"+t+"`")
	}
	plain := m.Content
	if m.Subject != "" {
		plain = m.Subject + ": " + m.Content
	}
	plain = strings.Join(strings.Fields(plain), " ")
	if f := []rune(plain); len(f) > 150 {
		plain = string(f[:150])
	}
	plain = escapeSlack(plain)
	return slackapi.PostInput{
		Section:   Truncate(strings.Join(parts, "\n"), maxChars, note),
		Context:   ctx,
		Fallback:  plain,
		Username:  ShortName(m.Sender, scope),
		IconEmoji: IconEmoji(m.Sender),
		Seq:       m.Seq,
	}
}
