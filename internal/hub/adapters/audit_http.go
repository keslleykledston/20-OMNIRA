package adapters

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/auditlog"
)

// AuditHandler serves the hub administrator's audit view (ADR-0038 §6). Behind the Access API flag; only an active admin of the hub in the path
// gets past the service (proven in the reading transaction); everything else is one uniform 404.
type AuditHandler struct{ svc *auditlog.Service }

func NewAuditHandler(pool *pgxpool.Pool) *AuditHandler { return &AuditHandler{svc: auditlog.New(pool)} }

// GET /api/v1/hubs/{hub_id}/audit?tenant=<instance>&before=<cursor>&limit=<n>
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	var tenant *uuid.UUID
	if v := r.URL.Query().Get("tenant"); v != "" {
		t, err := uuid.Parse(v)
		if err != nil {
			httpError(w, "invalid request", http.StatusBadRequest)
			return
		}
		tenant = &t
	}
	var before *auditlog.Cursor
	if v := r.URL.Query().Get("before"); v != "" {
		c, err := auditlog.ParseCursor(v)
		if err != nil {
			httpError(w, "invalid request", http.StatusBadRequest)
			return
		}
		before = c
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httpError(w, "invalid request", http.StatusBadRequest)
			return
		}
		limit = n
	}
	page, err := h.svc.List(r.Context(), actor, hub, tenant, before, limit)
	if errors.Is(err, auditlog.ErrForbidden) {
		httpError(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("hub audit: %v", err)
		httpError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, page)
}
