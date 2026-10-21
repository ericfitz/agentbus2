package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Bus is the remote implementation of ab2bus.Bus over the generated client.
// Names (channels, sessions) go to the generated client as given: there is
// no client-side path-parameter rule (Decision 80), with the one exception of
// UnsubscribeTags for an empty {patterns} segment (channels.go; pending Eric:
// Milestone 4 item 5).
type Bus struct{ c *gen.ClientWithResponses }

// New wraps a generated client.
func New(c *gen.ClientWithResponses) *Bus { return &Bus{c: c} }

var _ ab2bus.Bus = (*Bus)(nil)

// ErrNotImplemented marks a method whose remote implementation lands in a
// later task of Milestone 4; removed by Task 11.
var ErrNotImplemented = errors.New("remote: not implemented in this milestone")

func notImplemented(method string) error {
	return fmt.Errorf("remote.%s: %w", method, ErrNotImplemented)
}

// maxBodyExcerpt bounds the body text quoted in an unparsable-response error.
const maxBodyExcerpt = 200

// errorOf maps one response to a Bus error (spec 3.3, "never by guessing").
// status and p come from the generated response (p is the parsed problem or
// nil), body is the raw body, err the transport error.
//
//   - err != nil: ctx.Err() when the context is done, else unavailable.
//   - 2xx: nil.
//   - a problem with a code: the code as received (known or not) with the
//     wire's retryable; the message is detail, or title when detail is empty.
//   - anything else: unavailable for 5xx, internal otherwise.
func errorOf(ctx context.Context, status int, p *gen.Problem, body []byte, err error) error {
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return &ab2bus.Error{Code: ab2bus.CodeUnavailable, Message: ab2bus.TruncateErrorMessage(err.Error()), Retryable: true}
	}
	if status >= 200 && status < 300 {
		return nil
	}
	if p == nil && len(body) > 0 {
		// The generated client parses only a declared status; try the body.
		var q gen.Problem
		if json.Unmarshal(body, &q) == nil && q.Code != "" {
			p = &q
		}
	}
	if p != nil && p.Code != "" {
		msg := p.Title
		if p.Detail != nil && *p.Detail != "" {
			msg = *p.Detail
		}
		return &ab2bus.Error{Code: ab2bus.Code(p.Code), Message: ab2bus.TruncateErrorMessage(msg), Retryable: p.Retryable}
	}
	if status >= 500 {
		return &ab2bus.Error{Code: ab2bus.CodeUnavailable, Message: fmt.Sprintf("HTTP %d", status), Retryable: true}
	}
	excerpt := strings.TrimSpace(string(body))
	if len(excerpt) > maxBodyExcerpt {
		excerpt = excerpt[:maxBodyExcerpt]
	}
	return &ab2bus.Error{Code: ab2bus.CodeInternal, Message: ab2bus.TruncateErrorMessage(fmt.Sprintf("HTTP %d: %s", status, excerpt)), Retryable: false}
}

// key maps an idempotency key to the optional header: "" omits it.
func key(k string) *string {
	if k == "" {
		return nil
	}
	return &k
}

// seconds maps the watch timeout to the optional query parameter: 0 omits it
// and a negative duration stays negative for the server to reject.
func seconds(d time.Duration) *float64 {
	if d == 0 {
		return nil
	}
	s := d.Seconds()
	return &s
}
