package adapters

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
)

// SLAPolicy is one policy of one scope. Budgets are in seconds; absent = that metric is not measured by this policy.
type SLAPolicy struct {
	ScopeKind            string     `json:"scope_kind"` // tenant | sector | queue
	ScopeID              *uuid.UUID `json:"scope_id,omitempty"`
	FirstResponseSeconds *int       `json:"first_response_seconds,omitempty"`
	QueueWaitSeconds     *int       `json:"queue_wait_seconds,omitempty"`
	AssignmentSeconds    *int       `json:"assignment_seconds,omitempty"`
	ResolutionSeconds    *int       `json:"resolution_seconds,omitempty"`
	RiskPercent          int        `json:"risk_percent"`
	Counting             string     `json:"counting"` // wall | business
	Active               bool       `json:"active"`
	Revision             int        `json:"revision,omitempty"`
}

func validSeconds(v *int, max int) bool { return v == nil || (*v >= 1 && *v <= max) }

func (p SLAPolicy) validate() string {
	switch p.ScopeKind {
	case "tenant":
		if p.ScopeID != nil {
			return "a tenant policy has no scope_id"
		}
	case "sector", "queue":
		if p.ScopeID == nil {
			return "scope_id is required for a " + p.ScopeKind + " policy"
		}
	default:
		return "scope_kind must be tenant, sector or queue"
	}
	if p.FirstResponseSeconds == nil && p.QueueWaitSeconds == nil && p.AssignmentSeconds == nil && p.ResolutionSeconds == nil {
		return "at least one metric is required"
	}
	if !validSeconds(p.FirstResponseSeconds, 30*86400) || !validSeconds(p.QueueWaitSeconds, 30*86400) || !validSeconds(p.AssignmentSeconds, 30*86400) || !validSeconds(p.ResolutionSeconds, 90*86400) {
		return "a budget must be between 1 second and 30 days (resolution up to 90)"
	}
	if p.RiskPercent < 1 || p.RiskPercent > 99 {
		return "risk_percent must be between 1 and 99"
	}
	if p.Counting != "wall" && p.Counting != "business" {
		return "counting must be wall or business"
	}
	return ""
}

// ListSLAPolicies: GET /sla-policies.
func (h *Handler) ListSLAPolicies(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	rows, err := q.Query(r.Context(), `SELECT scope_kind, COALESCE(sector_id, queue_id), first_response_seconds, queue_wait_seconds, assignment_seconds, resolution_seconds, risk_percent, counting, active, revision
		FROM sla_policies WHERE tenant_id=$1 ORDER BY CASE scope_kind WHEN 'tenant' THEN 0 WHEN 'sector' THEN 1 ELSE 2 END, updated_at LIMIT 500`, tc.TenantID)
	if err != nil {
		http.Error(w, "failed to list SLA policies", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []SLAPolicy{}
	for rows.Next() {
		var p SLAPolicy
		if err := rows.Scan(&p.ScopeKind, &p.ScopeID, &p.FirstResponseSeconds, &p.QueueWaitSeconds, &p.AssignmentSeconds, &p.ResolutionSeconds, &p.RiskPercent, &p.Counting, &p.Active, &p.Revision); err != nil {
			http.Error(w, "failed to read SLA policies", http.StatusInternalServerError)
			return
		}
		items = append(items, p)
	}
	write(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// PutSLAPolicy: PUT /sla-policies — creates or replaces the policy of one scope (a new revision each time). Clocks already running keep
// the budget they started with; the new numbers apply to clocks that start afterwards (a change must not move a deadline under people).
func (h *Handler) PutSLAPolicy(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	var in struct {
		SLAPolicy
		ExpectedRevision *int `json:"expected_revision,omitempty"`
	}
	in.Active = true
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.RiskPercent == 0 {
		in.RiskPercent = 80
	}
	if in.Counting == "" {
		in.Counting = "wall"
	}
	if msg := in.validate(); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	var sectorID, queueID *uuid.UUID
	switch in.ScopeKind {
	case "sector":
		sectorID = in.ScopeID
		if !h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM sectors WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, *in.ScopeID) {
			http.Error(w, "sector not found", http.StatusNotFound)
			return
		}
	case "queue":
		queueID = in.ScopeID
		if !h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM queues WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, *in.ScopeID) {
			http.Error(w, "queue not found", http.StatusNotFound)
			return
		}
	}
	var current int
	var id uuid.UUID
	err := q.QueryRow(r.Context(), `SELECT id, revision FROM sla_policies WHERE tenant_id=$1 AND scope_kind=$2 AND sector_id IS NOT DISTINCT FROM $3 AND queue_id IS NOT DISTINCT FROM $4 FOR UPDATE`,
		tc.TenantID, in.ScopeKind, sectorID, queueID).Scan(&id, &current)
	rev := 1
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if in.ExpectedRevision != nil && *in.ExpectedRevision != 0 {
			http.Error(w, "the policy was changed by someone else; reload it and apply your change again", http.StatusConflict)
			return
		}
		if err := attempt(r, q, func() error {
			_, e := q.Exec(r.Context(), `INSERT INTO sla_policies (tenant_id, scope_kind, sector_id, queue_id, first_response_seconds, queue_wait_seconds, assignment_seconds, resolution_seconds, risk_percent, counting, active, updated_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, tc.TenantID, in.ScopeKind, sectorID, queueID, in.FirstResponseSeconds, in.QueueWaitSeconds, in.AssignmentSeconds, in.ResolutionSeconds, in.RiskPercent, in.Counting, in.Active, tc.ActorID)
			return e
		}); err != nil {
			if conflict(w, err, "a policy for this scope already exists") {
				return
			}
			http.Error(w, "failed to save the SLA policy", http.StatusInternalServerError)
			return
		}
	case err != nil:
		http.Error(w, "failed to read the SLA policy", http.StatusInternalServerError)
		return
	default:
		if in.ExpectedRevision != nil && *in.ExpectedRevision != current {
			http.Error(w, "the policy was changed by someone else; reload it and apply your change again", http.StatusConflict)
			return
		}
		rev = current + 1
		if _, err := q.Exec(r.Context(), `UPDATE sla_policies SET first_response_seconds=$3, queue_wait_seconds=$4, assignment_seconds=$5, resolution_seconds=$6, risk_percent=$7, counting=$8, active=$9, revision=$10, updated_by=$11, updated_at=now()
			WHERE tenant_id=$1 AND id=$2`, tc.TenantID, id, in.FirstResponseSeconds, in.QueueWaitSeconds, in.AssignmentSeconds, in.ResolutionSeconds, in.RiskPercent, in.Counting, in.Active, rev, tc.ActorID); err != nil {
			http.Error(w, "failed to save the SLA policy", http.StatusInternalServerError)
			return
		}
	}
	subject := tc.TenantID
	if in.ScopeID != nil {
		subject = *in.ScopeID
	}
	h.record(r, auditdomain.ResourceSLAPolicy, subject, auditdomain.ActionSLASaved, map[string]any{"scope": in.ScopeKind, "revision": rev, "counting": in.Counting})
	in.Revision = rev
	write(w, http.StatusOK, in.SLAPolicy)
}

// DeleteSLAPolicy: DELETE /sla-policies?scope_kind=&scope_id=.
func (h *Handler) DeleteSLAPolicy(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	kind := r.URL.Query().Get("scope_kind")
	var scope *uuid.UUID
	if raw := r.URL.Query().Get("scope_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "invalid scope_id", http.StatusBadRequest)
			return
		}
		scope = &id
	}
	var sectorID, queueID *uuid.UUID
	switch kind {
	case "tenant":
	case "sector":
		sectorID = scope
	case "queue":
		queueID = scope
	default:
		http.Error(w, "scope_kind must be tenant, sector or queue", http.StatusBadRequest)
		return
	}
	tag, err := q.Exec(r.Context(), `DELETE FROM sla_policies WHERE tenant_id=$1 AND scope_kind=$2 AND sector_id IS NOT DISTINCT FROM $3 AND queue_id IS NOT DISTINCT FROM $4`, tc.TenantID, kind, sectorID, queueID)
	if err != nil {
		http.Error(w, "failed to remove the SLA policy", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() != 1 {
		http.Error(w, "policy not found", http.StatusNotFound)
		return
	}
	subject := tc.TenantID
	if scope != nil {
		subject = *scope
	}
	h.record(r, auditdomain.ResourceSLAPolicy, subject, auditdomain.ActionSLARemoved, map[string]any{"scope": kind})
	w.WriteHeader(http.StatusNoContent)
}

type slaClockItem struct {
	ConversationID uuid.UUID `json:"conversation_id"`
	Metric         string    `json:"metric"`
	State          string    `json:"state"` // running | at_risk | breached
	StartedAt      time.Time `json:"started_at"`
	RiskAt         time.Time `json:"risk_at"`
	DeadlineAt     time.Time `json:"deadline_at"`
	Counting       string    `json:"counting"`
}

// QueueSLA: GET /queues/{queue_id}/sla — what is running, at risk or breached among the OPEN conversations of the queue (ids and times, no
// content), the nearest deadlines first.
func (h *Handler) QueueSLA(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	queueID, ok := pathUUID(w, r, "queue_id")
	if !ok {
		return
	}
	if !h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM queues WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, queueID) {
		http.Error(w, "queue not found", http.StatusNotFound)
		return
	}
	// ADR-0043 S3: with the company's sector scope on, a person who does not see every queue gets only the clocks of the conversations they may see
	restricted, err := tenancyadapters.SectorScopeRestricts(r.Context(), q, tc)
	if err != nil {
		http.Error(w, "failed to read the SLA", http.StatusInternalServerError)
		return
	}
	rows, err := q.Query(r.Context(), `SELECT s.conversation_id, s.metric,
		CASE WHEN s.state='breached' THEN 'breached' WHEN now() >= s.risk_at THEN 'at_risk' ELSE 'running' END, s.started_at, s.risk_at, s.deadline_at, s.counting
		FROM conversation_sla s JOIN conversations c ON c.tenant_id=s.tenant_id AND c.id=s.conversation_id
		WHERE s.tenant_id=$1 AND c.queue_id=$2 AND c.status='open' AND s.state IN ('running','breached')
		  AND (NOT $3::boolean OR conversation_in_sector_scope(c.tenant_id, $4, c.id))
		ORDER BY s.deadline_at, s.conversation_id LIMIT 200`, tc.TenantID, queueID, restricted, tc.ActorID)
	if err != nil {
		http.Error(w, "failed to read the SLA", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []slaClockItem{}
	counts := map[string]int{"running": 0, "at_risk": 0, "breached": 0}
	for rows.Next() {
		var it slaClockItem
		if err := rows.Scan(&it.ConversationID, &it.Metric, &it.State, &it.StartedAt, &it.RiskAt, &it.DeadlineAt, &it.Counting); err != nil {
			http.Error(w, "failed to read the SLA", http.StatusInternalServerError)
			return
		}
		counts[it.State]++
		items = append(items, it)
	}
	write(w, http.StatusOK, map[string]any{"queue_id": queueID, "counts": counts, "items": items})
}
