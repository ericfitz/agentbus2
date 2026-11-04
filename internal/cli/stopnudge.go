package cli

import (
	"fmt"
	"time"

	"github.com/ericfitz/agentmesh/internal/bus"
)

// nudgeTextRunes is the longest status text a nudge quotes (ADR 0014 decision 16).
const nudgeTextRunes = 80

// stopNudge is the Stop hook's reminder to set a status (ADR 0014 decision 16):
// "" for no nudge. entry is the bound identity's agent-list entry, nil when it
// has no status. Only a top-level (primary) session is nudged, and a
// staleMinutes of 0 turns the nudge off. A stored planning, implementing or
// reviewing status older than staleMinutes (strictly) or no status at all
// nudges; idle, waiting, blocked and needs-human never do, at any age.
func stopNudge(entry *bus.AgentStatusEntry, primary bool, staleMinutes int, now time.Time) string {
	if !primary || staleMinutes <= 0 {
		return ""
	}
	if entry == nil {
		return "Agentmesh: you have no status; call set_status."
	}
	tag := entry.Status.Tag
	if tag == bus.StatusOffline {
		tag = entry.Status.LastTag
	}
	switch tag {
	case bus.StatusPlanning, bus.StatusImplementing, bus.StatusReviewing:
	default:
		return ""
	}
	age := now.Sub(time.UnixMilli(entry.Status.SetAt))
	if age <= time.Duration(staleMinutes)*time.Minute {
		return ""
	}
	text := []rune(entry.Status.Text)
	if len(text) > nudgeTextRunes {
		text = append(text[:nudgeTextRunes-1], '…')
	}
	return fmt.Sprintf("Agentmesh: your status is \"%s: %s\" (%s); if that is no longer true, call set_status.", tag, string(text), shortAge(age))
}

// shortAge renders a duration in whole units, floored: 41m, 5h, 2d; under a
// minute is 0m.
func shortAge(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return "0m"
}
