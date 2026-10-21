package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) Send(_ context.Context, _ ab2bus.SendInput) (ab2bus.SendResult, error) {
	return ab2bus.SendResult{}, notImplemented("Send")
}

func (b *Bus) History(_ context.Context, _ ab2bus.HistoryInput) ([]ab2bus.Message, error) {
	return nil, notImplemented("History")
}

func (b *Bus) Search(_ context.Context, _ ab2bus.SearchInput) (ab2bus.SearchResult, error) {
	return ab2bus.SearchResult{}, notImplemented("Search")
}
