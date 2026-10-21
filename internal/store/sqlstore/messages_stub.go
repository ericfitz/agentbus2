// Stubs for the messages aggregate; replaced by Task 6.

package sqlstore

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/search"

	"github.com/ericfitz/agentbus2/internal/store"
)

func (t *tx) AppendMessage(context.Context, store.NewMessage) (int64, error) {
	return 0, ErrNotImplemented
}
func (t *tx) Message(context.Context, int64) (store.Message, error) {
	return store.Message{}, ErrNotImplemented
}
func (t *tx) MessagesAfter(context.Context, int64, int64, int) ([]store.Message, error) {
	return nil, ErrNotImplemented
}
func (t *tx) MessagesInRange(context.Context, int64, int64, int64, []store.TagPattern, int) ([]store.Message, error) {
	return nil, ErrNotImplemented
}
func (t *tx) SearchText(context.Context, search.Query, []int64, int) ([]store.Message, error) {
	return nil, ErrNotImplemented
}
func (t *tx) TombstoneMessage(context.Context, int64, int64) (bool, error) {
	return false, ErrNotImplemented
}
func (t *tx) DeleteMessages(context.Context, []int64) error { return ErrNotImplemented }
func (t *tx) ExpiredTombstones(context.Context, int64, int) ([]int64, error) {
	return nil, ErrNotImplemented
}
func (t *tx) OrdinaryMessagesBefore(context.Context, int64, int) ([]store.SeqChannel, error) {
	return nil, ErrNotImplemented
}
func (t *tx) UnembeddedMemories(context.Context, string, int) ([]store.Message, error) {
	return nil, ErrNotImplemented
}
func (t *tx) PutEmbedding(context.Context, store.Embedding) error       { return ErrNotImplemented }
func (t *tx) PutEmbedFailure(context.Context, store.EmbedFailure) error { return ErrNotImplemented }
func (t *tx) DeleteEmbedding(context.Context, int64) error              { return ErrNotImplemented }
func (t *tx) DeleteStaleModel(context.Context, string) error            { return ErrNotImplemented }
func (t *tx) Embeddings(context.Context, string, []int64) ([]store.Embedding, error) {
	return nil, ErrNotImplemented
}
