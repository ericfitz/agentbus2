package client

import (
	"context"
	"crypto"
	"fmt"
	"net/http"

	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Register registers pub as a credential of identity at server over an
// unauthenticated client (registration is a pre-identity call). It exists so
// the CLI can register without importing internal/gen (the Task 5 import
// rule). hc nil: a client with no Timeout. enrollmentToken is sent only when
// non-empty.
func Register(ctx context.Context, hc *http.Client, server, identity string, pub crypto.PublicKey, enrollmentToken string) (remote.RegisterResult, error) {
	if hc == nil {
		hc = &http.Client{}
	}
	gc, err := gen.NewClientWithResponses(server, gen.WithHTTPClient(hc))
	if err != nil {
		return remote.RegisterResult{}, resolveErr(fmt.Errorf("server %q: %w", server, err))
	}
	return remote.Register(ctx, gc, identity, pub, enrollmentToken)
}
