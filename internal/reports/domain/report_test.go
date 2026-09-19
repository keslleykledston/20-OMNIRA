package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewReportTemplate(t *testing.T) {
	tenantID := uuid.New()
	createdBy := uuid.New()
	columns := []ReportColumn{
		{Field: "id", Label: "ID", Type: "string", Sortable: true, Filterable: true},
		{Field: "status", Label: "Status", Type: "string", Sortable: true, Filterable: true},
	}

	template := NewReportTemplate(tenantID, createdBy, "Tickets Report", "", ReportTypeTickets, columns)

	if template.ID == uuid.Nil {
		t.Errorf("template id should not be nil")
	}

	if template.Status != "active" {
		t.Errorf("new template should be active")
	}

	if len(template.Columns) != 2 {
		t.Errorf("expected 2 columns, got %d", len(template.Columns))
	}
}

func TestNewReport(t *testing.T) {
	tenantID := uuid.New()
	templateID := uuid.New()
	executedBy := uuid.New()
	filters := []ReportFilter{
		{Field: "status", Operator: "eq", Values: []interface{}{"open"}},
	}

	report := NewReport(tenantID, templateID, executedBy, "My Report", ReportTypeTickets, filters)

	if report.ID == uuid.Nil {
		t.Errorf("report id should not be nil")
	}

	if report.Status != "pending" {
		t.Errorf("new report should be pending")
	}

	if report.TotalRows != 0 {
		t.Errorf("new report should have 0 rows")
	}
}

func TestReportAddRow(t *testing.T) {
	report := NewReport(uuid.New(), uuid.New(), uuid.New(), "Test", ReportTypeTickets, nil)

	row := map[string]interface{}{
		"id":     "ticket-1",
		"status": "open",
	}

	report.AddRow(row)

	if report.TotalRows != 1 {
		t.Errorf("expected 1 row, got %d", report.TotalRows)
	}

	if len(report.Data) != 1 {
		t.Errorf("expected 1 data entry, got %d", len(report.Data))
	}
}

func TestReportComplete(t *testing.T) {
	report := NewReport(uuid.New(), uuid.New(), uuid.New(), "Test", ReportTypeTickets, nil)

	report.Complete(150)

	if report.Status != "completed" {
		t.Errorf("report should be completed")
	}

	if report.DurationMs != 150 {
		t.Errorf("expected 150ms, got %d", report.DurationMs)
	}
}

func TestReportFail(t *testing.T) {
	report := NewReport(uuid.New(), uuid.New(), uuid.New(), "Test", ReportTypeTickets, nil)

	report.Fail("Database connection failed")

	if report.Status != "failed" {
		t.Errorf("report should be failed")
	}

	if report.ErrorMessage != "Database connection failed" {
		t.Errorf("error message should match")
	}
}

func TestReportTemplateIsValid(t *testing.T) {
	template := &ReportTemplate{
		Name:    "Test",
		Type:    ReportTypeTickets,
		Columns: []ReportColumn{{Field: "id", Label: "ID"}},
	}

	if !template.IsValid() {
		t.Errorf("template should be valid")
	}

	// Invalid: no columns
	template.Columns = []ReportColumn{}
	if template.IsValid() {
		t.Errorf("template without columns should be invalid")
	}
}
