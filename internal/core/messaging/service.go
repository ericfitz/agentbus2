// Package messaging holds the send and history rules (spec sections 3.4 and
// 4.6): the aggregate over the core for messages on ordinary and memory
// channels. Search lives in its own package.
package messaging

import "github.com/ericfitz/agentbus2/internal/core"

// Service is the messaging aggregate.
type Service struct{ *core.Core }
