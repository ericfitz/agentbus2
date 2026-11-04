package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentmesh/internal/bus"
)

func TestStopNudge(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	ent := func(tag bus.StatusTag, text string, age time.Duration) *bus.AgentStatusEntry {
		return &bus.AgentStatusEntry{Identity: "pat", Live: true, Status: bus.AgentStatus{Identity: "pat", Tag: tag, Text: text, SetAt: now.Add(-age).UnixMilli()}}
	}
	const noStatus = "Agentmesh: you have no status; call set_status."
	long := strings.Repeat("é", 120)
	cut := strings.Repeat("é", 79) + "…"
	offline := ent(bus.StatusOffline, "X", 31*time.Minute)
	offline.Status.LastTag = bus.StatusImplementing
	cases := []struct {
		name    string
		entry   *bus.AgentStatusEntry
		primary bool
		stale   int
		want    string
	}{
		{"stale implementing", ent(bus.StatusImplementing, "X", 31*time.Minute), true, 30, `Agentmesh: your status is "implementing: X" (31m); if that is no longer true, call set_status.`},
		{"fresh", ent(bus.StatusImplementing, "X", 29*time.Minute), true, 30, ""},
		{"exactly the threshold", ent(bus.StatusImplementing, "X", 30*time.Minute), true, 30, ""},
		{"no status", nil, true, 30, noStatus},
		{"planning", ent(bus.StatusPlanning, "P", 31*time.Minute), true, 30, `Agentmesh: your status is "planning: P" (31m); if that is no longer true, call set_status.`},
		{"reviewing", ent(bus.StatusReviewing, "R", 5*time.Hour+3*time.Minute), true, 30, `Agentmesh: your status is "reviewing: R" (5h); if that is no longer true, call set_status.`},
		{"not primary", ent(bus.StatusImplementing, "X", time.Hour), false, 30, ""},
		{"not primary, no status", nil, false, 30, ""},
		{"threshold 0", ent(bus.StatusImplementing, "X", time.Hour), true, 0, ""},
		{"threshold 0, no status", nil, true, 0, ""},
		{"long text", ent(bus.StatusImplementing, long, 49*time.Hour), true, 30, `Agentmesh: your status is "implementing: ` + cut + `" (2d); if that is no longer true, call set_status.`},
		{"offline uses last tag", offline, true, 30, `Agentmesh: your status is "implementing: X" (31m); if that is no longer true, call set_status.`},
	}
	for _, tag := range []bus.StatusTag{bus.StatusIdle, bus.StatusWaiting, bus.StatusBlocked, bus.StatusNeedsHuman, "mystery"} {
		cases = append(cases, struct {
			name    string
			entry   *bus.AgentStatusEntry
			primary bool
			stale   int
			want    string
		}{"never nudges: " + string(tag), ent(tag, "X", 10*time.Hour), true, 30, ""})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stopNudge(c.entry, c.primary, c.stale, now); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestShortAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		59 * time.Second:             "0m",
		41 * time.Minute:             "41m",
		5*time.Hour + 10*time.Minute: "5h",
		49 * time.Hour:               "2d",
	} {
		if got := shortAge(d); got != want {
			t.Errorf("shortAge(%v) = %q, want %q", d, got, want)
		}
	}
}
