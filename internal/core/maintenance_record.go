package core

import "time"

// MaintenanceJob is one job's outcome in a tick.
type MaintenanceJob struct {
	Name    string
	Changed int
	Err     string // "" when none
	Skipped bool
}

// MaintenanceTick is what this process's last maintenance tick found.
type MaintenanceTick struct {
	At      int64            // Core.NowMs at the tick
	Held    bool             // TryLock granted the lock to Core.Owner
	LockErr string           // TryLock's error text; "" when none
	Jobs    []MaintenanceJob // empty unless Held
}

// RecordMaintenanceTick stores t as this process's last tick. The record is
// in memory and overwritten by every tick, so with two loops in one process
// the last writer wins (health design spec section 8.1). Jobs is copied.
func (c *Core) RecordMaintenanceTick(t MaintenanceTick) {
	t.Jobs = append([]MaintenanceJob(nil), t.Jobs...)
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	c.lastTick = &t
}

// LastMaintenanceTick returns a copy of the last recorded tick; false before
// the first tick.
func (c *Core) LastMaintenanceTick() (MaintenanceTick, bool) {
	c.tickMu.Lock()
	defer c.tickMu.Unlock()
	if c.lastTick == nil {
		return MaintenanceTick{}, false
	}
	t := *c.lastTick
	t.Jobs = append([]MaintenanceJob(nil), t.Jobs...)
	return t, true
}

// StartedAt is the clock's time when New built this Core.
func (c *Core) StartedAt() time.Time { return c.started }
