package slackbridge

import "regexp"

// Permalink names a Slack message by channel ID and timestamp.
type Permalink struct{ Channel, TS string }

var permalinkRE = regexp.MustCompile(`https://[A-Za-z0-9.-]+\.slack\.com/archives/([CG][A-Z0-9]+)/p(\d+)`)

// FindPermalinks returns the message permalinks in raw Slack text, in order.
// Both the bare and the "<url|text>" forms match; a query string is ignored,
// and a "p" segment that is not exactly 16 digits is not a permalink.
func FindPermalinks(text string) []Permalink {
	var out []Permalink
	for _, m := range permalinkRE.FindAllStringSubmatch(text, -1) {
		d := m[2]
		if len(d) != 16 {
			continue
		}
		out = append(out, Permalink{Channel: m[1], TS: d[:10] + "." + d[10:]})
	}
	return out
}
