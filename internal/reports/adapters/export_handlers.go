package adapters

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/reports/application"
	reportdomain "github.com/omnira/omnira/internal/reports/domain"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ExportHandlers — handlers para export de relatórios
type ExportHandlers struct {
	reportService *application.ReportService
}

// NewExportHandlers — cria novo handler
func NewExportHandlers(reportService *application.ReportService) *ExportHandlers {
	return &ExportHandlers{reportService: reportService}
}

// ExportReportJSON — GET /api/v1/reports/:id/export/json
func (h *ExportHandlers) ExportReportJSON(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	idStr := r.PathValue("id")
	_, err = uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid report id", http.StatusBadRequest)
		return
	}

	// Placeholder report
	report := reportdomain.NewReport(uuid.New(), uuid.New(), uuid.New(), "Export Test", reportdomain.ReportTypeTickets, nil)
	report.AddRow(map[string]interface{}{"id": "1", "status": "open"})
	report.Complete(100)

	data, err := h.reportService.ExportReportAsJSON(report)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=report.json")
	w.Write(data)
}

// ExportReportCSV — GET /api/v1/reports/:id/export/csv
func (h *ExportHandlers) ExportReportCSV(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	idStr := r.PathValue("id")
	_, err = uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid report id", http.StatusBadRequest)
		return
	}

	// Placeholder report
	report := reportdomain.NewReport(uuid.New(), uuid.New(), uuid.New(), "Export Test", reportdomain.ReportTypeTickets, nil)
	report.AddRow(map[string]interface{}{"id": "1", "status": "open", "priority": "high"})
	report.Complete(100)

	csv, err := h.reportService.ExportReportAsCSV(report)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=report.csv")
	fmt.Fprint(w, csv)
}

// ExportReportPDF — GET /api/v1/reports/:id/export/pdf
func (h *ExportHandlers) ExportReportPDF(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	idStr := r.PathValue("id")
	_, err = uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid report id", http.StatusBadRequest)
		return
	}

	// PDF export would use a library like gofpdf
	// For now, return placeholder PDF header
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=report.pdf")
	fmt.Fprint(w, "PDF-1.4\nplaceholder pdf")
}

// ExportReportExcel — GET /api/v1/reports/:id/export/xlsx
func (h *ExportHandlers) ExportReportExcel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	idStr := r.PathValue("id")
	_, err = uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid report id", http.StatusBadRequest)
		return
	}

	// Excel export would use a library like excelize
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=report.xlsx")
	fmt.Fprint(w, "PK\x03\x04") // ZIP header placeholder
}

// ScheduleExport — POST /api/v1/reports/:id/export/schedule
type ScheduleExportRequest struct {
	Format   string `json:"format"` // json, csv, pdf, xlsx
	Schedule string `json:"schedule"` // daily, weekly, monthly
	Email    string `json:"email"`
}

// ScheduleExportResponse — resposta de agendamento
type ScheduleExportResponse struct {
	ID        uuid.UUID `json:"id"`
	ReportID  uuid.UUID `json:"report_id"`
	Format    string    `json:"format"`
	Schedule  string    `json:"schedule"`
	Status    string    `json:"status"`
	CreatedAt string    `json:"created_at"`
}

func (h *ExportHandlers) ScheduleExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, err := tenancydomain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	var req ScheduleExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	resp := ScheduleExportResponse{
		ID:       uuid.New(),
		ReportID: uuid.New(),
		Format:   req.Format,
		Schedule: req.Schedule,
		Status:   "scheduled",
		CreatedAt: "2024-01-01T00:00:00Z",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}
