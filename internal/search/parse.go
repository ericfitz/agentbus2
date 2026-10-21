package search

import (
	"strings"
	"unicode"
)

// Parse splits a query into terms (Decision 51). Words are separated by
// whitespace; a word ending in one or more stars is a prefix of the word
// without them. A double-quoted run is one phrase term whose inner words are
// kept in order; stars after the closing quote are dropped, and an
// unterminated quote runs to the end. A quote inside a word is part of the
// word. A term with no letter or digit (`!!!`, `*`, `""`) is dropped, so a
// query can parse to no terms; callers reject that. Ported from v1
// internal/bus/search.go searchTerms.
func Parse(q string) Query {
	var terms []Term
	rs := []rune(q)
	i := 0
	for i < len(rs) {
		if unicode.IsSpace(rs[i]) {
			i++
			continue
		}
		if rs[i] == '"' {
			i++
			start := i
			for i < len(rs) && rs[i] != '"' {
				i++
			}
			inner := string(rs[start:i])
			if i < len(rs) {
				i++ // closing quote
				for i < len(rs) && rs[i] == '*' {
					i++
				}
			}
			if text := strings.Join(strings.Fields(inner), " "); hasAlnum(text) {
				terms = append(terms, Term{Text: text, Phrase: true})
			}
			continue
		}
		start := i
		for i < len(rs) && !unicode.IsSpace(rs[i]) {
			i++
		}
		w := string(rs[start:i])
		bare := strings.TrimRight(w, "*")
		if hasAlnum(bare) {
			terms = append(terms, Term{Text: bare, Prefix: len(bare) < len(w)})
		}
	}
	return Query{Terms: terms}
}

func hasAlnum(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
