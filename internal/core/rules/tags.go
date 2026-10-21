package rules

import (
	"regexp"
	"slices"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// MaxTags is the per-message (and per-subscription) tag ceiling (v1 ADR 0009).
const MaxTags = 10

// MaxTagLen caps a stored tag, and a pattern's prefix, at 32 characters in
// total. The parts around the colon have no limit of their own.
const MaxTagLen = 32

// tagRe is the stored-tag rule: a-z, 0-9 and -, with at most one colon
// between other characters. Length is checked apart from the regexp so one
// message can name both limits.
var tagRe = regexp.MustCompile(`^[a-z0-9-]+(:[a-z0-9-]+)?$`)

// tagRuleText is the rule as NormalizeTags states it to the caller.
const tagRuleText = "tag %q must be 1-32 characters of a-z, 0-9 and -, with at most one : between other characters"

// validTag reports whether t (already lowercased) is a storable tag.
func validTag(t string) bool { return len(t) <= MaxTagLen && tagRe.MatchString(t) }

// normalizeTagList is the shared body of NormalizeTags and the pattern
// normalizer: lowercase, reject any entry valid rejects (one bad entry fails
// the whole call; badFmt takes the lowercased entry as its one %q), drop
// duplicates, cap at MaxTags (capFmt takes MaxTags as its one %d), sort.
// nil in, nil out.
func normalizeTagList(tags []string, valid func(string) bool, badFmt, capFmt string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(t)
		if !valid(t) {
			return nil, bus.Errorf(bus.CodeValidation, false, badFmt, t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) > MaxTags {
		return nil, bus.Errorf(bus.CodeValidation, false, capFmt, MaxTags)
	}
	slices.Sort(out)
	return out, nil
}

// NormalizeTags lowercases tags, rejects any that fail the tag rule (one bad
// tag fails the whole call), drops duplicates, sorts, and caps the result at
// MaxTags. nil in, nil out. Filters take patterns: see NormalizeTagPatterns.
func NormalizeTags(tags []string) ([]string, error) {
	return normalizeTagList(tags, validTag, tagRuleText, "at most %d tags per message")
}

// tagPrefixRe is what may precede a pattern's trailing *: the start of some
// valid tag, so the colon may be last (env:*) but not first or doubled.
var tagPrefixRe = regexp.MustCompile(`^[a-z0-9-]+(:[a-z0-9-]*)?$`)

// NormalizeTagPatterns is NormalizeTags for filters (history, search, tag
// subscriptions): each entry is an exact tag, or a 1-32 character tag prefix
// followed by one *. Same lowercasing, deduplication, sort and cap. nil in,
// nil out.
func NormalizeTagPatterns(patterns []string) ([]string, error) {
	return normalizeTagList(patterns, validTagPattern,
		"tag pattern %q must be a tag, or a 1-32 character tag prefix followed by one *",
		"at most %d tag patterns")
}

func validTagPattern(p string) bool {
	prefix, ok := strings.CutSuffix(p, "*")
	if !ok {
		return validTag(p)
	}
	return len(prefix) <= MaxTagLen && tagPrefixRe.MatchString(prefix)
}

// tagRange maps a pattern to the inclusive byte range of the tags it
// matches: an exact tag to itself, a prefix p* to [p, p~]. Every legal tag
// character sorts below ~ (0x7e). It is the same range store.TagRange
// returns; the copy keeps this package free of the store.
func tagRange(pattern string) (lo, hi string) {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return p, p + "~"
	}
	return pattern, pattern
}

// MatchTag is the Go side of the range rule the store applies in SQL:
// env* matches env itself, environment and env:prod; env:* matches only
// env:<value>.
func MatchTag(pattern, tag string) bool {
	lo, hi := tagRange(pattern)
	return tag >= lo && tag <= hi
}

// MatchTags reports whether every pattern matches at least one of tags: the
// AND-set rule. No patterns matches everything.
func MatchTags(patterns, tags []string) bool {
	return !slices.ContainsFunc(patterns, func(p string) bool {
		return !slices.ContainsFunc(tags, func(t string) bool { return MatchTag(p, t) })
	})
}

// TagSet is one AND set of patterns: a message matches when every pattern
// matches one of its tags and it was written after the set was added.
type TagSet struct {
	Patterns   []string
	CreatedSeq int64
}

// MatchedTags is the union, sorted and deduplicated, of a message's own tags
// that satisfied a set the message matches (env:prod, never the pattern
// env:*). nil when no set matches.
func MatchedTags(sets []TagSet, seq int64, tags []string) []string {
	var out []string
	for _, s := range sets {
		if seq <= s.CreatedSeq || !MatchTags(s.Patterns, tags) {
			continue
		}
		for _, t := range tags {
			if slices.ContainsFunc(s.Patterns, func(p string) bool { return MatchTag(p, t) }) && !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	slices.Sort(out)
	return out
}

// TagsKey is the key a tag set is stored under: its sorted patterns joined by commas.
func TagsKey(patterns []string) string { return strings.Join(patterns, ",") }
