package adapters

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/entitlements"
	"github.com/omnira/omnira/internal/hub/companies"
	"github.com/omnira/omnira/internal/hub/provisioning"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// AdminHandler is the control plane's HTTP surface (ADR-0038 phase 1): list, create and switch companies, issue and
// withdraw their capabilities. Behind OMNIRA_HUB_ADMIN_API_ENABLED, authn and the caller's own RLS session.
//
// Who may call it is decided from the database for the AUTHENTICATED user, never from the request: an active platform
// operator (platform_operators) who is a hub_admin of the hub in the path. Everything else is one uniform 404, so the
// API neither confirms that a hub exists nor that a person is an operator. The company in a path is only meaningful
// once the service has found its contract with that hub.
type AdminHandler struct {
	pool *pgxpool.Pool
	svc  *companies.Service
}

func NewAdminHandler(pool *pgxpool.Pool) *AdminHandler {
	return &AdminHandler{pool: pool, svc: companies.New(pool)}
}

// scope = requestScope + "is an active operator and admin of this hub", asked in the caller's own session.
func (h *AdminHandler) scope(w http.ResponseWriter, r *http.Request) (operator, hub uuid.UUID, ok bool) {
	operator, hub, ok = requestScope(w, r)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	isOp, err := provisioning.IsPlatformOperator(r.Context(), q, operator)
	if err != nil {
		log.Printf("hub admin: operator check: %v", err)
		httpError(w, "internal server error", http.StatusInternalServerError)
		return uuid.Nil, uuid.Nil, false
	}
	var isAdmin bool
	if isOp {
		if err := q.QueryRow(r.Context(), `SELECT is_hub_admin($1, $2)`, hub, operator).Scan(&isAdmin); err != nil {
			log.Printf("hub admin: hub admin check: %v", err)
			httpError(w, "internal server error", http.StatusInternalServerError)
			return uuid.Nil, uuid.Nil, false
		}
	}
	if !isOp || !isAdmin {
		httpError(w, "not found", http.StatusNotFound)
		return uuid.Nil, uuid.Nil, false
	}
	return operator, hub, true
}

func writeAdminError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, companies.ErrForbidden):
		httpError(w, "not found", http.StatusNotFound)
	case errors.Is(err, companies.ErrKeyMismatch):
		httpError(w, "this Idempotency-Key was already used for a different request", http.StatusUnprocessableEntity)
	case errors.Is(err, companies.ErrInvalid):
		httpError(w, err.Error(), http.StatusUnprocessableEntity)
	default:
		log.Printf("hub admin: %v", err)
		httpError(w, "internal server error", http.StatusInternalServerError)
	}
}

func (h *AdminHandler) ListCompanies(w http.ResponseWriter, r *http.Request) {
	operator, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	items, err := h.svc.List(r.Context(), operator, hub)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items, "capabilities": entitlements.Registry})
}

type createCompanyRequest struct {
	LegalName         string `json:"legal_name"`
	TradeName         string `json:"trade_name"`
	TaxID             string `json:"tax_id"`
	InitialAdminEmail string `json:"initial_admin_email"`
}

func (h *AdminHandler) CreateCompany(w http.ResponseWriter, r *http.Request) {
	operator, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpError(w, "Idempotency-Key header is required", http.StatusBadRequest)
		return
	}
	var body createCompanyRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	co, replayed, err := h.svc.Create(r.Context(), operator, hub, companies.CreateInput{
		LegalName: body.LegalName, TradeName: body.TradeName, TaxID: body.TaxID, InitialAdminEmail: body.InitialAdminEmail,
	}, key)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(co)
}

type updateCompanyRequest struct {
	Status       string          `json:"status"`
	Capabilities map[string]bool `json:"capabilities"`
	// ManagementScopes: absent = unchanged; [] = withdraw every delegation (ADR-0038 phase 3)
	ManagementScopes *[]string `json:"management_scopes"`
}

func (h *AdminHandler) UpdateCompany(w http.ResponseWriter, r *http.Request) {
	operator, hub, ok := h.scope(w, r)
	if !ok {
		return
	}
	tenant, err := uuid.Parse(r.PathValue("tenant_id"))
	if err != nil || tenant == uuid.Nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	var body updateCompanyRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	co, err := h.svc.Update(r.Context(), operator, hub, tenant, companies.UpdateInput{Status: body.Status, Capabilities: body.Capabilities, ManagementScopes: body.ManagementScopes})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, co)
}
