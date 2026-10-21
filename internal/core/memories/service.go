// Package memories holds the memory rules over the Store: reading a live
// revision or the revision history, editing (tombstone the live revision,
// insert the next) and deleting, each under the channel ACL check of spec
// section 4.6 (Decision 95). Ported from v1 internal/bus/memories.go.
package memories

import "github.com/ericfitz/agentbus2/internal/core"

// Service is the memory aggregate over a Core.
type Service struct{ *core.Core }
