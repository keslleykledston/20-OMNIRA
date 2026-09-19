package adapters

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/application"
	"github.com/omnira/omnira/internal/bpo/domain"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// BPOHandlers — handlers HTTP para BPO
type BPOHandlers struct {
	service *application.BPOService
}

// NewBPOHandlers — cria novo BPOHandlers
func NewBPOHandlers(service *application.BPOService) *BPOHandlers {
	return &BPOHandlers{service: service}
}

// CreateAccountRequest — requisição para criar account
type CreateAccountRequest struct {
	Name             string                        `json:"name"`
	Description      string                        `json:"description"`
	Type             domain.AccountType            `json:"type"`
	MaxTeamMembers   int                           `json:"max_team_members"`
	MaxTicketsMonth  int                           `json:"max_tickets_month"`
	SLAConfig        domain.SLAConfiguration       `json:"sla_config"`
}

// AccountResponse — resposta com account
type AccountResponse struct {
	ID               domain.AccountID              `json:"id"`
	TenantID         uuid.UUID                     `json:"tenant_id"`
	Name             string                        `json:"name"`
	Status           domain.AccountStatus          `json:"status"`
	MaxTeamMembers   int                           `json:"max_team_members"`
	MaxTicketsMonth  int                           `json:"max_tickets_month"`
	SLAConfig        domain.SLAConfiguration       `json:"sla_config"`
}

// CreateTicketRequest — requisição para criar ticket
type CreateTicketRequest struct {
	Subject     string                  `json:"subject"`
	Description string                  `json:"description"`
	Priority    domain.TicketPriority   `json:"priority"`
}

// TicketResponse — resposta com ticket
type TicketResponse struct {
	ID          domain.TicketID         `json:"id"`
	AccountID   domain.AccountID        `json:"account_id"`
	Subject     string                  `json:"subject"`
	Priority    domain.TicketPriority   `json:"priority"`
	Status      domain.TicketStatus     `json:"status"`
	CreatedAt   string                  `json:"created_at"`
}

// AssignRequest — requisição para atribuir ticket
type AssignRequest struct {
	AgentID uuid.UUID `json:"agent_id"`
}

// CreateAccount — POST /api/v1/accounts
func (h *BPOHandlers) CreateAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantCtx, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	var req CreateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	account, err := h.service.CreateAccount(
		ctx,
		tenantCtx.TenantID,
		uuid.New(),
		tenantCtx.ActorID,
		req.Name,
		req.Description,
		req.Type,
		req.MaxTeamMembers,
		req.MaxTicketsMonth,
		req.SLAConfig,
	)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := AccountResponse{
		ID:              account.ID,
		TenantID:        account.TenantID,
		Name:            account.Name,
		Status:          account.Status,
		MaxTeamMembers:  account.MaxTeamMembers,
		MaxTicketsMonth: account.MaxTicketsMonth,
		SLAConfig:       account.SLAConfig,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// GetAccount — GET /api/v1/accounts/:id
func (h *BPOHandlers) GetAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantCtx, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid account id", http.StatusBadRequest)
		return
	}

	account, err := h.service.GetAccount(ctx, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if account == nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}

	if account.TenantID != tenantCtx.TenantID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	resp := AccountResponse{
		ID:              account.ID,
		TenantID:        account.TenantID,
		Name:            account.Name,
		Status:          account.Status,
		MaxTeamMembers:  account.MaxTeamMembers,
		MaxTicketsMonth: account.MaxTicketsMonth,
		SLAConfig:       account.SLAConfig,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ListAccounts — GET /api/v1/accounts
func (h *BPOHandlers) ListAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantCtx, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	accounts, err := h.service.ListAccounts(ctx, tenantCtx.TenantID, 100, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var respList []AccountResponse
	for _, acc := range accounts {
		respList = append(respList, AccountResponse{
			ID:              acc.ID,
			TenantID:        acc.TenantID,
			Name:            acc.Name,
			Status:          acc.Status,
			MaxTeamMembers:  acc.MaxTeamMembers,
			MaxTicketsMonth: acc.MaxTicketsMonth,
			SLAConfig:       acc.SLAConfig,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(respList)
}

// SuspendAccount — POST /api/v1/accounts/:id/suspend
func (h *BPOHandlers) SuspendAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantCtx, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid account id", http.StatusBadRequest)
		return
	}

	err = h.service.SuspendAccount(ctx, id, "suspended via API", tenantCtx.ActorID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ReactivateAccount — POST /api/v1/accounts/:id/reactivate
func (h *BPOHandlers) ReactivateAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantCtx, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid account id", http.StatusBadRequest)
		return
	}

	err = h.service.ReactivateAccount(ctx, id, tenantCtx.ActorID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// CreateTicket — POST /api/v1/accounts/:id/tickets
func (h *BPOHandlers) CreateTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantCtx, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	idStr := r.PathValue("id")
	accountID, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid account id", http.StatusBadRequest)
		return
	}

	var req CreateTicketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	ticket, err := h.service.CreateTicket(
		ctx,
		accountID,
		tenantCtx.TenantID,
		tenantCtx.ActorID,
		req.Subject,
		req.Description,
		req.Priority,
	)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := TicketResponse{
		ID:        ticket.ID,
		AccountID: ticket.AccountID,
		Subject:   ticket.Subject,
		Priority:  ticket.Priority,
		Status:    ticket.Status,
		CreatedAt: ticket.CreatedAt.String(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// GetTicket — GET /api/v1/accounts/:id/tickets/:ticketId
func (h *BPOHandlers) GetTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantCtx, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	ticketIdStr := r.PathValue("ticketId")
	ticketID, err := uuid.Parse(ticketIdStr)
	if err != nil {
		http.Error(w, "invalid ticket id", http.StatusBadRequest)
		return
	}

	ticket, err := h.service.GetTicket(ctx, ticketID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if ticket == nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}

	if ticket.TenantID != tenantCtx.TenantID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	resp := TicketResponse{
		ID:        ticket.ID,
		AccountID: ticket.AccountID,
		Subject:   ticket.Subject,
		Priority:  ticket.Priority,
		Status:    ticket.Status,
		CreatedAt: ticket.CreatedAt.String(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// AssignTicket — POST /api/v1/accounts/:id/tickets/:ticketId/assign
func (h *BPOHandlers) AssignTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	ticketIdStr := r.PathValue("ticketId")
	ticketID, err := uuid.Parse(ticketIdStr)
	if err != nil {
		http.Error(w, "invalid ticket id", http.StatusBadRequest)
		return
	}

	var req AssignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	err = h.service.AssignTicket(ctx, ticketID, req.AgentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RecordResponse — POST /api/v1/accounts/:id/tickets/:ticketId/response
func (h *BPOHandlers) RecordResponse(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	ticketIdStr := r.PathValue("ticketId")
	ticketID, err := uuid.Parse(ticketIdStr)
	if err != nil {
		http.Error(w, "invalid ticket id", http.StatusBadRequest)
		return
	}

	err = h.service.RecordResponse(ctx, ticketID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ResolveTicket — POST /api/v1/accounts/:id/tickets/:ticketId/resolve
func (h *BPOHandlers) ResolveTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	ticketIdStr := r.PathValue("ticketId")
	ticketID, err := uuid.Parse(ticketIdStr)
	if err != nil {
		http.Error(w, "invalid ticket id", http.StatusBadRequest)
		return
	}

	err = h.service.ResolveTicket(ctx, ticketID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// CloseTicket — POST /api/v1/accounts/:id/tickets/:ticketId/close
func (h *BPOHandlers) CloseTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	ticketIdStr := r.PathValue("ticketId")
	ticketID, err := uuid.Parse(ticketIdStr)
	if err != nil {
		http.Error(w, "invalid ticket id", http.StatusBadRequest)
		return
	}

	err = h.service.CloseTicket(ctx, ticketID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetMetrics — GET /api/v1/accounts/:id/metrics
func (h *BPOHandlers) GetMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	idStr := r.PathValue("id")
	accountID, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid account id", http.StatusBadRequest)
		return
	}

	metrics, err := h.service.GetAccountMetrics(ctx, accountID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}
