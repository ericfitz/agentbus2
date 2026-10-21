package search

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	query "github.com/ericfitz/agentbus2/internal/search"
	"github.com/ericfitz/agentbus2/internal/store"
)

var (
	errNoEmbedder   = errors.New("no embedder configured")
	errBadEmbedding = errors.New("embedder returned no usable query vector")
)

// scored is a stored message with its rank score.
type scored struct {
	msg   store.Message
	score float64
}

// textRank is the text half: the Store's matches, newest first and at most
// SearchPageMax (the cap drops the oldest, never the newest), with score 0
// (the Store ranks no better than seq).
func (s Service) textRank(ctx context.Context, tx store.Tx, q query.Query, channelIDs []int64, f store.SearchFilter) ([]scored, error) {
	msgs, err := tx.SearchText(ctx, q, channelIDs, f, rules.SearchPageMax)
	if err != nil {
		return nil, core.FromStore(err)
	}
	out := make([]scored, len(msgs))
	for i, m := range msgs {
		out[i] = scored{msg: m}
	}
	return out, nil
}

// semanticRank scores every embedded live memory in scope by dot product
// against the normalized query vector (Decision 51), best first, ties by seq
// ascending, at most SearchPageMax.
func (s Service) semanticRank(ctx context.Context, tx store.Tx, qvec []float32, channelIDs []int64, f store.SearchFilter) ([]scored, error) {
	embs, err := tx.Embeddings(ctx, s.Cfg.EmbeddingModel, channelIDs, f)
	if err != nil {
		return nil, core.FromStore(err)
	}
	type cand struct {
		seq   int64
		score float64
	}
	cands := make([]cand, 0, len(embs))
	for _, e := range embs {
		cands = append(cands, cand{e.Seq, rules.Dot(qvec, rules.DecodeVec(e.Vector))})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score == cands[j].score {
			return cands[i].seq < cands[j].seq
		}
		return cands[i].score > cands[j].score
	})
	if len(cands) > rules.SearchPageMax {
		cands = cands[:rules.SearchPageMax]
	}
	seqs := make([]int64, len(cands))
	for i, c := range cands {
		seqs[i] = c.seq
	}
	msgs, err := tx.MessagesBySeqs(ctx, seqs)
	if err != nil {
		return nil, core.FromStore(err)
	}
	bySeq := make(map[int64]store.Message, len(msgs))
	for _, m := range msgs {
		bySeq[m.Seq] = m
	}
	out := make([]scored, 0, len(cands))
	for _, c := range cands {
		if m, ok := bySeq[c.seq]; ok { // gone between the two reads: skipped
			out = append(out, scored{msg: m, score: c.score})
		}
	}
	return out, nil
}

// page cuts the ranked list at the cursor and count, converts it, trims it
// to the result byte limit with the exact reserve (v1 C2), touches the
// memories of the hits that remain (Decision 49) and sets Next when more
// remain. It fills out.Hits and out.Next only.
func (s Service) page(ctx context.Context, tx store.Tx, p prepared, ranked []scored, out *bus.SearchResult) error {
	offset := min(p.offset, len(ranked))
	pg := ranked[offset:]
	if len(pg) > p.count {
		pg = pg[:p.count]
	}
	msgs := make([]store.Message, len(pg))
	for i, h := range pg {
		msgs[i] = h.msg
	}
	conv, err := s.ToBusMessages(ctx, tx, msgs)
	if err != nil {
		return err
	}
	hits := make([]bus.SearchHit, len(pg))
	for i := range pg {
		hits[i] = bus.SearchHit{Message: conv[i], Score: pg[i].score}
	}
	// Next is unknown until the page is final; reserve a max-width
	// placeholder. SemanticUnavailable is a fixed-size bool.
	reserve := rules.MarshalLen(bus.SearchResult{Next: strings.Repeat("9", 20), SemanticUnavailable: out.SemanticUnavailable})
	limit := min(s.Cfg.ResultDefaultKiB*1024, rules.TrimHardCeilingBytes) - reserve
	if limit > 0 {
		hits = rules.TrimToBytes(hits, func(h bus.SearchHit) int { return rules.MarshalLen(h) }, limit)
	} else {
		hits = []bus.SearchHit{} // the reserve alone exceeds the limit: metadata only
	}
	// Tasks-kind memories have no access row (v1 ADR 0013), so they are not touched.
	kinds := map[int64]store.Kind{}
	storeBySeq := make(map[int64]store.Message, len(pg))
	for _, h := range pg {
		storeBySeq[h.msg.Seq] = h.msg
	}
	var ids []int64
	for _, h := range hits {
		if h.MemoryID == nil {
			continue
		}
		cid := storeBySeq[h.Seq].ChannelID
		k, ok := kinds[cid]
		if !ok {
			ch, err := tx.Channel(ctx, cid)
			if err != nil {
				return core.FromStore(err)
			}
			k = ch.Kind
			kinds[cid] = k
		}
		if k != store.KindTasks && !slices.Contains(ids, *h.MemoryID) {
			ids = append(ids, *h.MemoryID)
		}
	}
	if len(ids) > 0 {
		if err := tx.TouchMemoryAccess(ctx, ids, s.NowMs()); err != nil {
			return core.FromStore(err)
		}
	}
	out.Hits = hits
	if offset+len(hits) < len(ranked) {
		out.Next = strconv.Itoa(offset + len(hits))
	}
	return nil
}
