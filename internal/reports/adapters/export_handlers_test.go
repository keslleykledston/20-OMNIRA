package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/reports/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func TestExportHandlersCompile(t *testing.T) {
	svc := application.NewReportService()
	_ = NewExportHandlers(svc)
}

func TestExportReportJSON(t *testing.T) {
	svc := application.NewReportService()
	handlers := NewExportHandlers(svc)

	tenantCtx, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.New(), tenancydomain.AccessSourceDirect)
	reportID := uuid.New()

	req := httptest.NewRequest("GET", "/api/v1/reports/"+reportID.String()+"/export/json", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", reportID.String())
	rec := httptest.NewRecorder()

	handlers.ExportReportJSON(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected json content type")
	}
}

func TestExportReportCSV(t *testing.T) {
	svc := application.NewReportService()
	handlers := NewExportHandlers(svc)

	tenantCtx, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.New(), tenancydomain.AccessSourceDirect)
	reportID := uuid.New()

	req := httptest.NewRequest("GET", "/api/v1/reports/"+reportID.String()+"/export/csv", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", reportID.String())
	rec := httptest.NewRecorder()

	handlers.ExportReportCSV(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if rec.Header().Get("Content-Type") != "text/csv; charset=utf-8" {
		t.Errorf("expected csv content type")
	}
}

func TestExportReportPDF(t *testing.T) {
	svc := application.NewReportService()
	handlers := NewExportHandlers(svc)

	tenantCtx, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.New(), tenancydomain.AccessSourceDirect)
	reportID := uuid.New()

	req := httptest.NewRequest("GET", "/api/v1/reports/"+reportID.String()+"/export/pdf", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", reportID.String())
	rec := httptest.NewRecorder()

	handlers.ExportReportPDF(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if rec.Header().Get("Content-Type") != "application/pdf" {
		t.Errorf("expected pdf content type")
	}
}

func TestExportReportExcel(t *testing.T) {
	svc := application.NewReportService()
	handlers := NewExportHandlers(svc)

	tenantCtx, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.New(), tenancydomain.AccessSourceDirect)
	reportID := uuid.New()

	req := httptest.NewRequest("GET", "/api/v1/reports/"+reportID.String()+"/export/xlsx", nil)
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	req.SetPathValue("id", reportID.String())
	rec := httptest.NewRecorder()

	handlers.ExportReportExcel(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if rec.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("expected excel content type")
	}
}

func TestScheduleExport(t *testing.T) {
	svc := application.NewReportService()
	handlers := NewExportHandlers(svc)

	tenantCtx, _ := tenancydomain.NewTenantContext(uuid.New(), uuid.New(), tenancydomain.AccessSourceDirect)

	body := []byte(`{"format":"csv","schedule":"daily","email":"user@example.com"}`)
	req := httptest.NewRequest("POST", "/api/v1/reports/export/schedule", bytes.NewReader(body))
	req = req.WithContext(tenancydomain.WithTenantContext(context.Background(), tenantCtx))
	rec := httptest.NewRecorder()

	handlers.ScheduleExport(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}

	var resp ScheduleExportResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Errorf("failed to decode response: %v", err)
	}

	if resp.Status != "scheduled" {
		t.Errorf("expected scheduled status")
	}
}
