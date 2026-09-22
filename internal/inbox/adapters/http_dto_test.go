package adapters_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
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
	exec(`INSERT INTO channel_connections(id,tenant_id,provider,status,external_id) VALUES($1,$2,'whatsapp','active',$3)`, channelConnID, tenantID, "12345")

	contactID := uuid.New()
	exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'Test','15551234567')`, contactID, tenantID)

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

func contains(s, substr string) bool {
	for i := 0; i < len(s)-len(substr)+1; i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
