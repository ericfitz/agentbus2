// Stubs for the receipts aggregate; replaced by Task 4.

package sqlstore

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/store"
)

func (t *tx) Receipt(context.Context, string, string, int64) (store.Receipt, bool, error) {
	return store.Receipt{}, false, ErrNotImplemented
}
func (t *tx) PutReceipt(context.Context, store.Receipt) error { return ErrNotImplemented }
func (t *tx) DeleteExpiredReceipts(context.Context, int64, int) (int, error) {
	return 0, ErrNotImplemented
}
