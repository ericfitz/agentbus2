package bus

import (
	"context"
	"sync"

	"github.com/ericfitz/agentbus2/internal/core"
)

// allowJSON is the inspector reply that allows a write.
const allowJSON = `{"verdict":"allow"}`

// refuseJSON is the inspector reply that refuses a write.
func refuseJSON(code, reason string) []byte {
	return []byte(`{"verdict":"refuse","code":"` + code + `","reason":"` + reason + `"}`)
}

// fakeInspector is the in-process core.Inspector the write-inspection suites
// share. It records every request; reply decides the answer (nil: allow). It
// runs outside any transaction, as the real adapter does, so reply may use the
// raw Store.
type fakeInspector struct {
	mu    sync.Mutex
	reqs  []core.InspectRequest
	reply func(ctx context.Context, req core.InspectRequest) ([]byte, error)
}

// Inspect records req and answers with reply.
func (f *fakeInspector) Inspect(ctx context.Context, req core.InspectRequest) ([]byte, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	reply := f.reply
	f.mu.Unlock()
	if reply == nil {
		return []byte(allowJSON), nil
	}
	return reply(ctx, req)
}

// calls returns a copy of the requests seen so far.
func (f *fakeInspector) calls() []core.InspectRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.InspectRequest(nil), f.reqs...)
}
