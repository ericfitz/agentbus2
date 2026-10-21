package sqlstore

import "errors"

// ErrNotImplemented is returned by the stubs of aggregates whose task has not
// landed yet. Task 12 deletes it.
var ErrNotImplemented = errors.New("sqlstore: not implemented in this milestone")
