package adapters

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type routingRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type routingDecisionItem struct {
	At               time.Time `json:"at"`
	Trigger          string    `json:"trigger"`
	Decision         string    `json:"decision"`
	Reason           string    `json:"reason"`
	Destination      string    `json:"destination,omitempty"`
	DestinationLabel string    `json:"destination_label,omitempty"`
	Attempts         int       `json:"attempts"`
}

type routingSLAItem struct {
	Metric     string     `json:"metric"`
	State      string     `json:"state"`
	StartedAt  time.Time  `json:"started_at"`
	RiskAt     time.Time  `json:"risk_at"`
	DeadlineAt time.Time  `json:"deadline_at"`
	Counting   string     `json:"counting"`
	MetAt      *time.Time `json:"met_at,omitempty"`
	BreachedAt *time.Time `json:"breached_at,omitempty"`
}

// GetConversationRouting: GET /inbox/conversations/{conversation_id}/routing. Where the conversation is (sector, group, owner), why it waits,
// when it can move, its SLA clocks and the central engine's recent decisions. Read-only; it shows names and states, never message content.
// Visible to whoever can read the conversation: the route is mounted like the conversation read itself (members through the database policies,
// a Hub operator through the delegated "conversation.read" door), and a conversation of a contact declared internal is hidden from whoever
// lacks conversation.read_internal, exactly as in the Inbox.
func (h *Handler) GetConversationRouting(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	id, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	var (
		queueID, assigned *uuid.UUID
		queueName         *string
		sectorID          *uuid.UUID
		sectorName        *string
		status            string
		assignedName      *string
		createdAt         time.Time
		assignedAt        *time.Time
	)
	var contactKind string
	err = q.QueryRow(r.Context(), `SELECT c.queue_id, qq.name, s.id, s.name, c.status, c.assigned_to_user_id, u.display_name, c.created_at, c.assigned_at, COALESCE(co.kind,'')
		FROM conversations c
		LEFT JOIN contacts co ON co.tenant_id=c.tenant_id AND co.id=c.contact_id
		LEFT JOIN queues qq ON qq.tenant_id=c.tenant_id AND qq.id=c.queue_id
		LEFT JOIN sectors s ON s.tenant_id=qq.tenant_id AND s.id=qq.sector_id
		LEFT JOIN users u ON u.id=c.assigned_to_user_id
		WHERE c.tenant_id=$1 AND c.id=$2`, tc.TenantID, id).Scan(&queueID, &queueName, &sectorID, &sectorName, &status, &assigned, &assignedName, &createdAt, &assignedAt, &contactKind)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read the conversation", http.StatusInternalServerError)
		return
	}
	if contactKind == "internal" {
		if can, perr := tenancyadapters.CanReadInternalConversations(r.Context(), q, tc); perr != nil || !can {
			http.Error(w, "conversation not found", http.StatusNotFound)
			return
		}
	}
	out := map[string]any{"conversation_id": id, "status": status}
	if queueID != nil && queueName != nil {
		out["queue"] = routingRef{*queueID, *queueName}
	}
	if sectorID != nil && sectorName != nil {
		out["sector"] = routingRef{*sectorID, *sectorName}
	}
	if assigned != nil {
		name := ""
		if assignedName != nil {
			name = *assignedName
		}
		out["assigned"] = map[string]any{"user_id": *assigned, "name": name, "at": assignedAt}
	}

	// the engine's own state (absent while the engine is off or has not seen the conversation)
	var state, lastDecision string
	var episode, step, hops int
	var enteredQueue time.Time
	var visited []string
	switch err := q.QueryRow(r.Context(), `SELECT state, COALESCE(last_decision,''), episode, step_index, hops, entered_queue_at, visited
		FROM routing_engine_state WHERE tenant_id=$1 AND conversation_id=$2`, tc.TenantID, id).Scan(&state, &lastDecision, &episode, &step, &hops, &enteredQueue, &visited); {
	case err == nil:
		st := map[string]any{"state": state, "last_decision": lastDecision, "episode": episode, "step": step, "hops": hops, "entered_queue_at": enteredQueue}
		if status == "open" && assigned == nil {
			st["waiting_seconds"] = int(time.Since(enteredQueue).Seconds())
		}
		var wake *time.Time
		if err := q.QueryRow(r.Context(), `SELECT min(due_at) FROM routing_engine_timers WHERE tenant_id=$1 AND conversation_id=$2 AND episode=$3 AND state IN ('pending','leased') AND kind IN ('open_wakeup','step_wait')`,
			tc.TenantID, id, episode).Scan(&wake); err == nil && wake != nil {
			st["next_attempt_at"] = wake
		}
		out["engine"] = st
	case !errors.Is(err, pgx.ErrNoRows):
		http.Error(w, "failed to read the routing state", http.StatusInternalServerError)
		return
	}

	if queueID != nil {
		if a, err := QueueAvailability(r.Context(), q, tc.TenantID, *queueID, time.Now()); err == nil {
			out["schedule"] = a
		}
	}

	slas := []routingSLAItem{}
	rows, err := q.Query(r.Context(), `SELECT metric, state, started_at, risk_at, deadline_at, counting, met_at, breached_at
		FROM conversation_sla WHERE tenant_id=$1 AND conversation_id=$2 AND state <> 'cancelled' ORDER BY started_at, metric LIMIT 40`, tc.TenantID, id)
	if err != nil {
		http.Error(w, "failed to read the SLA clocks", http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var it routingSLAItem
		if err := rows.Scan(&it.Metric, &it.State, &it.StartedAt, &it.RiskAt, &it.DeadlineAt, &it.Counting, &it.MetAt, &it.BreachedAt); err != nil {
			rows.Close()
			http.Error(w, "failed to read the SLA clocks", http.StatusInternalServerError)
			return
		}
		slas = append(slas, it)
	}
	rows.Close()
	out["sla"] = slas

	hist := []routingDecisionItem{}
	drows, err := q.Query(r.Context(), `SELECT created_at, trigger, decision, reason, destination, attempts FROM routing_engine_decisions
		WHERE tenant_id=$1 AND conversation_id=$2 ORDER BY created_at DESC, id LIMIT 30`, tc.TenantID, id)
	if err != nil {
		http.Error(w, "failed to read the routing history", http.StatusInternalServerError)
		return
	}
	for drows.Next() {
		var it routingDecisionItem
		if err := drows.Scan(&it.At, &it.Trigger, &it.Decision, &it.Reason, &it.Destination, &it.Attempts); err != nil {
			drows.Close()
			http.Error(w, "failed to read the routing history", http.StatusInternalServerError)
			return
		}
		hist = append(hist, it)
	}
	drows.Close()
	for i := range hist {
		hist[i].DestinationLabel = h.destinationLabel(r, q, tc.TenantID, hist[i].Destination)
	}
	out["history"] = hist
	write(w, http.StatusOK, out)
}

// destinationLabel turns "queue:<uuid>" / "agent:<uuid>" / "oncall:<uuid>" into a name of THIS company ("" when it no longer exists or the key is
// not one of those). The tenant filter is explicit on top of RLS.
func (h *Handler) destinationLabel(r *http.Request, q platformdb.Querier, tenant uuid.UUID, dest string) string {
	kind, raw, ok := strings.Cut(dest, ":")
	if !ok {
		return ""
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return ""
	}
	var name string
	switch kind {
	case "queue":
		err = q.QueryRow(r.Context(), `SELECT name FROM queues WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&name)
	case "oncall":
		err = q.QueryRow(r.Context(), `SELECT name FROM on_call_rosters WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&name)
	case "agent":
		err = q.QueryRow(r.Context(), `SELECT COALESCE(NULLIF(btrim(u.display_name),''), u.email, '') FROM users u JOIN memberships m ON m.user_id=u.id
			WHERE m.tenant_id=$1 AND u.id=$2 LIMIT 1`, tenant, id).Scan(&name)
	default:
		return ""
	}
	if err != nil {
		return ""
	}
	return name
}
