package core

import (
	"testing"
	"time"
)

func TestStartedAtIsTheClock(t *testing.T) {
	c, clk := newTestCore(t, nil)
	want := time.UnixMilli(startMs)
	if !c.StartedAt().Equal(want) {
		t.Fatalf("StartedAt = %v, want %v", c.StartedAt(), want)
	}
	clk.advance(time.Hour)
	if !c.StartedAt().Equal(want) {
		t.Fatalf("StartedAt moved to %v", c.StartedAt())
	}
}

func TestMaintenanceTickRecordCopies(t *testing.T) {
	c, _ := newTestCore(t, nil)
	if _, ok := c.LastMaintenanceTick(); ok {
		t.Fatal("record before any tick")
	}
	jobs := []MaintenanceJob{{Name: "a", Changed: 1}, {Name: "b", Err: "x"}}
	c.RecordMaintenanceTick(MaintenanceTick{At: 7, Held: true, Jobs: jobs})
	jobs[0].Name = "mutated"
	got, ok := c.LastMaintenanceTick()
	if !ok || got.At != 7 || !got.Held || got.Jobs[0].Name != "a" {
		t.Fatalf("record = %+v, %v", got, ok)
	}
	got.Jobs[1].Name = "mutated"
	again, _ := c.LastMaintenanceTick()
	if again.Jobs[1].Name != "b" || again.Jobs[1].Err != "x" {
		t.Fatalf("read copy aliases the record: %+v", again.Jobs)
	}
}
