package adapters

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// AuditHandlers — handlers HTTP para auditoria
type AuditHandlers struct {
	auditService *application.AuditService
}

// NewAuditHandlers — cria novo AuditHandlers
func NewAuditHandlers(auditService *application.AuditService) *AuditHandlers {
	return &AuditHandlers{auditService: auditService}
}

// GetAccountAuditTrail — GET /api/v1/accounts/:id/audit
func (h *AuditHandlers) GetAccountAuditTrail(w http.ResponseWriter, r *http.Request) {
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

	limit := 100
	offset := 0

	// Parse query params (simplificado - ignorar erros)
	if l := r.URL.Query().Get("limit"); l != "" {
		_ = l // Ignorar parse error para simplicidade
	}

	trail, err := h.auditService.GetAccountAuditTrail(ctx, accountID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(trail)
}

// GetTicketAuditTrail — GET /api/v1/accounts/:id/tickets/:ticketId/audit
func (h *AuditHandlers) GetTicketAuditTrail(w http.ResponseWriter, r *http.Request) {
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

	limit := 100
	offset := 0

	trail, err := h.auditService.GetTicketAuditTrail(ctx, ticketID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(trail)
}
