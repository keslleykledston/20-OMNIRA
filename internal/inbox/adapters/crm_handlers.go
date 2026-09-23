package adapters

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// CRMTicketRequest — payload para criar/atualizar ticket
type CRMTicketRequest struct {
	Subject string `json:"subject"`
	Status  string `json:"status,omitempty"`
}

// CRMTicketResponse — resposta de ticket
type CRMTicketResponse struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Subject   string `json:"subject"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// CRMHandlers — handlers para CRM
type CRMHandlers struct {
	dbPool    *pgxpool.Pool
	crm       connectors.CRMConnector
	k3gClient *connectors.K3GCRMClient
}

// NewCRMHandlers — cria novo CRM handler.
//
// PRODUCT.6-B: no ticketing connector is installed here. There is no
// tenant-scoped ERP ticket-provider resolution/configuration model yet
// (IXCConnector implements the real interface but is intentionally left
// unwired — see docs/adr/... future ERP ticket projection ADR). Until a
// real per-tenant connector exists, every ticket CRUD request must fail
// explicitly rather than silently succeed against an in-memory fake —
// see ticketingUnavailable / errTicketingNotConfigured below.
func NewCRMHandlers(dbPool *pgxpool.Pool) *CRMHandlers {
	return &CRMHandlers{dbPool: dbPool}
}

// SetCRMConnector — test-only injection point. Canonical runtime
// composition (server.go) must never call this: a mock/fake ticketing
// connector must not become product runtime authority (PRODUCT.6-B).
func (h *CRMHandlers) SetCRMConnector(crm connectors.CRMConnector) {
	h.crm = crm
}

// SetK3GCRMClient — configura o cliente K3G CRM
func (h *CRMHandlers) SetK3GCRMClient(client *connectors.K3GCRMClient) {
	h.k3gClient = client
}

// ticketingUnavailable writes the canonical response for "no real ERP
// ticketing connector is configured for this tenant" — 503, matching the
// exact convention already used by the frontend for the same class of
// problem (web/src/lib/integrations.ts: "Integração indisponível: o
// servidor não está configurado para este provedor."). Never a fake
// 200/201, never a fallback to local-only storage.
func ticketingUnavailable(w http.ResponseWriter) {
	http.Error(w, "ticketing integration not configured for this tenant", http.StatusServiceUnavailable)
}

// GetCurrentTicket — reports whether a real ERP ticketing connector is
// configured for this tenant, so TicketPanel can render an honest
// unavailable state proactively (PRODUCT.6-B) instead of only discovering
// it reactively when the operator tries to create a ticket. It never
// fabricates or returns a ticket: with no connector configured (the only
// state possible today), it always answers 503.
// GET /api/v1/tenants/{tenantId}/conversations/{conversationId}/ticket
func (h *CRMHandlers) GetCurrentTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.crm == nil {
		ticketingUnavailable(w)
		return
	}
	// A real connector exists but there is no per-conversation ticket
	// lookup yet (no local projection model — PRODUCT.6-A). Nothing to
	// report until that lands.
	http.Error(w, "no ticket associated with this conversation", http.StatusNotFound)
}

// CreateTicket — cria ticket para uma conversa
// POST /api/v1/tenants/{tenantId}/conversations/{conversationId}/ticket
func (h *CRMHandlers) CreateTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.crm == nil {
		ticketingUnavailable(w)
		return
	}

	tenantID, convID := r.PathValue("tenant_id"), r.PathValue("conversation_id")
	if tenantID == "" || convID == "" {
		http.Error(w, "missing tenant or conversation ID", http.StatusBadRequest)
		return
	}

	// Parse request
	var req CRMTicketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Subject == "" {
		http.Error(w, "subject required", http.StatusBadRequest)
		return
	}

	// Get tenant ID (from URL path, already validated by middleware)
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		http.Error(w, "invalid tenant ID", http.StatusBadRequest)
		return
	}

	// The tenantSession middleware already put a tenant-scoped transaction in ctx;
	// query through it so RLS applies (the raw pool would run without tenant context).
	q := db.QuerierFromContext(ctx, h.dbPool)
	var exists int
	if err := q.QueryRow(ctx, `SELECT 1 FROM conversations WHERE id = $1 AND tenant_id = $2`, convID, tid).Scan(&exists); err != nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}

	// Get contact email for CRM lookup
	var contactEmail string
	err = q.QueryRow(ctx, `
		SELECT COALESCE(c.email, '')
		FROM conversations conv
		JOIN contacts c ON conv.contact_id = c.id
		WHERE conv.id = $1 AND conv.tenant_id = $2
	`, convID, tid).Scan(&contactEmail)
	if err != nil || contactEmail == "" {
		contactEmail = "customer@example.com"
	}

	// Create CRM ticket
	customerID, err := h.crm.FindCustomer(ctx, contactEmail)
	if err != nil {
		http.Error(w, "CRM lookup failed", http.StatusInternalServerError)
		return
	}

	ticketID, err := h.crm.CreateTicket(ctx, customerID, req.Subject)
	if err != nil {
		http.Error(w, "ticket creation failed", http.StatusInternalServerError)
		return
	}

	// Store reference to external ticket in conversation (future: metadata column)
	// For now, just return the ticket

	ticket, _ := h.crm.GetTicket(ctx, ticketID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(CRMTicketResponse{
		ID:        ticket.ID,
		Status:    ticket.Status,
		Subject:   ticket.Subject,
		CreatedAt: ticket.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: ticket.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// GetTicket — obtém ticket
// GET /api/v1/tenants/{tenantId}/conversations/{conversationId}/ticket/{ticketId}
func (h *CRMHandlers) GetTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.crm == nil {
		ticketingUnavailable(w)
		return
	}

	ticketID := r.PathValue("ticket_id")
	if ticketID == "" {
		http.Error(w, "missing ticket ID", http.StatusBadRequest)
		return
	}

	ticket, err := h.crm.GetTicket(ctx, ticketID)
	if err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CRMTicketResponse{
		ID:        ticket.ID,
		Status:    ticket.Status,
		Subject:   ticket.Subject,
		CreatedAt: ticket.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: ticket.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// UpdateTicket — atualiza ticket
// PATCH /api/v1/tenants/{tenantId}/conversations/{conversationId}/ticket/{ticketId}
func (h *CRMHandlers) UpdateTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.crm == nil {
		ticketingUnavailable(w)
		return
	}

	ticketID := r.PathValue("ticket_id")
	var req CRMTicketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Status == "" {
		http.Error(w, "status required", http.StatusBadRequest)
		return
	}

	err := h.crm.UpdateTicket(ctx, ticketID, req.Status)
	if err != nil {
		http.Error(w, "ticket update failed", http.StatusInternalServerError)
		return
	}

	ticket, _ := h.crm.GetTicket(ctx, ticketID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CRMTicketResponse{
		ID:        ticket.ID,
		Status:    ticket.Status,
		Subject:   ticket.Subject,
		CreatedAt: ticket.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: ticket.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// CloseTicket — fecha ticket
// POST /api/v1/tenants/{tenantId}/conversations/{conversationId}/ticket/{ticketId}/close
func (h *CRMHandlers) CloseTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.crm == nil {
		ticketingUnavailable(w)
		return
	}

	ticketID := r.PathValue("ticket_id")
	err := h.crm.CloseTicket(ctx, ticketID)
	if err != nil {
		http.Error(w, "ticket close failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// CRMActivityRequest — payload para criar activity (atendimento)
type CRMActivityRequest struct {
	Subject   string `json:"subject"`
	CompanyID string `json:"company_id"`
	ContactID string `json:"contact_id"`
}

// CRMActivityResponse — resposta de activity
type CRMActivityResponse struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Subject   string `json:"subject"`
	ContactID string `json:"contact_id"`
	CompanyID string `json:"company_id"`
	CreatedAt string `json:"created_at"`
}

// CreateActivity — cria activity (atendimento WHATSAPP) para uma conversa
// POST /api/v1/tenants/{tenantId}/conversations/{conversationId}/crm/activity
func (h *CRMHandlers) CreateActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	tenantID, convID := r.PathValue("tenant_id"), r.PathValue("conversation_id")
	if tenantID == "" || convID == "" {
		http.Error(w, "missing tenant or conversation ID", http.StatusBadRequest)
		return
	}

	// Parse request
	var req CRMActivityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Subject == "" || req.ContactID == "" || req.CompanyID == "" {
		http.Error(w, "subject, contact_id and company_id required", http.StatusBadRequest)
		return
	}

	// Get tenant ID
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		http.Error(w, "invalid tenant ID", http.StatusBadRequest)
		return
	}

	// Check tenant access via RLS
	activityID := ""
	err = db.WithTenantSession(ctx, h.dbPool, tid, false, func(sessionCtx context.Context) error {
		// Create activity in CRM (type is always WHATSAPP in this context)
		activity, crErr := h.k3gClient.CreateActivity(sessionCtx, "WHATSAPP", req.Subject, req.ContactID, req.CompanyID)
		if crErr != nil {
			return crErr
		}
		activityID = activity.ID
		return nil
	})
	if err != nil {
		http.Error(w, "activity creation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Return response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(CRMActivityResponse{
		ID:        activityID,
		Type:      "WHATSAPP",
		Subject:   req.Subject,
		ContactID: req.ContactID,
		CompanyID: req.CompanyID,
		CreatedAt: "", // CRM retorna, mas não temos aqui
	})
}

// CompanyItem — representação de uma empresa
type CompanyItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	CNPJ string `json:"cnpj,omitempty"`
}

// CompanyListResponse — resposta com lista de empresas
type CompanyListResponse struct {
	Items []CompanyItem `json:"items"`
}

// ListCompanies — lista empresas do CRM K3G
// GET /api/v1/integrations/companies
func (h *CRMHandlers) ListCompanies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Check if K3G CRM client is configured
	if h.k3gClient == nil {
		// Return empty list if CRM not configured
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CompanyListResponse{Items: []CompanyItem{}})
		return
	}

	// List companies from K3G CRM
	companies, err := h.k3gClient.ListCompanies(ctx)
	if err != nil {
		http.Error(w, "failed to list companies: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to response format
	items := make([]CompanyItem, len(companies))
	for i, co := range companies {
		items[i] = CompanyItem{
			ID:   co.ID,
			Name: co.Name,
			CNPJ: co.CNPJ,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CompanyListResponse{Items: items})
}
