package adapters

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// SupervisorDashboardHandlers — handlers HTTP para supervisor dashboard
type SupervisorDashboardHandlers struct {
	dashboardService *application.SupervisorDashboardService
}

// NewSupervisorDashboardHandlers — cria novo handlers
func NewSupervisorDashboardHandlers(dashboardService *application.SupervisorDashboardService) *SupervisorDashboardHandlers {
	return &SupervisorDashboardHandlers{dashboardService: dashboardService}
}

// GetDashboard — GET /api/v1/supervisor/dashboard
func (h *SupervisorDashboardHandlers) GetDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantCtx, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	dashboard, err := h.dashboardService.GetSupervisorDashboard(ctx, tenantCtx.ActorID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dashboard)
}

// GetAlerts — GET /api/v1/supervisor/alerts
func (h *SupervisorDashboardHandlers) GetAlerts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantCtx, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	alerts, err := h.dashboardService.GetSupervisorAlerts(ctx, tenantCtx.ActorID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(alerts)
}

// GetAccountDetails — GET /api/v1/supervisor/accounts/:id
func (h *SupervisorDashboardHandlers) GetAccountDetails(w http.ResponseWriter, r *http.Request) {
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

	// Placeholder para detalhes da account
	details := map[string]interface{}{
		"account_id":  accountID.String(),
		"status":      "retrieved",
		"timestamp":   "2024-01-01T00:00:00Z",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(details)
}
