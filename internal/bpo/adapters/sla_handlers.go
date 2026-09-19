package adapters

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// SLAHandlers — handlers HTTP para SLA reports
type SLAHandlers struct {
	slaService *application.SLAService
}

// NewSLAHandlers — cria novo SLAHandlers
func NewSLAHandlers(slaService *application.SLAService) *SLAHandlers {
	return &SLAHandlers{slaService: slaService}
}

// GetSLAComplianceReport — GET /api/v1/accounts/:id/sla/compliance
func (h *SLAHandlers) GetSLAComplianceReport(w http.ResponseWriter, r *http.Request) {
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

	// Extrair query parameters
	startDateStr := r.URL.Query().Get("start_date")
	endDateStr := r.URL.Query().Get("end_date")

	var startDate, endDate time.Time

	if startDateStr != "" {
		if t, err := time.Parse(time.RFC3339, startDateStr); err == nil {
			startDate = t
		} else {
			http.Error(w, "invalid start_date format", http.StatusBadRequest)
			return
		}
	} else {
		startDate = time.Now().Add(-30 * 24 * time.Hour)
	}

	if endDateStr != "" {
		if t, err := time.Parse(time.RFC3339, endDateStr); err == nil {
			endDate = t
		} else {
			http.Error(w, "invalid end_date format", http.StatusBadRequest)
			return
		}
	} else {
		endDate = time.Now()
	}

	report, err := h.slaService.GetSLAComplianceReport(ctx, accountID, startDate, endDate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// GetSLASummary — GET /api/v1/accounts/:id/sla/summary
func (h *SLAHandlers) GetSLASummary(w http.ResponseWriter, r *http.Request) {
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

	summary, err := h.slaService.GetSLASummary(ctx, accountID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

// ExportSLAReport — GET /api/v1/accounts/:id/sla/export
func (h *SLAHandlers) ExportSLAReport(w http.ResponseWriter, r *http.Request) {
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

	startDate := time.Now().Add(-30 * 24 * time.Hour)
	endDate := time.Now()

	report, err := h.slaService.GetSLAComplianceReport(ctx, accountID, startDate, endDate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Determinar formato
	format := r.URL.Query().Get("format")
	if format == "csv" {
		h.exportSLAReportCSV(w, report)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=sla-report.json")
		json.NewEncoder(w).Encode(report)
	}
}

// exportSLAReportCSV — exporta relatório como CSV
func (h *SLAHandlers) exportSLAReportCSV(w http.ResponseWriter, report *application.SLAComplianceReport) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=sla-report.csv")

	// Header
	w.Write([]byte("Ticket ID,Subject,Priority,Status,First Response Met,Resolution Met,Breached\n"))

	// Dados
	for _, ticket := range report.TicketMetrics {
		csv := ticket.ID.String() + "," +
			ticket.Subject + "," +
			ticket.Priority + "," +
			ticket.Status + "," +
			boolToString(ticket.FirstResponseMet) + "," +
			boolToString(ticket.ResolutionMet) + "," +
			boolToString(ticket.Breached) + "\n"
		w.Write([]byte(csv))
	}

	// Sumário
	w.Write([]byte("\n\nSummary\n"))
	w.Write([]byte("Total Tickets," + string(rune(report.TotalTickets)) + "\n"))
	w.Write([]byte("Completed Tickets," + string(rune(report.CompletedTickets)) + "\n"))
	w.Write([]byte("First Response Rate," + percentToString(report.FirstResponseRate) + "%\n"))
	w.Write([]byte("Resolution Rate," + percentToString(report.ResolutionRate) + "%\n"))
}

func boolToString(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func percentToString(p float64) string {
	if p == 0 {
		return "0"
	}
	return string(rune(int(p)))
}
