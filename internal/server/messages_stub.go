package server

// Stubs replaced by Task 7. Each answers 500 with a detail naming it.

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/gen"
)

func (a *api) SendMessage(context.Context, gen.SendMessageRequestObject) (gen.SendMessageResponseObject, error) {
	return nil, notImplemented("SendMessage")
}

func (a *api) History(context.Context, gen.HistoryRequestObject) (gen.HistoryResponseObject, error) {
	return nil, notImplemented("History")
}

func (a *api) Search(context.Context, gen.SearchRequestObject) (gen.SearchResponseObject, error) {
	return nil, notImplemented("Search")
}
