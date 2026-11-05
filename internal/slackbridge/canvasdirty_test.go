package slackbridge

import (
	"slices"
	"testing"
	"time"
)

func TestCanvasDirtyCoalesces(t *testing.T) {
	t0 := time.Unix(0, 0)
	var d canvasDirty
	d.mark("job1", t0.Add(30*time.Second))
	d.mark("job1", t0.Add(5*time.Second))
	d.mark("job1", t0.Add(40*time.Second))
	d.mark("job2", t0.Add(30*time.Second))
	if at, ok := d.next(); !ok || !at.Equal(t0.Add(5*time.Second)) {
		t.Fatalf("next = %v %v", at, ok)
	}
	if got := d.take(t0.Add(4 * time.Second)); len(got) != 0 {
		t.Fatalf("take(4s) = %v", got)
	}
	if got := d.take(t0.Add(5 * time.Second)); !slices.Equal(got, []string{"job1"}) {
		t.Fatalf("take(5s) = %v", got)
	}
	if got := d.take(t0.Add(30 * time.Second)); !slices.Equal(got, []string{"job2"}) {
		t.Fatalf("take(30s) = %v", got)
	}
	if d.len() != 0 {
		t.Fatalf("len = %d", d.len())
	}
	if _, ok := d.next(); ok {
		t.Fatal("next on an empty set")
	}
}

func TestCanvasDirtyTakeSorted(t *testing.T) {
	t0 := time.Unix(0, 0)
	var d canvasDirty
	for _, s := range []string{"b", "a", ""} {
		d.mark(s, t0)
	}
	if got := d.take(t0); !slices.Equal(got, []string{"", "a", "b"}) {
		t.Fatalf("take = %v", got)
	}
}
