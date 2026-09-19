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
	"github.com/omnira/omnira/internal/testhelpers"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

func TestGetConversationIsTenantScoped(t *testing.T) {
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
	tenantA, tenantB, userA, userB, assignee := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{userA, userB, assignee} {
		exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	}
	for _, tn := range []uuid.UUID{tenantA, tenantB} {
		exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id IN ($1,$2)`, tenantA, tenantB)
		_, _ = seed.Exec(bg, `DELETE FROM users WHERE id IN ($1,$2,$3)`, userA, userB, assignee)
	})
	var role uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantA, userA, role)
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantB, userB, role)
	conv := func(tn uuid.UUID, name string, owner *uuid.UUID) uuid.UUID {
		contact, c := uuid.New(), uuid.New()
		exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,$3,$4)`, contact, tn, name, fmt.Sprintf("+5511%09d", contact.ID()%1000000000))
		exec(`INSERT INTO conversations(id,tenant_id,contact_id,status,assigned_to_user_id) VALUES($1,$2,$3,'open',$4)`, c, tn, contact, owner)
		return c
	}
	convA, convB := conv(tenantA, "Alice", &assignee), conv(tenantB, "Bruno", nil)

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	h := inboxadapters.NewInboxAPIHandler(app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}", tenancyadapters.AuthorizationMiddleware(app, authz)(http.HandlerFunc(h.GetConversation)))
	get := func(user, tenant uuid.UUID, id string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tenant.String()+"/inbox/conversations/"+id, nil)
		req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	code, body := get(userA, tenantA, convA.String())
	var got map[string]any
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || got["id"] != convA.String() || got["contact_name"] != "Alice" || got["assigned_to_user_id"] != assignee.String() {
		t.Fatalf("own conversation: %d %s", code, body)
	}
	// Another tenant's conversation id, and an unknown id, are indistinguishable.
	c1, b1 := get(userA, tenantA, convB.String())
	c2, b2 := get(userA, tenantA, uuid.NewString())
	if c1 != 404 || c2 != 404 || b1 != b2 {
		t.Fatalf("enumeration oracle: %d %q vs %d %q", c1, b1, c2, b2)
	}
	if c, _ := get(userA, tenantB, convB.String()); c != 404 {
		t.Fatalf("tenant B path without membership: %d", c)
	}
	if c, _ := get(userA, tenantA, "not-a-uuid"); c != 400 {
		t.Fatalf("invalid id: %d", c)
	}
}
