package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthCheckWithNilPool(t *testing.T) {
	h := NewHealthCheck(nil, nil)
	components := h.Check(context.Background())

	if components["database"].Status != StatusUnknown {
		t.Errorf("expected database status=unknown, got %s", components["database"].Status)
	}

	if components["nats"].Status != StatusUnknown {
		t.Errorf("expected nats status=unknown, got %s", components["nats"].Status)
	}

	if h.IsHealthy() {
		t.Errorf("expected IsHealthy=false with unknown components")
	}
}

func TestHealthHandler_MethodNotAllowed(t *testing.T) {
	h := NewHealthCheck(nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	w := httptest.NewRecorder()

	h.HealthHandler(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestHealthHandler_UnhealthyReturns503(t *testing.T) {
	h := NewHealthCheck(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	h.HealthHandler(w, req)

	// Should return 503 because components are unknown
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}
}

func TestHealthHandler_ContentType(t *testing.T) {
	h := NewHealthCheck(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	h.HealthHandler(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected content-type application/json, got %s", ct)
	}
}

func TestMetricsHandler_MethodNotAllowed(t *testing.T) {
	h := NewHealthCheck(nil, nil)

	req := httptest.NewRequest(http.MethodPut, "/metrics", nil)
	w := httptest.NewRecorder()

	h.MetricsHandler(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestMetricsHandler_Returns200(t *testing.T) {
	h := NewHealthCheck(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	h.MetricsHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestMetricsHandler_PrometheusFormat(t *testing.T) {
	h := NewHealthCheck(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	h.MetricsHandler(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "text/plain; version=0.0.4" {
		t.Errorf("expected prometheus format content-type, got %s", ct)
	}

	body := w.Body.String()
	if !contains(body, "omnira_health_status") {
		t.Errorf("expected omnira_health_status metric in response")
	}

	if !contains(body, "omnira_health_latency_ms") {
		t.Errorf("expected omnira_health_latency_ms metric in response")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
