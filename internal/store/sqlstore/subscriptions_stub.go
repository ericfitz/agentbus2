// Stubs for the subscriptions aggregate; replaced by Task 8.

package sqlstore

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/store"
)

func (t *tx) Subscribe(context.Context, string, int64, int64, int64) error { return ErrNotImplemented }
func (t *tx) Unsubscribe(context.Context, string, int64) error             { return ErrNotImplemented }
func (t *tx) Subscriptions(context.Context, string) ([]store.Subscription, error) {
	return nil, ErrNotImplemented
}
func (t *tx) AdvanceAckedMark(context.Context, string, int64, int64) error { return ErrNotImplemented }
func (t *tx) SubscribeTags(context.Context, store.TagSubscription) error   { return ErrNotImplemented }
func (t *tx) UnsubscribeTags(context.Context, string, string) error        { return ErrNotImplemented }
func (t *tx) TagSubscriptions(context.Context, string) ([]store.TagSubscription, error) {
	return nil, ErrNotImplemented
}
func (t *tx) TagMatchesAfter(context.Context, string, int64, []int64, int) ([]store.Message, error) {
	return nil, ErrNotImplemented
}
