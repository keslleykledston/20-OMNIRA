package adapters_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

// ADR-0020: finalized attendances leave the default view (the work queue) and come back with status=closed / status=all.
func TestInboxListHidesFinalizedConversationsByDefault(t *testing.T) {
	e := newKindEnv(t)
	a := e.tenant()
	admin := e.member(a, "tenant_admin")
	conn := e.connection(a)
	svc := e.service(svcOpts{})
	openConv := e.ingest(svc, conn, "+5592999990401", "Aberta", "").Conversation.ID
	closedConv := e.ingest(svc, conn, "+5592999990402", "Finalizada", "").Conversation.ID
	e.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, closedConv)

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(e.app), tenancyadapters.NewPostgresTenantRepository(e.app))
	h := inboxadapters.NewInboxAPIHandler(e.app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations", tenancyadapters.AuthorizationMiddleware(e.app, authz)(http.HandlerFunc(h.ListConversations)))
	list := func(query string) (int, map[string]string) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+a.String()+"/inbox/conversations"+query, nil)
		req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: admin, Subject: admin.String()}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		out := map[string]string{}
		if rec.Code == 200 {
			var page struct{ Items []map[string]any }
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			for _, it := range page.Items {
				out[it["id"].(string)], _ = it["status"].(string)
			}
		}
		return rec.Code, out
	}
	if _, items := list(""); len(items) != 1 || items[openConv.String()] == "" {
		t.Fatalf("the default list is the work queue: only open attendances: %v", items)
	}
	if _, items := list("?status=open"); len(items) != 1 || items[openConv.String()] == "" {
		t.Fatalf("status=open: %v", items)
	}
	if _, items := list("?status=closed"); len(items) != 1 || items[closedConv.String()] != "closed" {
		t.Fatalf("status=closed shows the finalized ones: %v", items)
	}
	if _, items := list("?status=all"); len(items) != 2 {
		t.Fatalf("status=all shows both: %v", items)
	}
	if code, _ := list("?status=bogus"); code != http.StatusBadRequest {
		t.Fatalf("an unknown status is refused: %d", code)
	}
}
