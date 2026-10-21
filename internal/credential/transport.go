package credential

import (
	"bytes"
	"io"
	"net/http"
)

type refreshing struct {
	base http.RoundTripper
	s    *Source
}

// Transport wraps base so that a 401 triggers exactly one refresh and one
// replay of the request. Any other status or error passes through; a refresh
// that fails returns the original 401 response (the remote maps it to
// unauthenticated).
func Transport(base http.RoundTripper, s *Source) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &refreshing{base: base, s: s}
}

func (t *refreshing) RoundTrip(req *http.Request) (*http.Response, error) {
	hasBody := req.Body != nil && req.Body != http.NoBody
	var body []byte
	if hasBody {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}
	clone := func() *http.Request {
		r := req.Clone(req.Context())
		if hasBody {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		}
		return r
	}
	resp, err := t.base.RoundTrip(clone())
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	_ = t.s.Cache.Remove(t.s.Identity)
	tok, rerr := t.s.Refresh(req.Context())
	if rerr != nil {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	again := clone()
	again.Header.Set("Authorization", "Bearer "+tok)
	return t.base.RoundTrip(again)
}
