// Package notify is the wake seam of Decision 52: a Notifier carries Hints
// from writers to watchers. It is a leaf package (core imports it, never the
// reverse) with no store import. Memory fans out in process; Polling is the
// local multi-process form, ticking at an interval and ignoring Publish
// (v1 internal/bus/wait.go WaitPollInterval). Postgres NOTIFY is Milestone 7.
package notify

// Hint says something may have changed. Seq 0 marks a non-message hint (a
// watch registration, an ACL change); a watcher re-reads the store either way.
type Hint struct{ ChannelID, Seq int64 }

// Notifier carries hints. Publish never blocks. Subscribe returns a channel
// of hints and a cancel func that stops delivery; cancel is idempotent.
type Notifier interface {
	Publish(h Hint)
	Subscribe() (hints <-chan Hint, cancel func())
}
