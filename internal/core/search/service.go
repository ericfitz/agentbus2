// Package search is the search aggregate of the core: the text, semantic and
// combined modes over the readable channels, scored in Go (Decision 51),
// with the memory-access touch inside Search (Decision 49). Ported from v1
// internal/bus/search.go.
package search

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	query "github.com/ericfitz/agentbus2/internal/search"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Service is the search aggregate.
type Service struct{ *core.Core }

// Search ranks the live messages the identity may read.
//
// Everything that can be refused is refused before any transaction, and the
// query embedding (an HTTP call) runs before the transaction too (Decision
// 26: fn may run more than once and has no side effects). The single
// Store.Tx that follows is a write transaction because the page's memories
// are touched.
func (s Service) Search(ctx context.Context, identity string, in bus.SearchInput) (bus.SearchResult, error) {
	p, err := prepare(in, s.Embedder() != nil)
	if err != nil {
		return bus.SearchResult{}, err
	}
	res := bus.SearchResult{}
	var qvec []float32
	wantSemantic := p.mode == "semantic" || p.mode == "both"
	if wantSemantic {
		qvec, err = s.embedQuery(ctx, p.semanticText())
		if err != nil {
			if !errors.Is(err, errNoEmbedder) {
				s.Log.Warn("semantic search unavailable", "err", err)
			}
			res.SemanticUnavailable = true
			qvec = nil
		}
	}
	useSemantic := wantSemantic && qvec != nil

	var out bus.SearchResult
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		out = bus.SearchResult{SemanticUnavailable: res.SemanticUnavailable}
		if _, err := s.RequireIdentity(ctx, tx, identity); err != nil {
			return err
		}
		var channelIDs []int64
		if p.channel != "" {
			ch, err := s.ChannelByName(ctx, tx, p.channel)
			if err != nil {
				return err
			}
			if err := s.Authorize(ctx, tx, identity, ch.ID, store.PermRead); err != nil {
				return err
			}
			channelIDs = []int64{ch.ID}
		} else {
			ids, err := s.ReadableChannelIDs(ctx, tx, identity)
			if err != nil {
				return err
			}
			channelIDs = ids
		}

		var ranked []scored
		var err error
		switch {
		case useSemantic && p.mode == "semantic":
			ranked, err = s.semanticRank(ctx, tx, qvec, channelIDs, p.filter)
		case useSemantic: // both
			var text, sem []scored
			if text, err = s.textRank(ctx, tx, p.query, channelIDs, p.filter); err == nil {
				if sem, err = s.semanticRank(ctx, tx, qvec, channelIDs, p.filter); err == nil {
					ranked = rules.RRF(text, sem,
						func(h scored) int64 { return h.msg.Seq },
						func(h *scored, v float64) { h.score = v })
				}
			}
		default: // text, or a semantic mode that fell back
			ranked, err = s.textRank(ctx, tx, p.query, channelIDs, p.filter)
		}
		if err != nil {
			return err
		}
		return s.page(ctx, tx, p, ranked, &out)
	})
	if err != nil {
		return bus.SearchResult{}, err
	}
	return out, nil
}

// embedQuery embeds the query under EmbeddingQueryTimeoutSeconds. A missing
// embedder is an error here; the caller turns every failure into
// SemanticUnavailable and falls back to text.
func (s Service) embedQuery(ctx context.Context, text string) ([]float32, error) {
	emb := s.Embedder()
	if emb == nil {
		return nil, errNoEmbedder
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.Cfg.EmbeddingQueryTimeoutSeconds*float64(time.Second)))
	defer cancel()
	vecs, err := emb.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return nil, errBadEmbedding
	}
	v := append([]float32(nil), vecs[0]...)
	rules.Normalize(v)
	return v, nil
}

// prepared is a validated SearchInput.
type prepared struct {
	query   query.Query
	mode    string
	channel string
	filter  store.SearchFilter
	count   int
	offset  int
}

// semanticText is what the semantic modes embed: the query's terms without
// their trailing stars, joined by single spaces (v1 ADR 0019).
func (p prepared) semanticText() string {
	words := make([]string, 0, len(p.query.Terms))
	for _, t := range p.query.Terms {
		words = append(words, t.Text)
	}
	return strings.Join(words, " ")
}

// prepare validates in without touching the Store.
func prepare(in bus.SearchInput, haveEmbedder bool) (prepared, error) {
	if strings.TrimSpace(in.Query) == "" {
		return prepared{}, core.E(bus.CodeValidation, "query is required")
	}
	q := query.Parse(in.Query)
	if q.Empty() {
		return prepared{}, core.E(bus.CodeValidation, "query must contain a word; * alone matches nothing")
	}
	p := prepared{query: q, channel: in.Channel, count: in.Count}
	switch in.Mode {
	case "":
		p.mode = "text"
		if haveEmbedder {
			p.mode = "both"
		}
	case "text", "semantic", "both":
		p.mode = in.Mode
	default:
		return prepared{}, core.E(bus.CodeValidation, "mode must be text, semantic, or both")
	}
	if in.Count < 0 {
		return prepared{}, core.E(bus.CodeValidation, "count must not be negative")
	}
	if p.count == 0 {
		p.count = rules.SearchPageDefault
	}
	p.count = min(p.count, rules.SearchPageMax)
	if in.Cursor != "" {
		o, err := strconv.Atoi(in.Cursor)
		if err != nil || o < 0 {
			return prepared{}, core.E(bus.CodeValidation, "invalid cursor %q", in.Cursor)
		}
		p.offset = o
	}
	pats, err := rules.NormalizeTagPatterns(in.Tags)
	if err != nil {
		return prepared{}, err
	}
	p.filter = store.SearchFilter{Sender: in.Sender, Since: in.Since, Until: in.Until, Thread: in.Thread}
	for _, pat := range pats {
		p.filter.Patterns = append(p.filter.Patterns, store.NewTagPattern(pat))
	}
	return p, nil
}
