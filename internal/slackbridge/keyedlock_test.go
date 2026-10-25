package slackbridge

import (
	"testing"
	"time"
)

func TestKeyedLock(t *testing.T) {
	var k keyedLock
	unlockA := k.lock(1)

	// Same key blocks until released.
	got := make(chan struct{})
	go func() {
		u := k.lock(1)
		close(got)
		u()
	}()
	select {
	case <-got:
		t.Fatal("second lock of the same key did not wait")
	case <-time.After(50 * time.Millisecond):
	}

	// Another key is independent.
	other := make(chan struct{})
	go func() {
		u := k.lock(2)
		close(other)
		u()
	}()
	select {
	case <-other:
	case <-time.After(3 * time.Second):
		t.Fatal("a different key was blocked")
	}

	unlockA()
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("waiter did not run after unlock")
	}

	// Entries are dropped once nobody holds or waits.
	k.mu.Lock()
	n := len(k.m)
	k.mu.Unlock()
	if n != 0 {
		t.Errorf("%d entries left after every unlock", n)
	}
}
