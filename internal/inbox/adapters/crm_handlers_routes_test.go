package adapters

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/platform/authn"
)

// The handlers must read the same wildcard names the routes are registered with
// (snake_case in httpserver/server.go). A mismatch makes PathValue return "" and
// every request fail with 400 "missing ... ID".
// PRODUCT.6-B: with no real ERP ticketing connector configured (canonical
// runtime state today, see NewCRMHandlers), every ticket CRUD route must
// fail with the explicit "not configured" response before reaching any
// request-shape validation — never a fake success, never a fallback to
// mock/local state. CreateActivity is unaffected: it is the K3G
// company/activity path, a semantically separate feature that does not use
// h.crm.
func TestCRMHandlersReadRouteWildcards(t *testing.T) {
	h := NewCRMHandlers(nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket", h.GetCurrentTicket)
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket", h.CreateTicket)
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/{ticket_id}", h.GetTicket)
	mux.HandleFunc("PATCH /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/{ticket_id}", h.UpdateTicket)
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/{ticket_id}/close", h.CloseTicket)
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/crm/activity", h.CreateActivity)

	base := "/api/v1/tenants/" + uuid.NewString() + "/conversations/" + uuid.NewString()
	const unavailable = "ticketing integration not configured for this tenant"
	cases := []struct {
		name, method, path, body string
		wantCode                 int
		wantBody                 string
	}{
		{"get current ticket unavailable", "GET", base + "/ticket", ``, http.StatusServiceUnavailable, unavailable},
		{"create ticket unavailable", "POST", base + "/ticket", `{"subject":"x"}`, http.StatusServiceUnavailable, unavailable},
		{"get ticket unavailable", "GET", base + "/ticket/T-1", ``, http.StatusServiceUnavailable, unavailable},
		{"update ticket unavailable", "PATCH", base + "/ticket/T-1", `{"status":"resolved"}`, http.StatusServiceUnavailable, unavailable},
		{"close ticket unavailable", "POST", base + "/ticket/T-1/close", ``, http.StatusServiceUnavailable, unavailable},
		{"create activity (unaffected)", "POST", base + "/crm/activity", `{}`, http.StatusBadRequest, "subject, contact_id and company_id required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: uuid.New()}))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != c.wantCode || !strings.Contains(rec.Body.String(), c.wantBody) {
				t.Fatalf("got %d %q, want %d containing %q", rec.Code, rec.Body.String(), c.wantCode, c.wantBody)
			}
		})
	}
}
