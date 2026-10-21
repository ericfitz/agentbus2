// Package embed holds the HTTP embedder and the background embedding batches
// (v1 internal/bus/embed.go and maintenance.go, v1 ADR 0018).
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ericfitz/agentbus2/internal/core"
)

// HTTP is an OpenAI-style embeddings endpoint client. It implements
// core.Embedder; vectors are returned as the endpoint sent them and the core
// normalizes them.
type HTTP struct {
	endpoint, model, key string
	client               *http.Client
}

var _ core.Embedder = (*HTTP)(nil)

// NewHTTP builds the embedder. A nil client means a default http.Client; the
// caller bounds each request with the context it passes to Embed.
func NewHTTP(endpoint, model, apiKey string, client *http.Client) *HTTP {
	if client == nil {
		client = &http.Client{}
	}
	return &HTTP{endpoint: endpoint, model: model, key: apiKey, client: client}
}

// Model is the model name sent with every request.
func (e *HTTP) Model() string { return e.model }

// Embed posts texts and returns one vector per text, in order. A 400, 413 or
// 422 answer is the endpoint refusing the input itself and is returned as a
// *core.RowRejected with Index -1 (retrying that text alone fails the same
// way); its Reason is the start of the response body, except for a 422, whose
// body echoes the input and is replaced by the status line. Every other
// failure is a plain error.
func (e *HTTP) Embed(ctx context.Context, texts []string) (vecs [][]float32, err error) {
	body, err := json.Marshal(map[string]any{"input": texts, "model": e.model})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if resp.StatusCode/100 != 2 {
		// A 422 body (validation detail) often echoes the input back, and the
		// text reaches status output, so it is never read (v1 ADR 0018).
		var text string
		if resp.StatusCode != http.StatusUnprocessableEntity {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			text = strings.Join(strings.Fields(string(snippet)), " ")
		}
		switch resp.StatusCode {
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
			if text == "" {
				text = resp.Status
			}
			return nil, &core.RowRejected{Index: -1, Reason: text}
		}
		msg := "embeddings endpoint returned " + resp.Status
		if text != "" {
			msg += ": " + text
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if e.key == "" {
				msg += " (no API key configured for the embeddings endpoint)"
			} else {
				msg += " (check the embeddings API key)"
			}
		}
		return nil, fmt.Errorf("%s", msg)
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode embeddings: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings endpoint returned %d vectors for %d inputs", len(out.Data), len(texts))
	}
	// Validate strictly: each index in range exactly once, every vector
	// nonempty, all of the same dimension. A malformed response must error
	// rather than silently persist a bad or zero vector (v1 R7).
	vecs = make([][]float32, len(texts))
	seen := make([]bool, len(texts))
	dim := -1
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("embedding index %d out of range", d.Index)
		}
		if seen[d.Index] {
			return nil, fmt.Errorf("embedding index %d returned more than once", d.Index)
		}
		seen[d.Index] = true
		if len(d.Embedding) == 0 {
			return nil, fmt.Errorf("embedding index %d is empty", d.Index)
		}
		if dim == -1 {
			dim = len(d.Embedding)
		} else if len(d.Embedding) != dim {
			return nil, fmt.Errorf("embedding index %d has dimension %d, want %d", d.Index, len(d.Embedding), dim)
		}
		vecs[d.Index] = d.Embedding
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("embedding index %d missing from response", i)
		}
	}
	return vecs, nil
}
