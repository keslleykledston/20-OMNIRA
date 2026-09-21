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
func TestCRMHandlersReadRouteWildcards(t *testing.T) {
	h := NewCRMHandlers(nil)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket", h.CreateTicket)
	mux.HandleFunc("GET /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/{ticket_id}", h.GetTicket)
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/crm/activity", h.CreateActivity)

	base := "/api/v1/tenants/" + uuid.NewString() + "/conversations/" + uuid.NewString()
	cases := []struct {
		name, method, path, body string
		wantCode                 int
		wantBody                 string
	}{
		{"create ticket", "POST", base + "/ticket", `{}`, http.StatusBadRequest, "subject required"},
		{"get ticket", "GET", base + "/ticket/T-1", ``, http.StatusNotFound, "ticket not found"},
		{"create activity", "POST", base + "/crm/activity", `{}`, http.StatusBadRequest, "subject, contact_id and company_id required"},
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
