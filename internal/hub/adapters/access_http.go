package adapters

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/access"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// AccessHandler is the Access panel's HTTP surface (ADR-0039): instances, their administrators, the hub's agents and
// what each agent may do where. Behind OMNIRA_HUB_ACCESS_API_ENABLED, authn and the caller's own RLS session.
//
// Only an active ADMIN of the hub in the path may call it (asked of the database for the authenticated user, again inside
// every writing transaction). Everything else, including an agent of the same hub, is one uniform 404. Authorizing an
// agent for more than one instance is exactly this API; a company administrator has no route here.
type AccessHandler struct {
	pool *pgxpool.Pool
	svc  *access.Service
}

func NewAccessHandler(pool *pgxpool.Pool) (*AccessHandler, error) {
	svc, err := access.New(pool)
	if err != nil {
		return nil, err
	}
	return &AccessHandler{pool: pool, svc: svc}, nil
}

func (h *AccessHandler) scope(w http.ResponseWriter, r *http.Request) (actor, hub uuid.UUID, ok bool) {
	actor, hub, ok = requestScope(w, r)
	if !ok {
		return
	}
	var admin bool
	if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `SELECT is_hub_admin($1, $2)`, hub, actor).Scan(&admin); err != nil {
		log.Printf("hub access: admin check: %v", err)
		httpError(w, "internal server error", http.StatusInternalServerError)
		return uuid.Nil, uuid.Nil, false
	}
	if !admin {
		httpError(w, "not found", http.StatusNotFound)
		return uuid.Nil, uuid.Nil, false
	}
	return actor, hub, true
}

func writeAccessPanelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, access.ErrForbidden), errors.Is(err, access.ErrNotFound):
		httpError(w, "not found", http.StatusNotFound)
	case errors.Is(err, access.ErrInvalid):
		httpError(w, err.Error(), http.StatusUnprocessableEntity)
	default:
		log.Printf("hub access: %v", err)
		httpError(w, "internal server error", http.StatusInternalServerError)
	}
}

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil || id == uuid.Nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

// GET /api/v1/hubs/{hub_id}/access
func (h *AccessHandler) Overview(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Overview(r.Context(), actor, hub)
	if err != nil {
		writeAccessPanelError(w, err)
		return
	}
	writeJSON(w, out)
}

type emailRequest struct {
	Email string `json:"email"`
}

// POST /api/v1/hubs/{hub_id}/access/agents
func (h *AccessHandler) AddAgent(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	var body emailRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	p, err := h.svc.AddAgent(r.Context(), actor, hub, body.Email)
	if err != nil {
		writeAccessPanelError(w, err)
		return
	}
	writeJSON(w, p)
}

type inviteRequest struct {
	Email  string                `json:"email"`
	Access []access.InviteAccess `json:"access"`
}

// POST /api/v1/hubs/{hub_id}/access/invitations
// One gesture for both cases: the e-mail of an existing account becomes an agent now ({"status":"applied"}); the e-mail of
// a person without an account is kept for 14 days and applied at their first sign-in ({"status":"pending"}).
func (h *AccessHandler) Invite(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	var body inviteRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	res, err := h.svc.Invite(r.Context(), actor, hub, body.Email, body.Access)
	if err != nil {
		writeAccessPanelError(w, err)
		return
	}
	writeJSON(w, res)
}

// DELETE /api/v1/hubs/{hub_id}/access/invitations/{invitation_id}
func (h *AccessHandler) RevokeInvitation(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "invitation_id")
	if !ok {
		return
	}
	if err := h.svc.RevokeInvitation(r.Context(), actor, hub, id); err != nil {
		writeAccessPanelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/hubs/{hub_id}/access/agents/{user_id}
func (h *AccessHandler) RemoveAgent(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	user, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	if err := h.svc.RemoveAgent(r.Context(), actor, hub, user); err != nil {
		writeAccessPanelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type setAccessRequest struct {
	Mode       string     `json:"mode"` // none | read | reply
	ValidUntil *time.Time `json:"valid_until"`
}

// PUT /api/v1/hubs/{hub_id}/access/agents/{user_id}/instances/{tenant_id}
func (h *AccessHandler) SetAccess(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	user, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	tenant, ok := pathUUID(w, r, "tenant_id")
	if !ok {
		return
	}
	var body setAccessRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	if err := h.svc.SetAccess(r.Context(), actor, hub, user, tenant, body.Mode, body.ValidUntil); err != nil {
		writeAccessPanelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type setManageRequest struct {
	CanManage bool `json:"can_manage"`
}

// PUT /api/v1/hubs/{hub_id}/access/agents/{user_id}/instances/{tenant_id}/management
// The "Gerenciar" switch (ADR-0038 phase 3): needs a live grant; matters only where the contract delegates a management scope.
func (h *AccessHandler) SetManage(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	user, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	tenant, ok := pathUUID(w, r, "tenant_id")
	if !ok {
		return
	}
	var body setManageRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	if err := h.svc.SetManage(r.Context(), actor, hub, user, tenant, body.CanManage); err != nil {
		writeAccessPanelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/hubs/{hub_id}/access/instances/{tenant_id}/admins
func (h *AccessHandler) AddInstanceAdmin(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	tenant, ok := pathUUID(w, r, "tenant_id")
	if !ok {
		return
	}
	var body emailRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	p, err := h.svc.AddInstanceAdmin(r.Context(), actor, hub, tenant, body.Email)
	if err != nil {
		writeAccessPanelError(w, err)
		return
	}
	writeJSON(w, p)
}

// DELETE /api/v1/hubs/{hub_id}/access/instances/{tenant_id}/admins/{user_id}
func (h *AccessHandler) RemoveInstanceAdmin(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	tenant, ok := pathUUID(w, r, "tenant_id")
	if !ok {
		return
	}
	user, ok := pathUUID(w, r, "user_id")
	if !ok {
		return
	}
	if err := h.svc.RemoveInstanceAdmin(r.Context(), actor, hub, tenant, user); err != nil {
		writeAccessPanelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
