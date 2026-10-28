// Package inspect holds the write-inspection adapters; the core owns the
// protocol (request, verdict parsing, failure policy).
package inspect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"

	"github.com/ericfitz/agentbus2/internal/core"
)

// Webhook is an Inspector that POSTs the request as JSON to an HTTP endpoint
// and returns the reply body for core.ParseVerdict.
type Webhook struct {
	endpoint, key string
	client        *http.Client
}

var _ core.Inspector = (*Webhook)(nil)

// NewWebhook builds the adapter. A nil client means a default http.Client; the
// caller bounds each call with the context it passes to Inspect. Redirects are
// never followed (a 3xx answer is a status failure), on a copy of the client so
// the caller's own is untouched.
func NewWebhook(endpoint, apiKey string, client *http.Client) *Webhook {
	c := &http.Client{}
	if client != nil {
		cc := *client
		c = &cc
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Webhook{endpoint: endpoint, key: apiKey, client: c}
}

// where names the endpoint by scheme and host only: the path and query string
// may carry a secret.
func (w *Webhook) where() string {
	u, err := url.Parse(w.endpoint)
	if err != nil || u.Host == "" {
		return "the inspection endpoint"
	}
	return u.Scheme + "://" + u.Host
}

func fail(class string, format string, args ...any) error {
	return &core.InspectError{Class: class, Err: fmt.Errorf(format, args...)}
}

// transport wraps err as a transport failure. A *url.Error carries the full
// URL, so it is unwrapped to its cause; a canceled or expired context reports
// the context's error, which stays reachable through errors.Is.
func (w *Webhook) transport(ctx context.Context, what string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		err = cerr
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fail(core.FailTransport, "%s %s: %w", what, w.where(), err)
}

// Inspect posts req and returns the reply body, at most core.MaxVerdictBytes.
// Error text never carries the request, the reply body, the API key or the
// URL's path or query string.
func (w *Webhook) Inspect(ctx context.Context, req core.InspectRequest) (raw []byte, err error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fail(core.FailTransport, "encode the request: %v", err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(body))
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fail(core.FailTransport, "build the request: %v", err)
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	if w.key != "" {
		hr.Header.Set("Authorization", "Bearer "+w.key)
	}
	resp, err := w.client.Do(hr)
	if err != nil {
		return nil, w.transport(ctx, "POST", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = w.transport(ctx, "close the reply from", cerr)
		}
	}()
	if resp.StatusCode/100 != 2 {
		return nil, fail(core.FailStatus, "%s answered %s", w.where(), resp.Status)
	}
	mt, _, merr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if merr != nil || mt != "application/json" {
		return nil, fail(core.FailMalformed, "%s answered with a media type other than application/json", w.where())
	}
	raw, err = io.ReadAll(io.LimitReader(resp.Body, core.MaxVerdictBytes+1))
	if err != nil {
		return nil, w.transport(ctx, "read the reply from", err)
	}
	if len(raw) > core.MaxVerdictBytes {
		return nil, fail(core.FailMalformed, "%s reply is over the %d byte limit", w.where(), core.MaxVerdictBytes)
	}
	return raw, nil
}
