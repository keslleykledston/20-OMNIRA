package adapters

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	groupsapp "github.com/omnira/omnira/internal/groups/application"
	"github.com/omnira/omnira/internal/hub/provisioning"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/ratelimit"
)

// SLAAlertFeatureFlagHandler is the admin console's control for the ADR-0058 operator kill switch — a
// button in place of editing OMNIRA_GROUP_SLA_ALERT_ENABLED and restarting the worker. Platform-wide,
// never tenant-scoped: authority is IsPlatformOperator alone, decided from the caller's OWN session,
// exactly like internal/platformrelease/updatemgr.HTTPHandler.scope. A denial here is the same uniform
// 404 as "endpoint doesn't exist" — no enumeration oracle.
type SLAAlertFeatureFlagHandler struct {
	pool *pgxpool.Pool
	repo *PostgresSLAAlertFeatureFlagRepository
}

func NewSLAAlertFeatureFlagHandler(pool *pgxpool.Pool) *SLAAlertFeatureFlagHandler {
	return &SLAAlertFeatureFlagHandler{pool: pool, repo: NewPostgresSLAAlertFeatureFlagRepository(pool)}
}

func (h *SLAAlertFeatureFlagHandler) scope(w http.ResponseWriter, r *http.Request) (operator uuid.UUID, ok bool) {
	principal, err := authn.FromContext(r.Context())
	if err != nil || principal.UserID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return uuid.Nil, false
	}
	if !ratelimit.EnforceUser(w, r, principal.UserID) {
		return uuid.Nil, false
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	isOp, err := provisioning.IsPlatformOperator(r.Context(), q, principal.UserID)
	if err != nil {
		log.Printf("groups: sla alert flag operator check: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return uuid.Nil, false
	}
	if !isOp {
		http.Error(w, "not found", http.StatusNotFound)
		return uuid.Nil, false
	}
	return principal.UserID, true
}

func (h *SLAAlertFeatureFlagHandler) Get(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.scope(w, r); !ok {
		return
	}
	state, err := h.repo.Get(r.Context())
	if err != nil {
		log.Printf("groups: sla alert flag get: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

type setSLAAlertFeatureFlagRequest struct {
	Enabled bool `json:"enabled"`
}

func (h *SLAAlertFeatureFlagHandler) Set(w http.ResponseWriter, r *http.Request) {
	operator, ok := h.scope(w, r)
	if !ok {
		return
	}
	var req setSLAAlertFeatureFlagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := h.repo.Set(r.Context(), req.Enabled, operator); err != nil {
		if errors.Is(err, groupsapp.ErrNotPlatformOperator) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		log.Printf("groups: sla alert flag set: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	state, err := h.repo.Get(r.Context())
	if err != nil {
		log.Printf("groups: sla alert flag get after set: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, state)
}
