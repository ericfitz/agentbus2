package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func wantCode(t *testing.T, err error, code bus.Code) {
	t.Helper()
	if got := bus.CodeOf(err); got != code {
		t.Fatalf("code = %q (err %v), want %q", got, err, code)
	}
}

func TestNormalizeTags(t *testing.T) {
	got, err := NormalizeTags([]string{"Release", "bug", "release", "env:prod", "a-1"})
	if err != nil || !slices.Equal(got, []string{"a-1", "bug", "env:prod", "release"}) {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := NormalizeTags(nil); err != nil || got != nil {
		t.Fatalf("no tags: %v %v", got, err)
	}
	long32 := strings.Repeat("a", 20) + ":" + strings.Repeat("b", 11)
	if got, err := NormalizeTags([]string{long32}); err != nil || len(got) != 1 || got[0] != long32 {
		t.Fatalf("32 characters: %v %v", got, err)
	}
	for _, bad := range [][]string{{""}, {"has space"}, {"x/y"}, {"ünïcode"}, {long32 + "c"}, {" bug "}, {"a_b"}, {":prod"}, {"env:"}, {"env::prod"}, {"a:b:c"}, {"env:*"}, {"*"}} {
		_, err := NormalizeTags(bad)
		wantCode(t, err, bus.CodeValidation)
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "t" + string(rune('a'+i))
	}
	_, err = NormalizeTags(eleven)
	wantCode(t, err, bus.CodeValidation)
	if _, err := NormalizeTags(append(eleven[:10], "TA")); err != nil {
		t.Fatalf("duplicates are removed before the limit applies: %v", err)
	}
}

func TestNormalizeTagPatterns(t *testing.T) {
	long32 := strings.Repeat("a", 32)
	got, err := NormalizeTagPatterns([]string{"Failed", "ENV:*", "env*", "env:pr*", "failed", long32 + "*", long32})
	want := []string{long32, long32 + "*", "env*", "env:*", "env:pr*", "failed"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("%v %v, want %v", got, err, want)
	}
	if got, err := NormalizeTagPatterns(nil); err != nil || got != nil {
		t.Fatalf("no patterns: %v %v", got, err)
	}
	for _, bad := range []string{"*", ":*", "e*v", "env**", "env_*", "*env", "env:*x", "", long32 + "b*", "a_b", "env::*"} {
		_, err := NormalizeTagPatterns([]string{bad})
		wantCode(t, err, bus.CodeValidation)
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "t" + string(rune('a'+i)) + "*"
	}
	_, err = NormalizeTagPatterns(eleven)
	wantCode(t, err, bus.CodeValidation)
	_, err = NormalizeTagPatterns([]string{"A_b*"})
	if err == nil || !strings.Contains(err.Error(), `tag pattern \"a_b*\" must be a tag, or a 1-32 character tag prefix followed by one *`) {
		t.Fatalf("pattern error text: %v", err)
	}
}

func TestMatchTag(t *testing.T) {
	cases := []struct {
		pattern, tag string
		want         bool
	}{
		{"env*", "env", true},
		{"env*", "environment", true},
		{"env*", "env:prod", true},
		{"env*", "env-", true},
		{"env*", "env9", true},
		{"env*", "envz", true},
		{"env*", "enw", false},
		{"env:*", "env", false},
		{"env:*", "env:prod", true},
		{"env:*", "env:9", true},
		{"env:*", "env:z-9", true},
		{"env:*", "envy", false},
		{"env:pr*", "env:prod", true},
		{"env:pr*", "env:staging", false},
		{"failed", "failed", true},
		{"failed", "failed-again", false},
		{"fail*", "failed", true},
		{"a*", "b", false},
	}
	for _, c := range cases {
		if got := MatchTag(c.pattern, c.tag); got != c.want {
			t.Errorf("MatchTag(%q, %q) = %v, want %v", c.pattern, c.tag, got, c.want)
		}
	}
	if lo, hi := tagRange("env:*"); lo != "env:" || hi != "env:~" {
		t.Fatalf("tagRange(env:*) = %q %q", lo, hi)
	}
	if lo, hi := tagRange("failed"); lo != "failed" || hi != "failed" {
		t.Fatalf("tagRange(failed) = %q %q", lo, hi)
	}
}

func TestMatchTags(t *testing.T) {
	cases := []struct {
		patterns, tags []string
		want           bool
	}{
		{[]string{"failed"}, []string{"failed", "env:prod"}, true},
		{[]string{"failed", "env:*"}, []string{"env:prod", "failed"}, true},
		{[]string{"failed", "env:*"}, []string{"env:prod"}, false},
		{[]string{"env:*", "env:prod"}, []string{"env:prod"}, true},
		{[]string{"env:*", "env:prod"}, []string{"env:staging"}, false},
		{[]string{"env:*"}, []string{"env"}, false},
		{[]string{"env*"}, nil, false},
		{nil, []string{"x"}, true},
	}
	for _, c := range cases {
		if got := MatchTags(c.patterns, c.tags); got != c.want {
			t.Errorf("MatchTags(%q, %q) = %v, want %v", c.patterns, c.tags, got, c.want)
		}
	}
}

func TestMatchedTags(t *testing.T) {
	sets := []TagSet{
		{Patterns: []string{"env:*", "failed"}, CreatedSeq: 5},
		{Patterns: []string{"urgent"}, CreatedSeq: 10},
	}
	// Matches the first set only (seq above its CreatedSeq).
	got := MatchedTags(sets, 7, []string{"env:prod", "failed", "urgent", "other"})
	if !slices.Equal(got, []string{"env:prod", "failed"}) {
		t.Fatalf("seq 7: %v", got)
	}
	// Both sets match; union is sorted and deduplicated.
	got = MatchedTags(sets, 11, []string{"urgent", "failed", "env:prod", "env:staging"})
	if !slices.Equal(got, []string{"env:prod", "env:staging", "failed", "urgent"}) {
		t.Fatalf("seq 11: %v", got)
	}
	// At or below CreatedSeq nothing matches.
	if got := MatchedTags(sets, 5, []string{"env:prod", "failed"}); got != nil {
		t.Fatalf("seq 5: %v", got)
	}
	// A set whose AND is unmet contributes nothing.
	if got := MatchedTags(sets, 8, []string{"env:prod"}); got != nil {
		t.Fatalf("unmet AND: %v", got)
	}
}

func TestTagsKey(t *testing.T) {
	if got := TagsKey([]string{"a", "b*"}); got != "a,b*" {
		t.Fatalf("TagsKey = %q", got)
	}
}
