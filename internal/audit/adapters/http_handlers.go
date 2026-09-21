package adapters

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/audit/application"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

const permissionAuditRead = "audit.read"

// AuditAPIHandler — handlers HTTP para Audit API.
type AuditAPIHandler struct {
	auditSvc *application.AuditService
	pool     *pgxpool.Pool
}

// NewAuditAPIHandler — cria um novo AuditAPIHandler.
func NewAuditAPIHandler(auditSvc *application.AuditService, pool *pgxpool.Pool) *AuditAPIHandler {
	return &AuditAPIHandler{auditSvc: auditSvc, pool: pool}
}

func (h *AuditAPIHandler) authorize(r *http.Request, permission string) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID.String() == "" {
		return nil, errors.New("tenant context not found")
	}
	var ok bool
	err = platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tc.TenantID, tc.ActorID, permission).Scan(&ok)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("permission denied")
	}
	return tc, nil
}

// AuditEventResponse — resposta de um evento de auditoria.
type AuditEventResponse struct {
	ID            string                 `json:"id"`
	TenantID      string                 `json:"tenant_id"`
	ActorID       string                 `json:"actor_id"`
	Action        string                 `json:"action"`
	ResourceType  string                 `json:"resource_type"`
	ResourceID    string                 `json:"resource_id"`
	Outcome       string                 `json:"outcome"`
	CorrelationID string                 `json:"correlation_id"`
	CausationID   string                 `json:"causation_id,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt     string                 `json:"created_at"`
}

// ListTenantAuditEvents — GET /api/v1/tenants/{tenant_id}/audit/events.
func (h *AuditAPIHandler) ListTenantAuditEvents(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permissionAuditRead)
	if err != nil {
		if err.Error() == "permission denied" {
			http.Error(w, "forbidden", http.StatusForbidden)
		} else {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
		return
	}

	ctx := r.Context()

	// Parse query params
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// Listar eventos
	events, err := h.auditSvc.ListTenantEvents(ctx, tc.TenantID, limit, offset)
	if err != nil {
		http.Error(w, "failed to fetch audit events", http.StatusInternalServerError)
		return
	}

	// Responder
	responses := make([]AuditEventResponse, len(events))
	for i, e := range events {
		responses[i] = toAuditEventResponse(e)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responses)
}

// Helper

// ExportEvents — exporta eventos em CSV ou JSON
// GET /audit/export?format=csv&start_date=...&end_date=...
func (h *AuditAPIHandler) ExportEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	tc, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusUnauthorized)
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}

	// Parse dates
	var startDate, endDate time.Time
	if startDateStr := r.URL.Query().Get("start_date"); startDateStr != "" {
		startDate, _ = time.Parse(time.RFC3339, startDateStr)
	} else {
		startDate = time.Now().AddDate(-1, 0, 0)
	}

	if endDateStr := r.URL.Query().Get("end_date"); endDateStr != "" {
		endDate, _ = time.Parse(time.RFC3339, endDateStr)
	} else {
		endDate = time.Now()
	}

	// Fetch events
	events, err := h.auditSvc.ListTenantEvents(ctx, tc.TenantID, 10000, 0)
	if err != nil {
		http.Error(w, "failed to export events", http.StatusInternalServerError)
		return
	}

	// Filter by date
	var filtered []*auditdomain.AuditEvent
	for _, e := range events {
		if e.CreatedAt.After(startDate) && e.CreatedAt.Before(endDate) {
			filtered = append(filtered, e)
		}
	}

	// Set download headers
	filename := fmt.Sprintf("audit-export-%s.%s", time.Now().Format("2006-01-02"), format)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))

	// Render
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		renderCSV(w, filtered)
	default:
		w.Header().Set("Content-Type", "application/json")
		responses := make([]AuditEventResponse, len(filtered))
		for i, e := range filtered {
			responses[i] = toAuditEventResponse(e)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"events": responses,
			"count":  len(responses),
		})
	}
}

// Stats — retorna estatísticas de auditoria
// GET /audit/stats
func (h *AuditAPIHandler) Stats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	tc, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusUnauthorized)
		return
	}

	// Fetch events
	events, err := h.auditSvc.ListTenantEvents(ctx, tc.TenantID, 10000, 0)
	if err != nil {
		http.Error(w, "failed to get stats", http.StatusInternalServerError)
		return
	}

	// Calculate stats
	actionCounts := make(map[string]int)
	actorCounts := make(map[string]int)
	successCount := 0

	for _, e := range events {
		actionCounts[string(e.Action)]++
		actorCounts[e.ActorID.String()]++
		if e.Outcome == auditdomain.OutcomeSuccess {
			successCount++
		}
	}

	stats := map[string]interface{}{
		"total_events":  len(events),
		"successful":    successCount,
		"failed":        len(events) - successCount,
		"by_action":     actionCounts,
		"by_actor":      actorCounts,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(stats)
}

// Helper

func toAuditEventResponse(e *auditdomain.AuditEvent) AuditEventResponse {
	return AuditEventResponse{
		ID:            e.ID.String(),
		TenantID:      e.TenantID.String(),
		ActorID:       e.ActorID.String(),
		Action:        string(e.Action),
		ResourceType:  string(e.ResourceType),
		ResourceID:    e.ResourceID.String(),
		Outcome:       string(e.Outcome),
		CorrelationID: e.CorrelationID.String(),
		CausationID:   e.CausationID.String(),
		Metadata:      e.Metadata,
		CreatedAt:     e.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func renderCSV(w http.ResponseWriter, events []*auditdomain.AuditEvent) {
	w.WriteHeader(http.StatusOK)

	writer := csv.NewWriter(w)
	defer writer.Flush()

	// Header
	writer.Write([]string{
		"ID", "TenantID", "ActorID", "Action", "ResourceType", "ResourceID",
		"Outcome", "CorrelationID", "Timestamp",
	})

	// Rows
	for _, e := range events {
		writer.Write([]string{
			e.ID.String(),
			e.TenantID.String(),
			e.ActorID.String(),
			string(e.Action),
			string(e.ResourceType),
			e.ResourceID.String(),
			string(e.Outcome),
			e.CorrelationID.String(),
			e.CreatedAt.Format(time.RFC3339),
		})
	}
}
