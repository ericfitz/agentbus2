package core

import "github.com/ericfitz/agentbus2/internal/core/notify"

// Publish passes a hint to the Notifier (Decision 52). Callers publish after
// the transaction commits; Publish never blocks.
func (c *Core) Publish(h notify.Hint) { c.notifier.Publish(h) }

// Notifier returns the notifier, for watchers that Subscribe.
func (c *Core) Notifier() notify.Notifier { return c.notifier }
