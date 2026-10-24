package embed

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// gateEmbedder blocks every Embed until release is closed and counts calls.
type gateEmbedder struct {
	calls   atomic.Int32
	release chan struct{}
}

func (g *gateEmbedder) Model() string { return "gate" }

func (g *gateEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	g.calls.Add(1)
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

func newCore(t *testing.T, emb core.Embedder) (*core.Core, store.Channel) {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "a.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cfg := core.Defaults()
	if emb != nil {
		cfg.EmbeddingModel = emb.Model()
	}
	c, err := core.New(s, cfg, core.Options{Embedder: emb})
	if err != nil {
		t.Fatal(err)
	}
	var ch store.Channel
	if err := s.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		ch, err = tx.CreateChannel(ctx, store.ChannelCreate{Name: "mem", Kind: store.KindMemory, CreatedAt: c.NowMs()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return c, ch
}

func addMemory(t *testing.T, c *core.Core, ch store.Channel, content string) {
	t.Helper()
	err := c.Store.Append(context.Background(), func(ctx context.Context, tx store.Tx) error {
		_, _, err := c.Append(ctx, tx, core.AppendInput{ChannelID: ch.ID, Channel: ch.Name, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, Content: content})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func embeddedCount(t *testing.T, c *core.Core, ch store.Channel) int {
	t.Helper()
	var n int
	err := c.Store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		es, err := tx.Embeddings(ctx, "gate", []int64{ch.ID}, store.SearchFilter{})
		n = len(es)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInstallRunsBatchOnce(t *testing.T) {
	g := &gateEmbedder{release: make(chan struct{})}
	c, ch := newCore(t, g)
	Install(c)
	if c.EmbedSoon == nil {
		t.Fatal("Install left EmbedSoon nil")
	}
	addMemory(t, c, ch, "one")

	c.EmbedSoon()
	waitFor(t, "the first pass to reach the embedder", func() bool { return g.calls.Load() == 1 })
	c.EmbedSoon() // collapses into the running pass
	time.Sleep(50 * time.Millisecond)
	if got := g.calls.Load(); got != 1 {
		t.Fatalf("calls = %d while a pass is running, want 1", got)
	}
	close(g.release)
	waitFor(t, "the first row to be embedded", func() bool { return embeddedCount(t, c, ch) == 1 })

	// The pass has returned its lock soon after the write; a new memory then
	// gets one more batch.
	addMemory(t, c, ch, "two")
	waitFor(t, "the second pass", func() bool {
		c.EmbedSoon()
		return embeddedCount(t, c, ch) == 2
	})
	if got := g.calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2 (one per memory batch)", got)
	}
}

func TestInstallWithoutEmbedderIsNoOp(t *testing.T) {
	c, _ := newCore(t, nil)
	Install(c)
	if c.EmbedSoon != nil {
		t.Fatal("Install set EmbedSoon with no embedder")
	}
}
