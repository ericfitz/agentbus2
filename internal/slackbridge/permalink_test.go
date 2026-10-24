package slackbridge

import (
	"slices"
	"testing"
)

func TestFindPermalinks(t *testing.T) {
	cases := []struct {
		name, in string
		want     []Permalink
	}{
		{"plain", "see https://corp.slack.com/archives/C0123ABC/p1700000000123456 ok", []Permalink{{"C0123ABC", "1700000000.123456"}}},
		{"angle", "<https://corp.slack.com/archives/C0123ABC/p1700000000123456>", []Permalink{{"C0123ABC", "1700000000.123456"}}},
		{"angle with text", "<https://corp.slack.com/archives/G0123ABC/p1700000000123456|here>", []Permalink{{"G0123ABC", "1700000000.123456"}}},
		{"thread query", "https://corp.slack.com/archives/C0123ABC/p1700000000123456?thread_ts=1699999999.000100&cid=C0123ABC", []Permalink{{"C0123ABC", "1700000000.123456"}}},
		{"15 digits", "https://corp.slack.com/archives/C0123ABC/p170000000012345", nil},
		{"17 digits", "https://corp.slack.com/archives/C0123ABC/p17000000001234567", nil},
		{"not slack", "https://example.com/archives/C0123ABC/p1700000000123456", nil},
		{"dm channel ignored", "https://corp.slack.com/archives/D0123ABC/p1700000000123456", nil},
		{"two in order", "https://a.slack.com/archives/C1/p1700000000000001 then https://a.slack.com/archives/C2/p1700000001000002",
			[]Permalink{{"C1", "1700000000.000001"}, {"C2", "1700000001.000002"}}},
		{"none", "nothing here", nil},
	}
	for _, c := range cases {
		got := FindPermalinks(c.in)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
