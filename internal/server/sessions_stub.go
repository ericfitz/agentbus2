package server

// Stubs replaced by Task 5. Each answers 500 with a detail naming it.

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/gen"
)

func (a *api) OpenSession(context.Context, gen.OpenSessionRequestObject) (gen.OpenSessionResponseObject, error) {
	return nil, notImplemented("OpenSession")
}

func (a *api) CloseSession(context.Context, gen.CloseSessionRequestObject) (gen.CloseSessionResponseObject, error) {
	return nil, notImplemented("CloseSession")
}

func (a *api) Receive(context.Context, gen.ReceiveRequestObject) (gen.ReceiveResponseObject, error) {
	return nil, notImplemented("Receive")
}

func (a *api) Watch(context.Context, gen.WatchRequestObject) (gen.WatchResponseObject, error) {
	return nil, notImplemented("Watch")
}

func (a *api) ListIdentities(context.Context, gen.ListIdentitiesRequestObject) (gen.ListIdentitiesResponseObject, error) {
	return nil, notImplemented("ListIdentities")
}
