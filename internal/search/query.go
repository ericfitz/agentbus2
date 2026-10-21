// Package search holds the engine-neutral text search query (Decision 51).
// Parse arrives in Milestone 2 Task 10.
package search

// Term is one search term: a word, a prefix (trailing star) or a phrase.
type Term struct {
	Text           string
	Prefix, Phrase bool
}

// Query is the AND of its terms.
type Query struct{ Terms []Term }

// Empty reports whether the query has no terms.
func (q Query) Empty() bool { return len(q.Terms) == 0 }
