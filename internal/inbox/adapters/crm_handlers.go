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

// NewCRMHandlers — cria novo CRM handler
func NewCRMHandlers(dbPool *pgxpool.Pool) *CRMHandlers {
	// Use mock CRM for now
	return &CRMHandlers{
		dbPool: dbPool,
		crm:    connectors.NewMockCRMConnector(),
	}
}

// SetK3GCRMClient — configura o cliente K3G CRM
func (h *CRMHandlers) SetK3GCRMClient(client *connectors.K3GCRMClient) {
	h.k3gClient = client
}

// CreateTicket — cria ticket para uma conversa
// POST /api/v1/tenants/{tenantId}/conversations/{conversationId}/ticket
func (h *CRMHandlers) CreateTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	tenantID, convID := r.PathValue("tenantId"), r.PathValue("conversationId")
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

	// Check tenant access via RLS
	err = db.WithTenantSession(ctx, h.dbPool, tid, false, func(sessionCtx context.Context) error {
		// Verify conversation exists and belongs to tenant
		var count int
		row := h.dbPool.QueryRow(sessionCtx, `
			SELECT 1 FROM conversations WHERE id = $1 AND tenant_id = $2
		`, convID, tid)
		if err := row.Scan(&count); err != nil {
			return http.ErrMissingFile
		}
		return nil
	})
	if err != nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}

	// Get contact email for CRM lookup
	var contactEmail string
	err = db.WithTenantSession(ctx, h.dbPool, tid, false, func(sessionCtx context.Context) error {
		row := h.dbPool.QueryRow(sessionCtx, `
			SELECT c.external_identity
			FROM conversations conv
			JOIN contacts c ON conv.contact_id = c.id
			WHERE conv.id = $1 AND conv.tenant_id = $2
		`, convID, tid)
		return row.Scan(&contactEmail)
	})
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

	ticketID := r.PathValue("ticketId")
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

	ticketID := r.PathValue("ticketId")
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

	ticketID := r.PathValue("ticketId")
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

	tenantID, convID := r.PathValue("tenantId"), r.PathValue("conversationId")
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
