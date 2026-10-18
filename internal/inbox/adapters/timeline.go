package adapters

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// TimelineEvent is one thing that happened TO the attendance (not a message): who took it, handed it over, which protocol opened or closed, how it ended. The screen
// draws it as a discreet line between the messages. It carries only what the conversation's own readers already see: people by display name, an external application
// by kind alone (never its operator, ADR-0048), the closure by reason (never its note or summary).
type TimelineEvent struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Actor    string    `json:"actor,omitempty"`    // who did it, when a person: the display name, which only appears when the reader may read that person (their own, or membership.read)
	ActorID  string    `json:"actor_id,omitempty"` // the same person's id (conversations already carry their owner's id), so the screen can say "you" or fall back on a generic label
	Target   string    `json:"target,omitempty"`   // who received it, when a person
	TargetID string    `json:"target_id,omitempty"`
	Protocol string    `json:"protocol,omitempty"` // the protocol number, for protocol events and a finalization
	Detail   string    `json:"detail,omitempty"`   // finalization: the reason code (resolved, no_response, duplicate, spam, transferred, other)
	System   bool      `json:"system,omitempty"`   // done by the platform itself (distribution, a timer, a rule), not by a person
}

// timelineRow is the raw shape every source is reduced to before the kind is decided in Go (so the decision is plain code, not SQL).
type timelineRow struct {
	src, id, code, by, byID, from, to, toID, extra string
	at                                             time.Time
}

// timelineLimit keeps one answer small: an attendance has a handful of events, and a runaway one still shows its latest.
const timelineLimit = 300

// toTimelineEvent turns a raw row into an event, or reports false for a row that says nothing worth showing (an assignment that changed nobody).
func toTimelineEvent(r timelineRow) (TimelineEvent, bool) {
	ev := TimelineEvent{ID: r.src + ":" + r.id, At: r.at, Actor: r.by, ActorID: r.byID}
	switch r.src {
	case "assignment":
		switch r.code {
		case "manual_claim", "hub_claim":
			ev.Kind = "claimed"
		case "manual_assign":
			ev.Kind, ev.Target, ev.TargetID = "assigned", r.to, r.toID
		case "manual_release", "hub_release", "manual_unassign":
			ev.Kind = "released"
		case "hub_transfer":
			ev.Kind, ev.Target, ev.TargetID = "transferred", r.to, r.toID
		case "queue_transfer":
			ev.Kind = "sent_to_queue"
		case "takeover_external":
			ev.Kind = "taken_over"
		default:
			// distribution by the platform (round robin, overflow, on call, ...) or a reason added later: said by who ends up with it
			if r.to == "" && r.from == "" {
				return ev, false
			}
			ev.System = r.extra == "system"
			if r.to != "" {
				ev.Kind, ev.Target, ev.TargetID = "auto_assigned", r.to, r.toID
			} else {
				ev.Kind = "released"
			}
		}
		if ev.Actor == "" && r.extra == "system" {
			ev.System = true
		}
	case "control":
		// only an external application is told here: people going through the legacy writers are already in assignment_events
		switch r.code {
		case "claimed":
			ev.Kind, ev.Actor, ev.ActorID = "external_claimed", "", ""
		case "released":
			ev.Kind, ev.Actor, ev.ActorID = "external_released", "", ""
		default:
			return ev, false
		}
	case "protocol_opened":
		ev.Kind, ev.Protocol = "protocol_opened", r.code
	case "protocol_closed":
		ev.Kind, ev.Protocol = "protocol_closed", r.code
	case "finalized":
		ev.Kind, ev.Detail, ev.Protocol = "finalized", r.code, r.from
		ev.System = r.extra == "system"
	default:
		return ev, false
	}
	return ev, true
}

var timelineSQL = `
SELECT src, id, at, code, by_name, by_id, from_name, to_name, to_id, extra FROM (
  SELECT 'assignment' AS src, e.id::text AS id, e.created_at AS at, e.reason AS code,
         COALESCE(NULLIF(btrim(cb.display_name), ''), '') AS by_name, COALESCE(e.changed_by::text, '') AS by_id,
         COALESCE(NULLIF(btrim(fu.display_name), ''), '') AS from_name,
         COALESCE(NULLIF(btrim(tu.display_name), ''), '') AS to_name, COALESCE(e.to_user_id::text, '') AS to_id,
         e.actor_source AS extra
    FROM assignment_events e
    LEFT JOIN users cb ON cb.id = e.changed_by
    LEFT JOIN users fu ON fu.id = e.from_user_id
    LEFT JOIN users tu ON tu.id = e.to_user_id
   WHERE e.tenant_id = $1 AND e.conversation_id = $2
  UNION ALL
  SELECT 'control', e.id::text, e.created_at,
         CASE WHEN COALESCE(e.to_kind, '') = 'crm_application' AND COALESCE(e.from_kind, '') <> 'crm_application' THEN 'claimed'
              WHEN COALESCE(e.from_kind, '') = 'crm_application' AND COALESCE(e.to_kind, '') <> 'crm_application' THEN 'released'
              ELSE '' END,
         '', '', '', '', '', e.actor_kind
    FROM conversation_control_events e
   WHERE e.tenant_id = $1 AND e.conversation_id = $2
  UNION ALL
  SELECT 'protocol_opened', s.id::text, s.opened_at, s.protocol,
         COALESCE(NULLIF(btrim(u.display_name), ''), ''), COALESCE(s.opened_by_user_id::text, ''), '', '', '', s.opened_source
    FROM service_sessions s LEFT JOIN users u ON u.id = s.opened_by_user_id
   WHERE s.tenant_id = $1 AND s.conversation_id = $2
  UNION ALL
  SELECT 'protocol_closed', s.id::text, s.closed_at, s.protocol,
         COALESCE(NULLIF(btrim(u.display_name), ''), ''), COALESCE(s.closed_by_user_id::text, ''), '', '', '', COALESCE(s.closed_source, '')
    FROM service_sessions s LEFT JOIN users u ON u.id = s.closed_by_user_id
   WHERE s.tenant_id = $1 AND s.conversation_id = $2 AND s.closed_at IS NOT NULL
     -- a finalization closes the protocol in the same breath: the finalization line already says so
     AND NOT EXISTS (SELECT 1 FROM conversation_closures cc
                      WHERE cc.tenant_id = s.tenant_id AND cc.conversation_id = s.conversation_id
                        AND abs(extract(epoch FROM cc.created_at - s.closed_at)) < 5)
  UNION ALL
  SELECT 'finalized', cc.id::text, cc.created_at, cc.reason,
         COALESCE(NULLIF(btrim(u.display_name), ''), ''), COALESCE(cc.closed_by_user_id::text, ''),
         COALESCE((SELECT s.protocol FROM service_sessions s
                    WHERE s.tenant_id = cc.tenant_id AND s.conversation_id = cc.conversation_id AND s.closed_at IS NOT NULL
                      AND abs(extract(epoch FROM cc.created_at - s.closed_at)) < 5
                    ORDER BY s.closed_at DESC LIMIT 1), ''),
         '', '', cc.source
    FROM conversation_closures cc LEFT JOIN users u ON u.id = cc.closed_by_user_id
   WHERE cc.tenant_id = $1 AND cc.conversation_id = $2
) t ORDER BY at DESC, id DESC LIMIT ` + strconv.Itoa(timelineLimit)

// ListTimeline answers GET .../inbox/conversations/{conversation_id}/timeline: the events of the attendance, oldest first. Who may read it is who may read the
// conversation (the route is guarded like its messages, sector scope included); a conversation of another company is a 404.
func (h *InboxAPIHandler) ListTimeline(w http.ResponseWriter, r *http.Request) {
	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var exists bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM conversations WHERE tenant_id=$1 AND id=$2)`, tenantID, conversationID).Scan(&exists); err != nil {
		http.Error(w, "failed to read timeline", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	rows, err := q.Query(r.Context(), timelineSQL, tenantID, conversationID)
	if err != nil {
		http.Error(w, "failed to read timeline", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	events := make([]TimelineEvent, 0, 16)
	for rows.Next() {
		var row timelineRow
		if err := rows.Scan(&row.src, &row.id, &row.at, &row.code, &row.by, &row.byID, &row.from, &row.to, &row.toID, &row.extra); err != nil {
			http.Error(w, "failed to read timeline", http.StatusInternalServerError)
			return
		}
		if ev, ok := toTimelineEvent(row); ok {
			events = append(events, ev)
		}
	}
	if rows.Err() != nil {
		http.Error(w, "failed to read timeline", http.StatusInternalServerError)
		return
	}
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].At.Equal(events[j].At) {
			return events[i].At.Before(events[j].At)
		}
		return strings.Compare(events[i].ID, events[j].ID) < 0
	})
	writeJSON(w, map[string]any{"items": events})
}
