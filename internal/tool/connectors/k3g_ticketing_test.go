package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func newTestK3GTicketingConnector(t *testing.T, handler http.HandlerFunc) (*K3GTicketingConnector, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	conn, err := NewK3GTicketingConnector(K3GTicketingConfig{BaseURL: server.URL, Token: "test-token-do-not-log"})
	if err != nil {
		t.Fatalf("NewK3GTicketingConnector: %v", err)
	}
	return conn, server
}

// A. CreateTicket sends POST /api/support/tickets.
// B. body contains exactly companyId/name/content.
// C. no requester/category/service/priority/urgency/assignee/status fields.
// D. Bearer auth sent correctly, never logged/leaked.
func TestK3GTicketingCreateTicketSendsExactMinimalPayload(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]any
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"source": "crm",
			"ticket": map[string]any{"id": 28180, "statusId": 1, "status": "1", "statusLabel": "Novo"},
		})
	})

	_, err := conn.CreateTicket(context.Background(), CreateTicketRequest{
		CustomerExternalID: "d38e7970-635d-490b-a119-749ee6f1fe23",
		Subject:            "subject",
		Description:        "description",
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/support/tickets" {
		t.Errorf("path = %q, want /api/support/tickets", gotPath)
	}
	if gotAuth != "Bearer test-token-do-not-log" {
		t.Errorf("Authorization header not sent correctly (value redacted in this failure message on purpose)")
	}

	wantKeys := map[string]string{"companyId": "d38e7970-635d-490b-a119-749ee6f1fe23", "name": "subject", "content": "description"}
	if len(gotBody) != len(wantKeys) {
		t.Fatalf("request body has %d keys, want exactly %d: %v", len(gotBody), len(wantKeys), gotBody)
	}
	for k, want := range wantKeys {
		got, _ := gotBody[k].(string)
		if got != want {
			t.Errorf("body[%q] = %q, want %q", k, got, want)
		}
	}
	forbidden := []string{"requesterUserId", "requesterContactId", "categoryKey", "serviceKey", "priority", "urgency", "assigneeUserId", "assignedGroupId", "statusId", "status"}
	for _, k := range forbidden {
		if _, present := gotBody[k]; present {
			t.Errorf("request body must not include %q, got %v", k, gotBody[k])
		}
	}
}

// E. HTTP 201 maps numeric ticket.id to the neutral string external ID.
// F. status/statusLabel map correctly.
func TestK3GTicketingCreateTicketMapsSuccessResponse(t *testing.T) {
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"source": "crm",
			"ticket": map[string]any{"id": 28180, "statusId": 1, "status": "1", "statusLabel": "Novo"},
		})
	})

	ticket, err := conn.CreateTicket(context.Background(), CreateTicketRequest{CustomerExternalID: "c1", Subject: "s", Description: "d"})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if ticket.ExternalID != "28180" {
		t.Errorf("ExternalID = %q, want %q", ticket.ExternalID, "28180")
	}
	if ticket.ExternalStatus != "1" {
		t.Errorf("ExternalStatus = %q, want %q", ticket.ExternalStatus, "1")
	}
	if ticket.ExternalStatusLabel != "Novo" {
		t.Errorf("ExternalStatusLabel = %q, want %q", ticket.ExternalStatusLabel, "Novo")
	}
}

func TestK3GTicketingGetTicketMapsResponse(t *testing.T) {
	var gotMethod, gotPath string
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ticket": map[string]any{"id": 28176, "statusId": 1, "status": "1", "statusLabel": "Novo"},
		})
	})

	ticket, err := conn.GetTicket(context.Background(), "28176")
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/support/tickets/28176" {
		t.Errorf("request = %s %s, want GET /api/support/tickets/28176", gotMethod, gotPath)
	}
	if ticket.ExternalID != "28176" || ticket.ExternalStatusLabel != "Novo" {
		t.Errorf("unexpected ticket: %+v", ticket)
	}
}

// G-L: error classification.
func TestK3GTicketingErrorClassification(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode TicketingErrorCode
	}{
		{"400 -> VALIDATION_ERROR", http.StatusBadRequest, `{"error":"bad request"}`, TicketingValidationError},
		{"401 -> UNAUTHORIZED", http.StatusUnauthorized, `{"error":"unauthorized"}`, TicketingUnauthorized},
		{"403 -> UNAUTHORIZED", http.StatusForbidden, `{"error":"forbidden"}`, TicketingUnauthorized},
		{"404 CRM_TICKET_NOT_MIGRATED -> NOT_MIGRATED", http.StatusNotFound, `{"error":"not migrated","code":"CRM_TICKET_NOT_MIGRATED"}`, TicketingNotMigrated},
		{"generic 404 -> NOT_FOUND", http.StatusNotFound, `{"error":"not found"}`, TicketingNotFound},
		{"429 -> PROVIDER_UNAVAILABLE", http.StatusTooManyRequests, `{"error":"rate limited"}`, TicketingProviderUnavailable},
		{"503 -> PROVIDER_UNAVAILABLE", http.StatusServiceUnavailable, `{"error":"unavailable"}`, TicketingProviderUnavailable},
		{"malformed 201 -> UNKNOWN_PROVIDER_ERROR", http.StatusCreated, `not json`, TicketingUnknownProviderError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			})
			_, err := conn.GetTicket(context.Background(), "1")
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if got := TicketingErrorCodeOf(err); got != c.wantCode {
				t.Fatalf("code = %q, want %q (err=%v)", got, c.wantCode, err)
			}
		})
	}
}

// K (transport variant): a network/transport failure classifies as
// PROVIDER_UNAVAILABLE, same as 503.
func TestK3GTicketingTransportErrorClassifiesAsProviderUnavailable(t *testing.T) {
	conn, err := NewK3GTicketingConnector(K3GTicketingConfig{BaseURL: "http://127.0.0.1:1", Token: "t"})
	if err != nil {
		t.Fatalf("NewK3GTicketingConnector: %v", err)
	}
	_, err = conn.GetTicket(context.Background(), "1")
	if err == nil {
		t.Fatalf("expected a transport error")
	}
	if got := TicketingErrorCodeOf(err); got != TicketingProviderUnavailable {
		t.Fatalf("code = %q, want PROVIDER_UNAVAILABLE", got)
	}
}

// M. Create performs exactly ONE POST when the provider returns failure —
// proves no retry middleware exists anywhere in this path.
func TestK3GTicketingCreateTicketNeverRetriesOnFailure(t *testing.T) {
	var attempts int64
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"unavailable"}`))
	})

	_, err := conn.CreateTicket(context.Background(), CreateTicketRequest{CustomerExternalID: "c", Subject: "s", Description: "d"})
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := TicketingErrorCodeOf(err); got != TicketingProviderUnavailable {
		t.Fatalf("code = %q, want PROVIDER_UNAVAILABLE", got)
	}
	if got := atomic.LoadInt64(&attempts); got != 1 {
		t.Fatalf("server received %d requests, want exactly 1 (no automatic retry)", got)
	}
}

// Same no-retry proof for a transport-level failure (server closes the
// connection before responding) rather than an HTTP error status.
func TestK3GTicketingCreateTicketNeverRetriesOnTransportFailure(t *testing.T) {
	var attempts int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter does not support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = conn.Close() // abrupt close, no response — simulates an ambiguous transport failure
	}))
	t.Cleanup(server.Close)

	conn, err := NewK3GTicketingConnector(K3GTicketingConfig{BaseURL: server.URL, Token: "t"})
	if err != nil {
		t.Fatalf("NewK3GTicketingConnector: %v", err)
	}
	_, err = conn.CreateTicket(context.Background(), CreateTicketRequest{CustomerExternalID: "c", Subject: "s", Description: "d"})
	if err == nil {
		t.Fatalf("expected a transport error")
	}
	if got := TicketingErrorCodeOf(err); got != TicketingProviderUnavailable {
		t.Fatalf("code = %q, want PROVIDER_UNAVAILABLE", got)
	}
	if got := atomic.LoadInt64(&attempts); got != 1 {
		t.Fatalf("server received %d connection attempts, want exactly 1 (no automatic retry)", got)
	}
}

func TestK3GTicketingConnectorSatisfiesTicketingConnector(t *testing.T) {
	var _ TicketingConnector = (*K3GTicketingConnector)(nil)
}

func TestK3GTicketingConnectorRejectsEmptyConfig(t *testing.T) {
	if _, err := NewK3GTicketingConnector(K3GTicketingConfig{}); err == nil {
		t.Fatal("expected error for empty config")
	}
	if _, err := NewK3GTicketingConnector(K3GTicketingConfig{BaseURL: "not-a-url", Token: "t"}); err == nil {
		t.Fatal("expected error for non-absolute base URL")
	}
}
