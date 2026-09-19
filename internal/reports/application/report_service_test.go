package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	reportdomain "github.com/omnira/omnira/internal/reports/domain"
)

func TestCreateTemplate(t *testing.T) {
	svc := NewReportService()

	tenantID := uuid.New()
	createdBy := uuid.New()
	columns := []reportdomain.ReportColumn{
		{Field: "id", Label: "ID", Type: "string", Sortable: true},
	}

	template, err := svc.CreateTemplate(
		context.Background(),
		tenantID,
		createdBy,
		"Test Report",
		"",
		reportdomain.ReportTypeTickets,
		columns,
	)

	if err != nil {
		t.Errorf("failed to create template: %v", err)
	}

	if template.ID == uuid.Nil {
		t.Errorf("template id should not be nil")
	}
}

func TestCreateTemplateInvalid(t *testing.T) {
	svc := NewReportService()

	tenantID := uuid.New()
	createdBy := uuid.New()

	// No columns
	_, err := svc.CreateTemplate(
		context.Background(),
		tenantID,
		createdBy,
		"Test Report",
		"",
		reportdomain.ReportTypeTickets,
		[]reportdomain.ReportColumn{},
	)

	if err == nil {
		t.Errorf("should error when no columns provided")
	}

	// No name
	columns := []reportdomain.ReportColumn{
		{Field: "id", Label: "ID"},
	}

	_, err = svc.CreateTemplate(
		context.Background(),
		tenantID,
		createdBy,
		"",
		"",
		reportdomain.ReportTypeTickets,
		columns,
	)

	if err == nil {
		t.Errorf("should error when no name provided")
	}
}

func TestGenerateReport(t *testing.T) {
	svc := NewReportService()

	tenantID := uuid.New()
	templateID := uuid.New()
	executedBy := uuid.New()

	report, err := svc.GenerateReport(
		context.Background(),
		tenantID,
		templateID,
		executedBy,
		[]reportdomain.ReportFilter{},
	)

	if err != nil {
		t.Errorf("failed to generate report: %v", err)
	}

	if report.Status != "completed" {
		t.Errorf("report should be completed")
	}

	if report.TotalRows == 0 {
		t.Errorf("report should have rows")
	}
}

func TestExportReportAsJSON(t *testing.T) {
	svc := NewReportService()

	report := reportdomain.NewReport(uuid.New(), uuid.New(), uuid.New(), "Test", reportdomain.ReportTypeTickets, nil)
	report.AddRow(map[string]interface{}{"id": "1"})
	report.Complete(100)

	data, err := svc.ExportReportAsJSON(report)

	if err != nil {
		t.Errorf("failed to export: %v", err)
	}

	if len(data) == 0 {
		t.Errorf("exported data should not be empty")
	}
}

func TestExportReportAsCSV(t *testing.T) {
	svc := NewReportService()

	report := reportdomain.NewReport(uuid.New(), uuid.New(), uuid.New(), "Test", reportdomain.ReportTypeTickets, nil)
	report.AddRow(map[string]interface{}{"id": "1", "status": "open", "priority": "high"})
	report.Complete(100)

	csv, err := svc.ExportReportAsCSV(report)

	if err != nil {
		t.Errorf("failed to export: %v", err)
	}

	if len(csv) == 0 {
		t.Errorf("exported CSV should not be empty")
	}

	if csv[0] != 'i' {
		t.Errorf("CSV should start with headers")
	}
}
