package core

import (
	"context"
	"testing"
	"time"
)

type fakeModelEmbedder string

func (f fakeModelEmbedder) Model() string { return string(f) }
func (f fakeModelEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

func TestLimiterCheckThenCharge(t *testing.T) {
	l := NewLimiter(100, 1024, 64)
	now := time.UnixMilli(startMs)
	for i := range 100 {
		if !l.Check("pat", 10, now) {
			t.Fatalf("Check %d refused", i)
		}
		l.Charge("pat", 10, now)
	}
	if l.Check("pat", 10, now) {
		t.Fatal("101st Check passed")
	}
	// Check alone charges nothing.
	if l.Check("pat", 10, now) {
		t.Fatal("Check changed the bucket")
	}
	if !l.Check("pat", 10, now.Add(time.Second)) {
		t.Fatal("no message available after 1 s")
	}
}

func TestLimiterBytes(t *testing.T) {
	l := NewLimiter(1000, 64, 1) // 64 KiB/s burst
	now := time.UnixMilli(startMs)
	if !l.Check("pat", 64*1024, now) {
		t.Fatal("a full-bucket message refused")
	}
	l.Charge("pat", 64*1024, now)
	if l.Check("pat", 1, now) {
		t.Fatal("empty byte bucket passed")
	}
	if !l.Check("pat", 1024, now.Add(time.Second)) {
		t.Fatal("bucket did not refill after 1 s")
	}
	if l.Check("pat", 64*1024+1, now.Add(10*time.Second)) {
		t.Fatal("a message above the burst cap passed")
	}
}

func TestLimiterChargeGoesNegative(t *testing.T) {
	l := NewLimiter(1, 64, 1)
	now := time.UnixMilli(startMs)
	l.Charge("pat", 1, now)
	l.Charge("pat", 1, now) // two senders passed Check together
	if l.Check("pat", 1, now.Add(time.Second)) {
		t.Fatal("a bucket one message in debt passed after 1 s")
	}
	if !l.Check("pat", 1, now.Add(2*time.Second)) {
		t.Fatal("bucket did not recover")
	}
}

func TestLimiterKeyedPerIdentity(t *testing.T) { // Decision 86
	l := NewLimiter(1, 64, 1)
	now := time.UnixMilli(startMs)
	l.Charge("pat", 1, now)
	if l.Check("pat", 1, now) {
		t.Fatal("pat should be exhausted")
	}
	if !l.Check("kim", 1, now) {
		t.Fatal("kim shares pat's bucket")
	}
}
