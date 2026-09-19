package domain

import (
	"time"

	"github.com/google/uuid"
)

// ReportType — tipo de relatório
type ReportType string

const (
	ReportTypeTickets      ReportType = "tickets"
	ReportTypeAccounts     ReportType = "accounts"
	ReportTypeSLA          ReportType = "sla"
	ReportTypeAudit        ReportType = "audit"
	ReportTypeFinancial    ReportType = "financial"
)

// ReportColumn — coluna de relatório
type ReportColumn struct {
	Field       string `json:"field"`
	Label       string `json:"label"`
	Type        string `json:"type"` // string, number, date, boolean
	Width       int    `json:"width,omitempty"`
	Sortable    bool   `json:"sortable"`
	Filterable  bool   `json:"filterable"`
}

// ReportFilter — filtro de relatório
type ReportFilter struct {
	Field    string        `json:"field"`
	Operator string        `json:"operator"` // eq, ne, gt, lt, contains, in, between
	Values   []interface{} `json:"values"`
}

// ReportTemplate — template de relatório
type ReportTemplate struct {
	ID          uuid.UUID       `json:"id"`
	TenantID    uuid.UUID       `json:"tenant_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Type        ReportType      `json:"type"`
	Columns     []ReportColumn  `json:"columns"`
	DefaultFilters []ReportFilter `json:"default_filters"`
	SortBy      string          `json:"sort_by"`
	SortOrder   string          `json:"sort_order"` // asc, desc
	Status      string          `json:"status"` // active, archived
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	CreatedBy   uuid.UUID       `json:"created_by"`
}

// NewReportTemplate — cria novo template
func NewReportTemplate(
	tenantID, createdBy uuid.UUID,
	name, description string,
	reportType ReportType,
	columns []ReportColumn,
) *ReportTemplate {
	return &ReportTemplate{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Name:         name,
		Description:  description,
		Type:         reportType,
		Columns:      columns,
		DefaultFilters: []ReportFilter{},
		Status:       "active",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		CreatedBy:    createdBy,
	}
}

// Report — execução de relatório
type Report struct {
	ID           uuid.UUID      `json:"id"`
	TenantID     uuid.UUID      `json:"tenant_id"`
	TemplateID   uuid.UUID      `json:"template_id"`
	Name         string         `json:"name"`
	Type         ReportType     `json:"type"`
	Filters      []ReportFilter `json:"filters"`
	Data         []map[string]interface{} `json:"data"`
	TotalRows    int            `json:"total_rows"`
	ExecutedAt   time.Time      `json:"executed_at"`
	ExecutedBy   uuid.UUID      `json:"executed_by"`
	DurationMs   int            `json:"duration_ms"`
	Status       string         `json:"status"` // pending, executing, completed, failed
	ErrorMessage string         `json:"error_message,omitempty"`
}

// NewReport — cria novo relatório
func NewReport(
	tenantID, templateID, executedBy uuid.UUID,
	name string,
	reportType ReportType,
	filters []ReportFilter,
) *Report {
	return &Report{
		ID:         uuid.New(),
		TenantID:   tenantID,
		TemplateID: templateID,
		Name:       name,
		Type:       reportType,
		Filters:    filters,
		Data:       []map[string]interface{}{},
		ExecutedAt: time.Now().UTC(),
		ExecutedBy: executedBy,
		Status:     "pending",
	}
}

// AddRow — adiciona linha ao relatório
func (r *Report) AddRow(row map[string]interface{}) {
	r.Data = append(r.Data, row)
	r.TotalRows = len(r.Data)
}

// Complete — marca como completo
func (r *Report) Complete(durationMs int) {
	r.Status = "completed"
	r.DurationMs = durationMs
}

// Fail — marca como falhado
func (r *Report) Fail(errorMsg string) {
	r.Status = "failed"
	r.ErrorMessage = errorMsg
}

// IsValid — valida template
func (t *ReportTemplate) IsValid() bool {
	return t.Name != "" && len(t.Columns) > 0 && t.Type != ""
}
