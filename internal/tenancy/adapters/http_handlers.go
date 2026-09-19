package adapters

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/tenancy/application"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// TenantAPIHandler — handlers HTTP para Tenant API.
type TenantAPIHandler struct {
	tenantSvc    *application.TenantService
	membershipSvc *application.MembershipService
}

// NewTenantAPIHandler — cria um novo TenantAPIHandler.
func NewTenantAPIHandler(
	tenantSvc *application.TenantService,
	membershipSvc *application.MembershipService,
) *TenantAPIHandler {
	return &TenantAPIHandler{
		tenantSvc:     tenantSvc,
		membershipSvc: membershipSvc,
	}
}

// TenantResponse — resposta de um tenant (para JSON).
type TenantResponse struct {
	ID               uuid.UUID `json:"id"`
	LegalName        string    `json:"legal_name"`
	TradeName        *string   `json:"trade_name,omitempty"`
	TaxID            *string   `json:"tax_id,omitempty"`
	IsolationProfile string    `json:"isolation_profile"`
	Status           string    `json:"status"`
	CreatedAt        string    `json:"created_at"`
	UpdatedAt        string    `json:"updated_at"`
}

// MembershipResponse — resposta de uma membership.
type MembershipResponse struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	UserID    uuid.UUID `json:"user_id"`
	RoleID    uuid.UUID `json:"role_id"`
	Status    string    `json:"status"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
}

// GetTenantMe — GET /api/v1/tenants/me (tenant do contexto).
func (h *TenantAPIHandler) GetTenantMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extrair TenantContext
	tc, err := domain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	// Buscar tenant
	tenant, err := h.tenantSvc.GetTenant(ctx, tc.TenantID)
	if err != nil {
		http.Error(w, "failed to fetch tenant", http.StatusInternalServerError)
		return
	}
	if tenant == nil {
		http.Error(w, "tenant not found", http.StatusNotFound)
		return
	}

	// Responder
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(toTenantResponse(tenant))
}

// ListMyTenants — GET /api/v1/tenants (tenants em que o usuário autenticado
// tem membership ativa). Diferente de GetTenantMe, não depende de
// AuthorizationMiddleware/tenant_id na URL — usa só o Principal. A própria
// query em memberships já é filtrada por RLS a partir de
// app.current_user_id, então FindByUser nunca vaza membership de outro
// usuário mesmo que o código aqui tivesse um bug de filtro.
func (h *TenantAPIHandler) ListMyTenants(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	principal, err := authn.FromContext(ctx)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	memberships, err := h.membershipSvc.GetUserActiveMemberships(ctx, principal.UserID)
	if err != nil {
		http.Error(w, "failed to fetch memberships", http.StatusInternalServerError)
		return
	}

	responses := make([]TenantResponse, 0, len(memberships))
	for _, m := range memberships {
		tenant, err := h.tenantSvc.GetTenant(ctx, m.TenantID)
		if err != nil || tenant == nil {
			// RLS pode legitimamente esconder um tenant que ficou inativo
			// entre as duas queries; não é um erro fatal para a listagem.
			continue
		}
		responses = append(responses, toTenantResponse(tenant))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responses)
}

// ListMemberships — GET /api/v1/tenants/{tenant_id}/memberships (listar memberships do tenant).
func (h *TenantAPIHandler) ListMemberships(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extrair TenantContext
	tc, err := domain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	// Listar memberships
	memberships, err := h.membershipSvc.GetTenantMemberships(ctx, tc.TenantID)
	if err != nil {
		http.Error(w, "failed to fetch memberships", http.StatusInternalServerError)
		return
	}

	// Responder
	responses := make([]MembershipResponse, len(memberships))
	for i, m := range memberships {
		responses[i] = toMembershipResponse(m)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responses)
}

// CreateMembership — POST /api/v1/tenants/{tenant_id}/memberships.
type CreateMembershipRequest struct {
	UserID uuid.UUID `json:"user_id"`
	RoleID uuid.UUID `json:"role_id"`
}

func (h *TenantAPIHandler) CreateMembership(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extrair TenantContext
	tc, err := domain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	// Parse request
	var req CreateMembershipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validar campos
	if req.UserID == uuid.Nil || req.RoleID == uuid.Nil {
		http.Error(w, "user_id and role_id are required", http.StatusBadRequest)
		return
	}

	// Criar membership
	membership, err := h.membershipSvc.GrantMembership(ctx, tc.TenantID, req.UserID, req.RoleID)
	if err != nil {
		http.Error(w, "failed to grant membership", http.StatusInternalServerError)
		return
	}

	// Responder
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toMembershipResponse(membership))
}

// RevokeMembership — DELETE /api/v1/tenants/{tenant_id}/memberships/{membership_id}.
func (h *TenantAPIHandler) RevokeMembership(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extrair membership_id do path
	membershipIDStr := r.PathValue("membership_id")
	if membershipIDStr == "" {
		http.Error(w, "membership_id required", http.StatusBadRequest)
		return
	}

	membershipID, err := uuid.Parse(membershipIDStr)
	if err != nil {
		http.Error(w, "invalid membership_id", http.StatusBadRequest)
		return
	}

	// Revogar
	err = h.membershipSvc.RevokeMembership(ctx, membershipID)
	if err != nil {
		http.Error(w, "failed to revoke membership", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Helpers

func toTenantResponse(t *domain.Tenant) TenantResponse {
	return TenantResponse{
		ID:               t.ID,
		LegalName:        t.LegalName,
		TradeName:        t.TradeName,
		TaxID:            t.TaxID,
		IsolationProfile: string(t.IsolationProfile),
		Status:           string(t.Status),
		CreatedAt:        t.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:        t.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func toMembershipResponse(m *domain.Membership) MembershipResponse {
	return MembershipResponse{
		ID:        m.ID,
		TenantID:  m.TenantID,
		UserID:    m.UserID,
		RoleID:    m.RoleID,
		Status:    string(m.Status),
		CreatedAt: m.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: m.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}
