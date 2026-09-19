package health

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// HealthResponse — resposta de health check.
type HealthResponse struct {
	Status     Status                         `json:"status"`
	Timestamp  time.Time                      `json:"timestamp"`
	Components map[string]*ComponentHealth    `json:"components"`
}

// HealthHandler — HTTP handler para /healthz.
func (h *HealthCheck) HealthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	components := h.Check(r.Context())

	// Determinar status global
	status := StatusHealthy
	for _, component := range components {
		if component.Status != StatusHealthy {
			status = StatusUnhealthy
			break
		}
	}

	response := HealthResponse{
		Status:     status,
		Timestamp:  time.Now(),
		Components: components,
	}

	// HTTP status code
	statusCode := http.StatusOK
	if status != StatusHealthy {
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}

// MetricsResponse — resposta de métricas em Prometheus text format.
type MetricsResponse struct {
	text string
}

// MetricsHandler — HTTP handler para /metrics (Prometheus text format).
func (h *HealthCheck) MetricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	components := h.Check(r.Context())

	// Prometheus text format
	var sb strings.Builder

	sb.WriteString("# HELP omnira_health_status Health status of components (1=healthy, 0=unhealthy)\n")
	sb.WriteString("# TYPE omnira_health_status gauge\n")

	for _, component := range components {
		statusVal := 0
		if component.Status == StatusHealthy {
			statusVal = 1
		}
		sb.WriteString("omnira_health_status{component=\"")
		sb.WriteString(component.Name)
		sb.WriteString("\"} ")
		sb.WriteString(json.Number(string(rune(statusVal))).String())
		sb.WriteString("\n")
	}

	sb.WriteString("\n# HELP omnira_health_latency_ms Latency of health check in milliseconds\n")
	sb.WriteString("# TYPE omnira_health_latency_ms gauge\n")

	for _, component := range components {
		sb.WriteString("omnira_health_latency_ms{component=\"")
		sb.WriteString(component.Name)
		sb.WriteString("\"} ")
		sb.WriteString(json.Number(string(rune(component.Latency.Milliseconds()))).String())
		sb.WriteString("\n")
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(sb.String()))
}
