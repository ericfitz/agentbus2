// Package rules holds the pure rule ports of the core: tag normalization and
// matching, subjects, sibling ranks, vectors, result trimming, request
// fingerprints and rank fusion. It imports only internal/bus (for the error
// type and Message), never the store, so every function is testable without a
// database. Ported from v1 internal/bus (tags.go, messages.go, rank.go,
// embed.go, receipts.go, search.go).
package rules
