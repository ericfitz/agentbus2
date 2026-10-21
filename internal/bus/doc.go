// Package bus is the consumer-facing seam of agentbus2: the five interfaces
// split by consumer (Messaging, Sessions, Memories, Tasks, Admin; Decision 7),
// the domain types they exchange and the Error with its code and retryable
// members (Decision 46). It imports neither the store nor the core, so no
// client ever sees a storage or generated type (Decision 47).
//
// Error, MaxErrorMessageBytes and TruncateErrorMessage are ported from v1
// internal/bus/errors.go; the rule ports live in internal/core/rules.
package bus
