package slackbridge

import (
	"slices"
	"time"
)

// canvasDirty is the canvas coalescer: the scopes waiting for a rebuild and
// the deadline of each. The refresh request (every live scope) is the key "".
// It is not safe for concurrent use; its owner locks around it.
type canvasDirty struct{ due map[string]time.Time }

// mark makes scope due at at. An earlier deadline already held wins, so marks
// within a window join it and never lengthen it; a later mark with an earlier
// time (a rate-limit retry) shortens it.
func (d *canvasDirty) mark(scope string, at time.Time) {
	if d.due == nil {
		d.due = map[string]time.Time{}
	}
	if cur, ok := d.due[scope]; ok && !at.Before(cur) {
		return
	}
	d.due[scope] = at
}

// next is the earliest deadline, false when nothing is waiting.
func (d *canvasDirty) next() (time.Time, bool) {
	var first time.Time
	found := false
	for _, at := range d.due {
		if !found || at.Before(first) {
			first, found = at, true
		}
	}
	return first, found
}

// take removes and returns, sorted, every scope whose deadline is not after
// now.
func (d *canvasDirty) take(now time.Time) []string {
	var out []string
	for scope, at := range d.due {
		if !at.After(now) {
			out = append(out, scope)
			delete(d.due, scope)
		}
	}
	slices.Sort(out)
	return out
}

// len is how many scopes wait.
func (d *canvasDirty) len() int { return len(d.due) }
