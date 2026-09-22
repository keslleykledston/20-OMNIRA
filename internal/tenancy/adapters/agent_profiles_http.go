package adapters

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// AgentProfilesHandler manages operational profiles. Availability below is per
// queue eligibility, not presence.
type AgentProfilesHandler struct {
	pool  *pgxpool.Pool
	audit auditports.AuditEventRepository
}

func NewAgentProfilesHandler(p *pgxpool.Pool, a auditports.AuditEventRepository) *AgentProfilesHandler {
	return &AgentProfilesHandler{p, a}
}

type agentProfile struct {
	ID           uuid.UUID         `json:"id"`
	MembershipID uuid.UUID         `json:"membership_id"`
	UserID       uuid.UUID         `json:"user_id"`
	Name         string            `json:"name"`
	Email        string            `json:"email"`
	Role         string            `json:"role"`
	Status       string            `json:"status"`
	Queues       []queueAssignment `json:"queues"`
}
type queueAssignment struct {
	ID        uuid.UUID `json:"id"`
	QueueID   uuid.UUID `json:"queue_id"`
	QueueName string    `json:"queue_name"`
	Available bool      `json:"available"`
	Capacity  int       `json:"capacity"`
}

func (h *AgentProfilesHandler) auth(r *http.Request, p string) (*uuid.UUID, error) {
	tc, e := (&TeamHandler{pool: h.pool}).authorize(r, p)
	if e != nil {
		return nil, e
	}
	return &tc.TenantID, nil
}
func (h *AgentProfilesHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, e := h.auth(r, "agent.read")
	if e != nil {
		respondAuthzError(w, e)
		return
	}
	rows, e := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `SELECT ap.id,ap.membership_id,u.id,COALESCE(u.display_name,''),COALESCE(u.email,''),ro.key,ap.status FROM agent_profiles ap JOIN memberships m ON m.id=ap.membership_id JOIN users u ON u.id=m.user_id JOIN roles ro ON ro.id=m.role_id WHERE ap.tenant_id=$1 ORDER BY u.email`, *tid)
	if e != nil {
		http.Error(w, "failed to list agents", 500)
		return
	}
	defer rows.Close()
	out := []agentProfile{}
	for rows.Next() {
		var x agentProfile
		if e = rows.Scan(&x.ID, &x.MembershipID, &x.UserID, &x.Name, &x.Email, &x.Role, &x.Status); e != nil {
			http.Error(w, "failed to read agents", 500)
			return
		}
		out = append(out, x)
	}
	if e = rows.Err(); e != nil {
		http.Error(w, "failed to read agents", 500)
		return
	}
	rows.Close() // pgx permits next query only after list rows are consumed.
	for i := range out {
		out[i].Queues, e = h.queueAssignments(r, *tid, out[i].ID)
		if e != nil {
			http.Error(w, "failed to list queue assignments", 500)
			return
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"items": out})
}

// Get returns the operational view of one profile. It deliberately does not
// expose human presence: available is queue-local routing eligibility only.
func (h *AgentProfilesHandler) Get(w http.ResponseWriter, r *http.Request) {
	tid, e := h.auth(r, "agent.read")
	if e != nil {
		respondAuthzError(w, e)
		return
	}
	id, e := uuid.Parse(r.PathValue("agent_profile_id"))
	if e != nil {
		http.Error(w, "invalid agent_profile_id", 400)
		return
	}
	var x agentProfile
	e = platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT ap.id,ap.membership_id,u.id,COALESCE(u.display_name,''),COALESCE(u.email,''),ro.key,ap.status
		FROM agent_profiles ap JOIN memberships m ON m.id=ap.membership_id
		JOIN users u ON u.id=m.user_id JOIN roles ro ON ro.id=m.role_id
		WHERE ap.tenant_id=$1 AND ap.id=$2`, *tid, id).Scan(&x.ID, &x.MembershipID, &x.UserID, &x.Name, &x.Email, &x.Role, &x.Status)
	if e == pgx.ErrNoRows {
		http.Error(w, "agent not found", 404)
		return
	}
	if e != nil {
		http.Error(w, "failed to get agent", 500)
		return
	}
	x.Queues, e = h.queueAssignments(r, *tid, x.ID)
	if e != nil {
		http.Error(w, "failed to list queue assignments", 500)
		return
	}
	json.NewEncoder(w).Encode(x)
}
func (h *AgentProfilesHandler) Create(w http.ResponseWriter, r *http.Request) {
	tid, e := h.auth(r, "agent.manage")
	if e != nil {
		respondAuthzError(w, e)
		return
	}
	var in struct {
		MembershipID uuid.UUID `json:"membership_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.MembershipID == uuid.Nil {
		http.Error(w, "membership_id required", 400)
		return
	}
	var id uuid.UUID
	e = platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `INSERT INTO agent_profiles(tenant_id,membership_id,status) SELECT $1,id,'active' FROM memberships WHERE tenant_id=$1 AND id=$2 AND status='active' ON CONFLICT(tenant_id,membership_id) DO UPDATE SET status='active',updated_at=now() RETURNING id`, *tid, in.MembershipID).Scan(&id)
	if e == pgx.ErrNoRows {
		http.Error(w, "membership not found", 404)
		return
	}
	if e != nil {
		http.Error(w, "failed to activate agent", 500)
		return
	}
	h.auditEvent(r, id, auditdomain.ActionAgentEnabled)
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "active"})
}
func (h *AgentProfilesHandler) Update(w http.ResponseWriter, r *http.Request) {
	tid, e := h.auth(r, "agent.manage")
	if e != nil {
		respondAuthzError(w, e)
		return
	}
	id, e := uuid.Parse(r.PathValue("agent_profile_id"))
	if e != nil {
		http.Error(w, "invalid agent_profile_id", 400)
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || (in.Status != "active" && in.Status != "disabled") {
		http.Error(w, "status must be active or disabled", 400)
		return
	}
	tag, e := platformdb.QuerierFromContext(r.Context(), h.pool).Exec(r.Context(), `UPDATE agent_profiles SET status=$3,updated_at=now() WHERE tenant_id=$1 AND id=$2`, *tid, id, in.Status)
	if e != nil {
		http.Error(w, "failed to update agent", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "agent not found", 404)
		return
	}
	act := auditdomain.ActionAgentEnabled
	if in.Status == "disabled" {
		act = auditdomain.ActionAgentDisabled
	}
	h.auditEvent(r, id, act)
	w.WriteHeader(204)
}
func (h *AgentProfilesHandler) ListQueues(w http.ResponseWriter, r *http.Request) {
	tid, e := h.auth(r, "agent.read")
	if e != nil {
		respondAuthzError(w, e)
		return
	}
	id, e := uuid.Parse(r.PathValue("agent_profile_id"))
	if e != nil {
		http.Error(w, "invalid agent_profile_id", 400)
		return
	}
	items, e := h.queueAssignments(r, *tid, id)
	if e != nil {
		http.Error(w, "failed to list queue assignments", 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"items": items})
}
func (h *AgentProfilesHandler) AddQueue(w http.ResponseWriter, r *http.Request) {
	tid, e := h.auth(r, "agent.manage")
	if e != nil {
		respondAuthzError(w, e)
		return
	}
	profileID, e := uuid.Parse(r.PathValue("agent_profile_id"))
	if e != nil {
		http.Error(w, "invalid agent_profile_id", 400)
		return
	}
	var in struct {
		QueueID   uuid.UUID `json:"queue_id"`
		Available *bool     `json:"available"`
		Capacity  *int      `json:"capacity"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.QueueID == uuid.Nil {
		http.Error(w, "queue_id required", 400)
		return
	}
	available := true
	if in.Available != nil {
		available = *in.Available
	}
	capacity := 1
	if in.Capacity != nil {
		capacity = *in.Capacity
	}
	if capacity < 1 {
		http.Error(w, "capacity must be positive", 400)
		return
	}
	var id uuid.UUID
	e = platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		INSERT INTO queue_members(tenant_id,queue_id,user_id,available,capacity)
		SELECT ap.tenant_id,$2,m.user_id,$3,$4 FROM agent_profiles ap
		JOIN memberships m ON m.id=ap.membership_id AND m.tenant_id=ap.tenant_id AND m.status='active'
		JOIN queues q ON q.id=$2 AND q.tenant_id=ap.tenant_id
		WHERE ap.tenant_id=$1 AND ap.id=$5 AND ap.status='active'
		ON CONFLICT(tenant_id,queue_id,user_id) DO UPDATE SET active=true,available=EXCLUDED.available,capacity=EXCLUDED.capacity
		RETURNING id`, *tid, in.QueueID, available, capacity, profileID).Scan(&id)
	if e == pgx.ErrNoRows {
		http.Error(w, "active agent or queue not found", 404)
		return
	}
	if e != nil {
		http.Error(w, "failed to assign queue", 500)
		return
	}
	h.auditEvent(r, profileID, auditdomain.ActionAgentQueueAssigned)
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(map[string]any{"id": id})
}
func (h *AgentProfilesHandler) UpdateQueue(w http.ResponseWriter, r *http.Request) {
	tid, e := h.auth(r, "agent.manage")
	if e != nil {
		respondAuthzError(w, e)
		return
	}
	profileID, e := uuid.Parse(r.PathValue("agent_profile_id"))
	if e != nil {
		http.Error(w, "invalid agent_profile_id", 400)
		return
	}
	memberID, e := uuid.Parse(r.PathValue("queue_member_id"))
	if e != nil {
		http.Error(w, "invalid queue_member_id", 400)
		return
	}
	var in struct {
		Available *bool `json:"available"`
		Capacity  *int  `json:"capacity"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || (in.Available == nil && in.Capacity == nil) {
		http.Error(w, "availability or capacity required", 400)
		return
	}
	if in.Capacity != nil && *in.Capacity < 1 {
		http.Error(w, "capacity must be positive", 400)
		return
	}
	tag, e := platformdb.QuerierFromContext(r.Context(), h.pool).Exec(r.Context(), `
		UPDATE queue_members qm SET available=COALESCE($4,qm.available),capacity=COALESCE($5,qm.capacity)
		FROM agent_profiles ap JOIN memberships m ON m.id=ap.membership_id AND m.tenant_id=ap.tenant_id
		WHERE qm.id=$3 AND qm.tenant_id=$1 AND ap.id=$2 AND ap.tenant_id=qm.tenant_id AND m.user_id=qm.user_id`, *tid, profileID, memberID, in.Available, in.Capacity)
	if e != nil {
		http.Error(w, "failed to update queue assignment", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "queue assignment not found", 404)
		return
	}
	if in.Available != nil {
		h.auditEvent(r, profileID, auditdomain.ActionAgentQueueAvailability)
	}
	if in.Capacity != nil {
		h.auditEvent(r, profileID, auditdomain.ActionAgentQueueCapacity)
	}
	w.WriteHeader(204)
}
func (h *AgentProfilesHandler) RemoveQueue(w http.ResponseWriter, r *http.Request) {
	tid, e := h.auth(r, "agent.manage")
	if e != nil {
		respondAuthzError(w, e)
		return
	}
	profileID, e := uuid.Parse(r.PathValue("agent_profile_id"))
	if e != nil {
		http.Error(w, "invalid agent_profile_id", 400)
		return
	}
	memberID, e := uuid.Parse(r.PathValue("queue_member_id"))
	if e != nil {
		http.Error(w, "invalid queue_member_id", 400)
		return
	}
	tag, e := platformdb.QuerierFromContext(r.Context(), h.pool).Exec(r.Context(), `
		UPDATE queue_members qm SET active=false FROM agent_profiles ap JOIN memberships m ON m.id=ap.membership_id AND m.tenant_id=ap.tenant_id
		WHERE qm.id=$3 AND qm.tenant_id=$1 AND ap.id=$2 AND ap.tenant_id=qm.tenant_id AND m.user_id=qm.user_id`, *tid, profileID, memberID)
	if e != nil {
		http.Error(w, "failed to remove queue assignment", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "queue assignment not found", 404)
		return
	}
	h.auditEvent(r, profileID, auditdomain.ActionAgentQueueRemoved)
	w.WriteHeader(204)
}
func (h *AgentProfilesHandler) queueAssignments(r *http.Request, tenantID, profileID uuid.UUID) ([]queueAssignment, error) {
	rows, e := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT qm.id,qm.queue_id,q.name,qm.available,qm.capacity FROM agent_profiles ap
		JOIN memberships m ON m.id=ap.membership_id AND m.tenant_id=ap.tenant_id
		JOIN queue_members qm ON qm.tenant_id=ap.tenant_id AND qm.user_id=m.user_id AND qm.active
		JOIN queues q ON q.id=qm.queue_id AND q.tenant_id=qm.tenant_id
		WHERE ap.tenant_id=$1 AND ap.id=$2 ORDER BY q.name`, tenantID, profileID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []queueAssignment{}
	for rows.Next() {
		var x queueAssignment
		if e = rows.Scan(&x.ID, &x.QueueID, &x.QueueName, &x.Available, &x.Capacity); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (h *AgentProfilesHandler) auditEvent(r *http.Request, id uuid.UUID, a auditdomain.AuditAction) {
	if h.audit == nil {
		return
	}
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil {
		return
	}
	_ = h.audit.Store(r.Context(), &auditdomain.AuditEvent{ID: uuid.New(), TenantID: tc.TenantID, ActorID: tc.ActorID, Action: a, ResourceType: auditdomain.ResourceAgentProfile, ResourceID: id, Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), Metadata: map[string]any{}, CreatedAt: time.Now().UTC()})
}
