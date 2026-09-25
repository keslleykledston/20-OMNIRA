package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// PRODUCT.6-O2B2: K3GTicketingConnector.UpdateTicketStatus, against the
// confirmed real K3G wire contract (PRODUCT.6-O2A3, live evidence ticket
// 9115): PUT /api/support/tickets/{id}/status, body {"status":<int>}. All
// HTTP is a local httptest server — no live K3G call anywhere in this file.

// A/B/C. valid codes 1/5/6 send the exact integer wire value.
func TestK3GUpdateTicketStatusSendsExactWireBody(t *testing.T) {
	cases := []struct {
		code string
		want float64
	}{
		{"1", 1}, {"5", 5}, {"6", 6},
	}
	for _, c := range cases {
		t.Run("code_"+c.code, func(t *testing.T) {
			var gotBody map[string]any
			conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok": true, "source": "crm",
					"ticket": map[string]any{"id": 9115, "statusId": c.want, "status": c.code, "statusLabel": "x"},
				})
			})
			if _, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: c.code}); err != nil {
				t.Fatalf("UpdateTicketStatus: %v", err)
			}
			if len(gotBody) != 1 {
				t.Fatalf("request body has %d keys, want exactly 1: %v", len(gotBody), gotBody)
			}
			if got, _ := gotBody["status"].(float64); got != c.want {
				t.Errorf("body[status] = %v, want %v", got, c.want)
			}
		})
	}
}

// D. an invalid target is rejected BEFORE any HTTP call.
func TestK3GUpdateTicketStatusRejectsInvalidTargetBeforeHTTPCall(t *testing.T) {
	invalid := []string{"", "0", "7", "resolved", "closed", "05", " 1", "1 "}
	for _, code := range invalid {
		t.Run("code_"+code, func(t *testing.T) {
			var called int64
			conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt64(&called, 1)
				w.WriteHeader(http.StatusOK)
			})
			_, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: code})
			if err == nil {
				t.Fatalf("expected error for invalid code %q", code)
			}
			if got := TicketingErrorCodeOf(err); got != TicketingValidationError {
				t.Fatalf("code = %q, want VALIDATION_ERROR", got)
			}
			if atomic.LoadInt64(&called) != 0 {
				t.Fatalf("provider must not be called for an invalid target, got %d calls", called)
			}
		})
	}
}

// E/F/G. method, path and Authorization header.
func TestK3GUpdateTicketStatusRequestShape(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotContentType string
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth, gotContentType = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "source": "crm",
			"ticket": map[string]any{"id": 9115, "statusId": 5, "status": "5", "statusLabel": "Resolvido"},
		})
	})
	if _, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: "5"}); err != nil {
		t.Fatalf("UpdateTicketStatus: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/api/support/tickets/9115/status" {
		t.Errorf("path = %q, want /api/support/tickets/9115/status", gotPath)
	}
	if gotAuth != "Bearer test-token-do-not-log" {
		t.Errorf("Authorization header not sent correctly (value redacted in this failure message on purpose)")
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
}

// H/I. successful response maps ExternalID/ExternalStatus/ExternalStatusLabel,
// and the returned status matches the requested target.
func TestK3GUpdateTicketStatusMapsSuccessResponse(t *testing.T) {
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "source": "crm",
			"ticket": map[string]any{
				"id": 9115, "statusId": 5, "status": "5", "statusLabel": "Resolvido",
				"solvedate": "2026-09-24T23:31:13.261Z", "closedate": nil,
			},
			"crmCompany": map[string]any{"id": "c1"},
		})
	})
	ticket, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: "5"})
	if err != nil {
		t.Fatalf("UpdateTicketStatus: %v", err)
	}
	if ticket.ExternalID != "9115" {
		t.Errorf("ExternalID = %q, want 9115", ticket.ExternalID)
	}
	if ticket.ExternalStatus != "5" {
		t.Errorf("ExternalStatus = %q, want 5", ticket.ExternalStatus)
	}
	if ticket.ExternalStatusLabel != "Resolvido" {
		t.Errorf("ExternalStatusLabel = %q, want Resolvido", ticket.ExternalStatusLabel)
	}
}

// J. a syntactically successful 2xx with no ticket at all -> WRITE_OUTCOME_UNKNOWN.
func TestK3GUpdateTicketStatusMissingTicketIsWriteOutcomeUnknown(t *testing.T) {
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "source": "crm"})
	})
	_, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: "5"})
	if got := TicketingErrorCodeOf(err); got != TicketingWriteOutcomeUnknown {
		t.Fatalf("code = %q, want WRITE_OUTCOME_UNKNOWN", got)
	}
}

// K. missing/invalid external id in an otherwise-successful 2xx -> WRITE_OUTCOME_UNKNOWN.
func TestK3GUpdateTicketStatusMissingExternalIDIsWriteOutcomeUnknown(t *testing.T) {
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "source": "crm",
			"ticket": map[string]any{"id": 0, "statusId": 5, "status": "5", "statusLabel": "Resolvido"},
		})
	})
	_, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: "5"})
	if got := TicketingErrorCodeOf(err); got != TicketingWriteOutcomeUnknown {
		t.Fatalf("code = %q, want WRITE_OUTCOME_UNKNOWN", got)
	}
}

// L. the provider's returned status disagrees with the requested target
// after an otherwise well-formed 2xx -> WRITE_OUTCOME_UNKNOWN, never a
// silently-accepted mismatch.
func TestK3GUpdateTicketStatusMismatchedReturnedStatusIsWriteOutcomeUnknown(t *testing.T) {
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "source": "crm",
			// requested target "5", provider echoes back "2".
			"ticket": map[string]any{"id": 9115, "statusId": 2, "status": "2", "statusLabel": "Em atendimento"},
		})
	})
	_, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: "5"})
	if got := TicketingErrorCodeOf(err); got != TicketingWriteOutcomeUnknown {
		t.Fatalf("code = %q, want WRITE_OUTCOME_UNKNOWN", got)
	}
}

// M. malformed JSON on a 2xx -> WRITE_OUTCOME_UNKNOWN.
func TestK3GUpdateTicketStatusMalformedJSONIsWriteOutcomeUnknown(t *testing.T) {
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	})
	_, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: "5"})
	if got := TicketingErrorCodeOf(err); got != TicketingWriteOutcomeUnknown {
		t.Fatalf("code = %q, want WRITE_OUTCOME_UNKNOWN", got)
	}
}

// N. a transport failure (abrupt connection close) -> WRITE_OUTCOME_UNKNOWN
// (mutating), never PROVIDER_UNAVAILABLE.
func TestK3GUpdateTicketStatusTransportFailureIsWriteOutcomeUnknown(t *testing.T) {
	var attempts int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter does not support hijacking")
		}
		c, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = c.Close()
	}))
	t.Cleanup(server.Close)
	conn, err := NewK3GTicketingConnector(K3GTicketingConfig{BaseURL: server.URL, Token: "t"})
	if err != nil {
		t.Fatalf("NewK3GTicketingConnector: %v", err)
	}
	_, err = conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: "5"})
	if got := TicketingErrorCodeOf(err); got != TicketingWriteOutcomeUnknown {
		t.Fatalf("code = %q, want WRITE_OUTCOME_UNKNOWN", got)
	}
	if got := atomic.LoadInt64(&attempts); got != 1 {
		t.Fatalf("server received %d connection attempts, want exactly 1 (no automatic retry)", got)
	}
}

// O/P/Q/R/S/T/U. HTTP status classification for the mutating status route.
func TestK3GUpdateTicketStatusErrorClassification(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode TicketingErrorCode
	}{
		{"400 -> definitive VALIDATION_ERROR", http.StatusBadRequest, `{"error":"bad request"}`, TicketingValidationError},
		{"401 -> definitive UNAUTHORIZED", http.StatusUnauthorized, `{"error":"unauthorized"}`, TicketingUnauthorized},
		{"403 -> definitive UNAUTHORIZED", http.StatusForbidden, `{"error":"forbidden"}`, TicketingUnauthorized},
		{"404 -> provider-neutral NOT_FOUND", http.StatusNotFound, `{"error":"not found"}`, TicketingNotFound},
		{"404 CRM_TICKET_NOT_MIGRATED -> NOT_MIGRATED", http.StatusNotFound, `{"error":"not migrated","code":"CRM_TICKET_NOT_MIGRATED"}`, TicketingNotMigrated},
		{"422 -> definitive VALIDATION_ERROR", http.StatusUnprocessableEntity, `{"error":"semantically invalid"}`, TicketingValidationError},
		{"429 -> WRITE_OUTCOME_UNKNOWN", http.StatusTooManyRequests, `{"error":"rate limited"}`, TicketingWriteOutcomeUnknown},
		{"500 -> WRITE_OUTCOME_UNKNOWN", http.StatusInternalServerError, `{"error":"boom"}`, TicketingWriteOutcomeUnknown},
		{"503 -> WRITE_OUTCOME_UNKNOWN", http.StatusServiceUnavailable, `{"error":"unavailable"}`, TicketingWriteOutcomeUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var attempts int64
			conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt64(&attempts, 1)
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			})
			_, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: "5"})
			if err == nil {
				t.Fatalf("expected error")
			}
			if got := TicketingErrorCodeOf(err); got != c.wantCode {
				t.Fatalf("code = %q, want %q (err=%v)", got, c.wantCode, err)
			}
			if got := atomic.LoadInt64(&attempts); got != 1 {
				t.Fatalf("server received %d requests, want exactly 1 (no automatic retry)", got)
			}
		})
	}
}

// V. exactly one PUT is issued even on an ambiguous (WRITE_OUTCOME_UNKNOWN)
// outcome — no retry loop anywhere in this adapter.
func TestK3GUpdateTicketStatusExactlyOnePUTOnAmbiguity(t *testing.T) {
	var attempts int64
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"unavailable"}`))
	})
	_, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: "5"})
	if got := TicketingErrorCodeOf(err); got != TicketingWriteOutcomeUnknown {
		t.Fatalf("code = %q, want WRITE_OUTCOME_UNKNOWN", got)
	}
	if got := atomic.LoadInt64(&attempts); got != 1 {
		t.Fatalf("server received %d requests, want exactly 1", got)
	}
}

// W. UpdateTicketStatus never automatically calls GetTicket — a single
// endpoint hit per invocation, structurally proven by asserting every
// request this fake server sees is a PUT to the status route, never a GET.
func TestK3GUpdateTicketStatusNeverAutomaticallyCallsGetTicket(t *testing.T) {
	var sawGet bool
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			sawGet = true
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "source": "crm",
			"ticket": map[string]any{"id": 9115, "statusId": 5, "status": "5", "statusLabel": "Resolvido"},
		})
	})
	if _, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: "5"}); err != nil {
		t.Fatalf("UpdateTicketStatus: %v", err)
	}
	if sawGet {
		t.Fatal("UpdateTicketStatus must never issue a GET on its own")
	}
}

// X. same-target mutation ("5" while already "5") requires no special
// adapter logic — it is just another PUT with the requested integer.
func TestK3GUpdateTicketStatusSameTargetNoSpecialHandling(t *testing.T) {
	var attempts int64
	var gotBody map[string]any
	conn, _ := newTestK3GTicketingConnector(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "source": "crm",
			"ticket": map[string]any{"id": 9115, "statusId": 5, "status": "5", "statusLabel": "Resolvido"},
		})
	})
	ticket, err := conn.UpdateTicketStatus(context.Background(), "9115", ExternalStatusTarget{Code: "5"})
	if err != nil {
		t.Fatalf("UpdateTicketStatus: %v", err)
	}
	if ticket.ExternalStatus != "5" {
		t.Fatalf("ExternalStatus = %q, want 5", ticket.ExternalStatus)
	}
	if got, _ := gotBody["status"].(float64); got != 5 {
		t.Fatalf("body[status] = %v, want 5", got)
	}
	if got := atomic.LoadInt64(&attempts); got != 1 {
		t.Fatalf("exactly one PUT expected, got %d", got)
	}
}

func TestK3GTicketingConnectorStillSatisfiesTicketingConnectorAfterExtension(t *testing.T) {
	var _ TicketingConnector = (*K3GTicketingConnector)(nil)
}
