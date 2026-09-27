package adapters_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

// TestMessageItemNeverExposesMediaRef proves that public MessageItem DTO
// never serializes media_ref or provider URL to JSON. This is a security
// regression test: media references must only be accessed through authenticated
// media endpoint, never embedded in message listing/detail.
func TestMessageItemNeverExposesMediaRef(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := seed.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	tenantID, userID := uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, userID, userID, userID.String()+"@invalid")
	exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenantID, tenantID.String())
	var role uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantID, userID, role)

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id=$1`, tenantID)
		_, _ = seed.Exec(bg, `DELETE FROM users WHERE id=$1`, userID)
	})

	channelConnID := uuid.New()
	// external_number_id is unique per (provider, external_number_id) — derive
	// it from the connection's own ID (like other packages' nextPhone()-style
	// helpers) instead of a fixed literal, so concurrent/repeated runs never
	// collide on a leftover row.
	exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status)
	      VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active')`, channelConnID, tenantID, channelConnID.String())

	contactID := uuid.New()
	exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'Test','+15551234567')`, contactID, tenantID)

	convID := uuid.New()
	exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status,title,created_at,updated_at)
	      VALUES($1,$2,$3,$4,'open','Test',NOW(),NOW())`, convID, tenantID, contactID, channelConnID)

	msgID := uuid.New()
	mediaRef := "https://untrusted-waha.local/media/abc123"
	exec(`INSERT INTO messages(id,tenant_id,conversation_id,channel_connection_id,direction,message_type,body,media_ref,mime_type,size_bytes,status,created_at,updated_at)
	      VALUES($1,$2,$3,$4,'inbound','image','',$5,'image/jpeg',5242880,'received',NOW(),NOW())`,
		msgID, tenantID, convID, channelConnID, mediaRef)

	// Setup handler with AuthorizationMiddleware.
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	handler := inboxadapters.NewInboxAPIHandler(app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages", tenancyadapters.AuthorizationMiddleware(app, authz)(http.HandlerFunc(handler.ListMessages)))

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/tenants/%s/inbox/conversations/%s/messages", tenantID, convID), nil)
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: userID, Subject: userID.String()}))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	var found bool
	for _, item := range resp.Items {
		id, ok := item["id"]
		if ok && fmt.Sprint(id) == msgID.String() {
			found = true
			if _, hasMediaRef := item["media_ref"]; hasMediaRef {
				t.Errorf("SECURITY REGRESSION: MessageItem EXPOSED media_ref in public DTO")
			}
			break
		}
	}
	if !found {
		t.Fatalf("message not found in response")
	}

	bodyStr := w.Body.String()
	if contains(bodyStr, "media_ref") {
		t.Errorf("REGRESSION: response JSON contains 'media_ref' key")
	}
}

// PILOT.4A2 test J (API): the new 'uncertain' terminal status serializes
// through the real message API verbatim — never silently coerced to
// 'failed' or dropped — while existing statuses are unaffected.
func TestMessageItemSerializesUncertainStatusWithoutCoercion(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := seed.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	tenantID, userID := uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, userID, userID, userID.String()+"@invalid")
	exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenantID, tenantID.String())
	var role uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantID, userID, role)

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id=$1`, tenantID)
		_, _ = seed.Exec(bg, `DELETE FROM users WHERE id=$1`, userID)
	})

	channelConnID := uuid.New()
	exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status)
	      VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active')`, channelConnID, tenantID, channelConnID.String())

	contactID := uuid.New()
	exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'Test','+15551234568')`, contactID, tenantID)

	convID := uuid.New()
	exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status,title,created_at,updated_at)
	      VALUES($1,$2,$3,$4,'open','Test',NOW(),NOW())`, convID, tenantID, contactID, channelConnID)

	sentID, uncertainID := uuid.New(), uuid.New()
	exec(`INSERT INTO messages(id,tenant_id,conversation_id,channel_connection_id,direction,message_type,body,status,provider_message_id,created_at,updated_at)
	      VALUES($1,$2,$3,$4,'outbound','text','confirmed','sent','waha-confirmed-1',NOW(),NOW())`,
		sentID, tenantID, convID, channelConnID)
	exec(`INSERT INTO messages(id,tenant_id,conversation_id,channel_connection_id,direction,message_type,body,status,reserved_provider_message_id,failure_reason,created_at,updated_at)
	      VALUES($1,$2,$3,$4,'outbound','text','unproven','uncertain','reserved-abc','outcome_unknown:provider_id_mismatch',NOW(),NOW())`,
		uncertainID, tenantID, convID, channelConnID)

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	handler := inboxadapters.NewInboxAPIHandler(app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages", tenancyadapters.AuthorizationMiddleware(app, authz)(http.HandlerFunc(handler.ListMessages)))

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/tenants/%s/inbox/conversations/%s/messages", tenantID, convID), nil)
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: userID, Subject: userID.String()}))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	statuses := map[string]string{}
	for _, item := range resp.Items {
		if id, ok := item["id"]; ok {
			statuses[fmt.Sprint(id)] = fmt.Sprint(item["status"])
		}
	}
	if got := statuses[sentID.String()]; got != "sent" {
		t.Errorf("existing 'sent' status changed: got %q", got)
	}
	if got := statuses[uncertainID.String()]; got != "uncertain" {
		t.Errorf("uncertain status was coerced or dropped: got %q, want \"uncertain\"", got)
	}
}

func contains(s, substr string) bool {
	for i := 0; i < len(s)-len(substr)+1; i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
