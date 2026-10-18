package adapters

import (
	"testing"
	"time"
)

// The decision of what a stored fact is called on the screen is plain code: one table covers every reason the writers use today and the
// ones that may be added (they fall back on who ends up with the conversation).
func TestToTimelineEventNamesEachKindOfChange(t *testing.T) {
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		row        timelineRow
		ok         bool
		kind       string
		actor, tgt string
		system     bool
	}{
		{"claim", timelineRow{src: "assignment", code: "manual_claim", by: "Ana", to: "Ana"}, true, "claimed", "Ana", "", false},
		{"hub claim", timelineRow{src: "assignment", code: "hub_claim", by: "Beto"}, true, "claimed", "Beto", "", false},
		{"assign", timelineRow{src: "assignment", code: "manual_assign", by: "Ana", to: "Beto"}, true, "assigned", "Ana", "Beto", false},
		{"release", timelineRow{src: "assignment", code: "manual_release", by: "Ana", from: "Ana"}, true, "released", "Ana", "", false},
		{"unassign", timelineRow{src: "assignment", code: "manual_unassign", by: "Chefe", from: "Ana"}, true, "released", "Chefe", "", false},
		{"hub transfer", timelineRow{src: "assignment", code: "hub_transfer", by: "Ana", to: "Beto"}, true, "transferred", "Ana", "Beto", false},
		{"queue", timelineRow{src: "assignment", code: "queue_transfer", by: "Ana", from: "Ana"}, true, "sent_to_queue", "Ana", "", false},
		{"takeover", timelineRow{src: "assignment", code: "takeover_external", by: "Chefe", to: "Chefe"}, true, "taken_over", "Chefe", "", false},
		{"round robin", timelineRow{src: "assignment", code: "round_robin", to: "Beto", extra: "system"}, true, "auto_assigned", "", "Beto", true},
		{"unknown reason that gave it to someone", timelineRow{src: "assignment", code: "novo_motivo", to: "Beto", extra: "system"}, true, "auto_assigned", "", "Beto", true},
		{"unknown reason that took it from someone", timelineRow{src: "assignment", code: "novo_motivo", from: "Beto", extra: "system"}, true, "released", "", "", true},
		{"changed nobody", timelineRow{src: "assignment", code: "novo_motivo"}, false, "", "", "", false},
		{"app claims", timelineRow{src: "control", code: "claimed", by: "ignored"}, true, "external_claimed", "", "", false},
		{"app releases", timelineRow{src: "control", code: "released"}, true, "external_released", "", "", false},
		{"control change between people is not told here", timelineRow{src: "control", code: ""}, false, "", "", "", false},
		{"protocol opened", timelineRow{src: "protocol_opened", code: "KTG-1007", by: "Ana"}, true, "protocol_opened", "Ana", "", false},
		{"finalized by the system", timelineRow{src: "finalized", code: "no_response", from: "KTG-1007", extra: "system"}, true, "finalized", "", "", true},
		{"something else", timelineRow{src: "other"}, false, "", "", "", false},
	}
	for _, c := range cases {
		c.row.at, c.row.id = at, "x"
		ev, ok := toTimelineEvent(c.row)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if ev.Kind != c.kind || ev.Actor != c.actor || ev.Target != c.tgt || ev.System != c.system {
			t.Errorf("%s: got kind=%q actor=%q target=%q system=%v, want %q %q %q %v", c.name, ev.Kind, ev.Actor, ev.Target, ev.System, c.kind, c.actor, c.tgt, c.system)
		}
	}
}
