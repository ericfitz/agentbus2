package server

import (
	"net/http"
	"net/http/httptest"
)

type inprocTransport struct {
	h        http.Handler
	identity string
}

// InProcessTransport is a RoundTripper that calls h.ServeHTTP on a recorder
// (Decision 35), with identity on the context for the InProcess
// authenticator. The request is handed over as the client built it, with no
// request-line serialization. A done context is an error, as in net/http.
func InProcessTransport(h http.Handler, identity string) http.RoundTripper {
	return inprocTransport{h: h, identity: identity}
}

func (t inprocTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, req.WithContext(withInprocIdentity(req.Context(), t.identity)))
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}
