package adapters

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// InboxSettingsHandler exposes the per-tenant Inbox display settings: today the two
// "customer is waiting" thresholds (minutes) that colour the list chip. Everyone who
// belongs to the tenant reads them (the Inbox needs them to paint); changing them is
// tenant.manage, and the tenants UPDATE policy independently allows only an admin.
type InboxSettingsHandler struct {
	pool  *pgxpool.Pool
	audit auditports.AuditEventRepository
}

func NewInboxSettingsHandler(p *pgxpool.Pool, a auditports.AuditEventRepository) *InboxSettingsHandler {
	return &InboxSettingsHandler{pool: p, audit: a}
}

const (
	defaultWaitWarnMinutes   = 30
	defaultWaitDangerMinutes = 120
	maxWaitDangerMinutes     = 10080 // one week; mirrors tenants_wait_thresholds_chk
)

type inboxSettings struct {
	WaitWarnMinutes   int `json:"wait_warn_minutes"`
	WaitDangerMinutes int `json:"wait_danger_minutes"`
}

func (h *InboxSettingsHandler) authorize(w http.ResponseWriter, r *http.Request, permission string) (*tenancydomain.TenantContext, bool) {
	tc, err := (&TeamHandler{pool: h.pool}).authorize(r, permission)
	if err != nil {
		respondAuthzError(w, err)
		return nil, false
	}
	return tc, true
}

func writeInboxSettings(w http.ResponseWriter, s inboxSettings) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s)
}

// Get returns the tenant's thresholds.
func (h *InboxSettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r, "tenant.read")
	if !ok {
		return
	}
	var s inboxSettings
	err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(),
		`SELECT wait_warn_minutes, wait_danger_minutes FROM tenants WHERE id = $1`, tc.TenantID).
		Scan(&s.WaitWarnMinutes, &s.WaitDangerMinutes)
	if err != nil {
		http.Error(w, "failed to read settings", http.StatusInternalServerError)
		return
	}
	writeInboxSettings(w, s)
}

// Put replaces both thresholds. Both are required so a client can never send half a pair
// and leave the other in a state the user did not see.
func (h *InboxSettingsHandler) Put(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r, "tenant.manage")
	if !ok {
		return
	}
	var req struct {
		WaitWarnMinutes   *int `json:"wait_warn_minutes"`
		WaitDangerMinutes *int `json:"wait_danger_minutes"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.WaitWarnMinutes == nil || req.WaitDangerMinutes == nil {
		http.Error(w, "wait_warn_minutes and wait_danger_minutes are required integers", http.StatusBadRequest)
		return
	}
	next := inboxSettings{WaitWarnMinutes: *req.WaitWarnMinutes, WaitDangerMinutes: *req.WaitDangerMinutes}
	if next.WaitWarnMinutes < 1 || next.WaitDangerMinutes <= next.WaitWarnMinutes || next.WaitDangerMinutes > maxWaitDangerMinutes {
		http.Error(w, "attention must be at least 1 minute, critical must be greater than attention and at most 10080", http.StatusUnprocessableEntity)
		return
	}

	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var prev inboxSettings
	if err := q.QueryRow(r.Context(), `SELECT wait_warn_minutes, wait_danger_minutes FROM tenants WHERE id = $1`, tc.TenantID).
		Scan(&prev.WaitWarnMinutes, &prev.WaitDangerMinutes); err != nil {
		http.Error(w, "failed to read settings", http.StatusInternalServerError)
		return
	}
	tag, err := q.Exec(r.Context(),
		`UPDATE tenants SET wait_warn_minutes = $2, wait_danger_minutes = $3, updated_at = now() WHERE id = $1`,
		tc.TenantID, next.WaitWarnMinutes, next.WaitDangerMinutes)
	if err != nil {
		http.Error(w, "failed to save settings", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() != 1 {
		// RLS refused the update (not an admin of this tenant): never report success.
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	h.record(r, prev, next)
	writeInboxSettings(w, next)
}

func (h *InboxSettingsHandler) record(r *http.Request, prev, next inboxSettings) {
	if h.audit == nil {
		return
	}
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, auditdomain.ActionTenantInboxSettings, auditdomain.ResourceTenant, tc.TenantID, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	ev.SetMetadata("wait_warn_minutes_from", prev.WaitWarnMinutes)
	ev.SetMetadata("wait_warn_minutes_to", next.WaitWarnMinutes)
	ev.SetMetadata("wait_danger_minutes_from", prev.WaitDangerMinutes)
	ev.SetMetadata("wait_danger_minutes_to", next.WaitDangerMinutes)
	_ = h.audit.Store(r.Context(), ev)
}
