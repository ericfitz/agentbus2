package embed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

const (
	// BatchSize is how many memories one EmbedBatch embeds (v1 embedBatchSize).
	BatchSize = 64
	// RequestTimeout bounds each embeddings request in real time (v1
	// embedRequestTimeout).
	RequestTimeout = 30 * time.Second

	// noticeKind is the notice EmbedBatch sets on error and clears on success
	// (v1 ADR 0018).
	noticeKind = "embedding"
)

// EmbedBatch embeds up to BatchSize live memory revisions that lack an
// embedding (and a recorded failure) for the embedder's model, and returns
// how many rows it embedded or recorded as failed. Without an embedder it
// does nothing.
//
// The HTTP calls run outside any transaction (Decision 26), each under its
// own RequestTimeout; the rows are written in one Tx afterwards, where the
// Store skips rows that stopped being live memories meanwhile. When the
// endpoint rejects the batch's input (core.IsRowRejected) each row is
// retried alone, and a row rejected alone is recorded as a failure so one
// bad memory never blocks the rest (v1 ADR 0018). A rejection counts as the
// row's fault only once the endpoint has accepted some input under this
// model; an endpoint that rejects everything is a configuration error, so
// nothing is recorded and the pass fails.
//
// Second run: the rows are embedded, so it finds nothing and returns 0.
// Overlapping runs (two processes) may embed the same rows twice; the puts
// replace, so the result is identical. A run killed before the write leaves
// the rows for the next one. Rows of other models are deleted in the write
// Tx, which every pass with work to do ends with; stale rows only ever
// belong to live memories, which are candidates under the new model.
func EmbedBatch(ctx context.Context, c *core.Core) (n int, err error) {
	emb := c.Embedder()
	if emb == nil {
		return 0, nil
	}
	defer func() { noteEmbed(ctx, c, err) }()
	model := emb.Model()

	var cands []store.Message
	if err := c.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		cands, err = tx.UnembeddedMemories(ctx, model, BatchSize)
		return err
	}); err != nil {
		return 0, core.FromStore(err)
	}
	if len(cands) == 0 {
		return 0, nil
	}
	seqs := make([]int64, len(cands))
	texts := make([]string, len(cands))
	for i, m := range cands {
		seqs[i] = m.Seq
		texts[i] = rules.EmbedText(m.Subject, m.Content)
	}

	vecs, err := embedTimed(ctx, emb, texts)
	rejected := map[int]string{}
	var stopErr error // a non-row failure partway through the row retry
	if core.IsRowRejected(err) {
		firstReject := err
		proven, perr := hasEmbeddings(ctx, c, model)
		if perr != nil {
			return 0, perr
		}
		vecs = make([][]float32, len(texts))
		for i, text := range texts {
			v, err := embedTimed(ctx, emb, []string{text})
			switch {
			case core.IsRowRejected(err):
				rejected[i] = rejectReason(err)
			case err != nil:
				// Keep what this retry already did, so the next pass starts
				// after it rather than repeating it.
				stopErr = err
				seqs, texts = seqs[:i], texts[:i]
			default:
				vecs[i] = v[0]
				proven = true
			}
			if stopErr != nil {
				break
			}
		}
		switch {
		case stopErr != nil && !proven:
			return 0, stopErr
		case !proven:
			return 0, fmt.Errorf("the endpoint rejected every input, so this is likely its configuration (endpoint, model), not the memories: %w", firstReject)
		}
	} else if err != nil {
		return 0, err
	}
	if len(seqs) == 0 {
		return 0, stopErr
	}

	at := c.NowMs()
	err = c.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.DeleteStaleModel(ctx, model); err != nil {
			return err
		}
		for i, seq := range seqs {
			if reason, bad := rejected[i]; bad {
				if err := tx.PutEmbedFailure(ctx, store.EmbedFailure{Seq: seq, Model: model, Error: reason, FailedAt: at}); err != nil {
					return err
				}
				continue
			}
			if err := tx.PutEmbedding(ctx, store.Embedding{Seq: seq, Model: model, Vector: rules.EncodeVec(vecs[i])}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, core.FromStore(err)
	}
	return len(seqs), stopErr
}

// embedTimed is one embeddings request bounded by RequestTimeout. The vectors
// are copied and normalized (the core normalizes after Embed returns), and a
// reply of the wrong length is an error.
func embedTimed(ctx context.Context, emb core.Embedder, texts []string) ([][]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	raw, err := emb.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	if len(raw) != len(texts) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d inputs", len(raw), len(texts))
	}
	out := make([][]float32, len(raw))
	for i, v := range raw {
		if len(v) == 0 {
			return nil, fmt.Errorf("embedder returned an empty vector for input %d", i)
		}
		out[i] = append([]float32(nil), v...)
		rules.Normalize(out[i])
	}
	return out, nil
}

func rejectReason(err error) string {
	var rr *core.RowRejected
	if errors.As(err, &rr) {
		return rr.Reason
	}
	return err.Error()
}

// hasEmbeddings reports whether any embedding exists under model: the proof
// that the endpoint accepts input under it. Scanned channel by channel and
// stopped at the first hit; it only runs when a batch was rejected.
func hasEmbeddings(ctx context.Context, c *core.Core, model string) (bool, error) {
	var found bool
	err := c.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		found = false
		chs, err := tx.ListChannels(ctx)
		if err != nil {
			return err
		}
		for _, ch := range chs {
			es, err := tx.Embeddings(ctx, model, []int64{ch.ID}, store.SearchFilter{})
			if err != nil {
				return err
			}
			if len(es) > 0 {
				found = true
				return nil
			}
		}
		return nil
	})
	return found, core.FromStore(err)
}

// noteEmbed records the outcome of a pass in the "embedding" notice (v1 ADR
// 0018): an error replaces it, success clears it. A pass cut short by its
// caller's context says nothing either way.
func noteEmbed(ctx context.Context, c *core.Core, err error) {
	if ctx.Err() != nil {
		return
	}
	var dbErr error
	if err == nil {
		// Read first: an idle tick must not take a write lock (v1 9dfc490).
		var set bool
		dbErr = c.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			_, set, err = tx.Notice(ctx, noticeKind)
			return err
		})
		if dbErr == nil && set {
			dbErr = c.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error { return tx.ClearNotice(ctx, noticeKind) })
		}
	} else {
		// Name the process: with several, the one that failed (for example,
		// one started without the key) is the one to fix.
		msg := fmt.Sprintf("agentbus2 pid %d: %v", os.Getpid(), err)
		at := c.NowMs()
		dbErr = c.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			return tx.SetNotice(ctx, store.Notice{Kind: noticeKind, Message: msg, SetAt: at})
		})
	}
	if dbErr != nil {
		c.Log.Warn("record embedding status failed", "err", dbErr)
	}
}

// Install sets c.EmbedSoon to one best-effort background pass, so a new
// memory is searchable quickly (v1 embedSoon). Overlapping calls collapse
// into the running pass. It does nothing when c has no embedder.
func Install(c *core.Core) {
	if c.Embedder() == nil {
		return
	}
	var mu sync.Mutex
	c.EmbedSoon = func() {
		if !mu.TryLock() {
			return
		}
		go func() {
			defer mu.Unlock()
			// Each request has its own RequestTimeout; the pass as a whole may
			// make one per row when a batch is rejected.
			ctx, cancel := context.WithTimeout(context.Background(), (BatchSize+1)*RequestTimeout)
			defer cancel()
			if _, err := EmbedBatch(ctx, c); err != nil {
				c.Log.Warn("background embedding failed", "err", err)
			}
		}()
	}
}
