package adapters_test

// ADR-0040 phase 09 on a real PostgreSQL, with the application's own role and the real routes: a Hub agent searches the EARLIER MESSAGES of the contact of a conversation
// (history-search) with the key attendance.history. The search is a plain SELECT in the request's RLS session, so what it can find is exactly what the delegated policies already show
// that person: the key opens the ROUTE, the database decides the ROWS. What must hold:
//   - with the key, hits come from the other conversations of the same contact in the same instance, never from another instance;
//   - without the key, 403 and nothing is read;
//   - a person who holds the key but not conversation.read (or whose grant is scoped to other queues) finds nothing, whatever the route says;
//   - the member path is unchanged.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/hub/provisioning"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

func TestDelegatedHistorySearchThroughTheRealRoute(t *testing.T) {
	w := newWorld(t)
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	svc := attendanceapplication.NewService(attendanceadapters.NewPostgresRepository(w.app), attendanceadapters.NewAuthorizer(w.app),
		attendanceadapters.NewAuditor(auditadapters.NewPostgresAuditEventRepository(w.app)))
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))
	session := tenancyadapters.AuthorizationMiddleware(w.app, authz)
	who := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if u, err := uuid.Parse(r.Header.Get("X-Test-User")); err == nil {
				r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: u}))
			}
			next.ServeHTTP(rw, r)
		})
	}
	mux := http.NewServeMux()
	handler := attendanceadapters.NewHandler(svc).WithDelegatedSuggest(func(ctx context.Context, tenantID, userID uuid.UUID) (bool, error) {
		return tenancyadapters.ActorHasPermission(ctx, platformdb.QuerierFromContext(ctx, w.app), tenantID, userID, "conversation.read")
	}, 5)
	handler.Routes(mux, func(fn http.HandlerFunc) http.Handler { return who(session(fn)) })
	hub := "hub:" + w.hub.String()
	search := func(tenantKey, convKey, q string, user uuid.UUID, acting string) (int, string) {
		req := httptest.NewRequest("GET", "/api/v1/tenants/"+w.tenant[tenantKey].String()+"/inbox/conversations/"+w.conv[convKey].String()+"/history-search?q="+url.QueryEscape(q), nil)
		req.Header.Set("X-Test-User", user.String())
		if acting != "" {
			req.Header.Set("X-Omnira-Acting-As", acting)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	base := provisioning.ServingPresets["completo"]
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }
	w.ceiling("A", with("attendance.history")...)
	w.ceiling("B", with("attendance.history")...)
	reader := w.colleague("reader", "A", with("attendance.history")...)
	blind := w.colleague("blind", "A", base...)
	histOnly := w.colleague("hist-only", "A", "attendance.history") // the key of the memory and nothing that reads a conversation
	member := w.user("member")
	w.directMember(member, "A")
	w.grantKeys(w.grantReply(reader, "B"), with("attendance.history")...) // the same person also serves B: the pin to one instance must still hold

	contactA, contactB := w.contactOf("A"), w.contactOf("B")
	say := func(tenantKey, convKey string, _ uuid.UUID, direction, body string) {
		w.exec(`INSERT INTO messages (tenant_id, conversation_id, direction, message_type, body) VALUES ($1,$2,$3,'text',$4)`, w.tenant[tenantKey], w.conv[convKey], direction, body)
	}
	say("A", "A", contactA, "inbound", "bom dia, preciso falar da fatura")
	say("A", "A2", contactA, "inbound", "meu boleto de março não chegou")
	say("A", "A0", contactA, "outbound", "reenviei o boleto de abril por e-mail")
	say("B", "B", contactB, "inbound", "boleto confidencial da outra empresa")

	t.Run("with the key the agent finds the earlier messages of the contact, in this instance only, and never the conversation they are looking at", func(t *testing.T) {
		code, body := search("A", "A", "boleto", reader, hub)
		if code != 200 || !strings.Contains(body, "boleto de março") || !strings.Contains(body, "boleto de abril") {
			t.Fatalf("search: %d %s", code, body)
		}
		if strings.Contains(body, "outra empresa") || strings.Contains(body, "confidencial") {
			t.Errorf("another instance's message leaked: %s", body)
		}
		if strings.Contains(body, "falar da fatura") {
			t.Errorf("the conversation being looked at must be left out: %s", body)
		}
		// the same person serving B finds B's own, and not A's
		code, body = search("B", "B", "boleto", reader, hub)
		if code != 200 || strings.Contains(body, "março") || strings.Contains(body, "abril") {
			t.Errorf("serving B: %d %s", code, body)
		}
		// a conversation of another instance under this instance's URL is not there
		if code, _ := search("A", "B", "boleto", reader, hub); code != 404 && code != 400 {
			t.Errorf("another instance's conversation under this URL: %d, want 404", code)
		}
		if code, _ := search("A", "A", "b", reader, hub); code != 400 {
			t.Errorf("a one-character query: %d, want 400", code)
		}
	})

	t.Run("without the key the route refuses", func(t *testing.T) {
		if code, body := search("A", "A", "boleto", blind, hub); code != 403 || strings.Contains(body, "março") {
			t.Errorf("without attendance.history: %d %s, want 403 and no text", code, body)
		}
	})

	t.Run("the key alone reads nothing: the route also asks for conversation.read", func(t *testing.T) {
		code, body := search("A", "A", "boleto", histOnly, hub)
		if code != 403 || strings.Contains(body, "março") || strings.Contains(body, "abril") {
			t.Fatalf("a person without conversation.read: %d %s, want 403 and no text", code, body)
		}
	})

	t.Run("a grant scoped to other queues finds nothing of the conversations out of its scope", func(t *testing.T) {
		w.queueScope("A", w.queue1["A"]) // only queue 1: the conversation being looked at; A2 (queue 2) is outside
		t.Cleanup(func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb WHERE id = $1`, w.contract["A"])
		})
		code, body := search("A", "A", "boleto", reader, hub)
		if code != 200 {
			t.Fatalf("search: %d %s", code, body)
		}
		if strings.Contains(body, "março") {
			t.Errorf("a message of a conversation outside the scope of the grant was found: %s", body)
		}
	})

	t.Run("the member path is unchanged", func(t *testing.T) {
		code, body := search("A", "A", "boleto", member, "")
		if code != 200 || !strings.Contains(body, "boleto de março") || !strings.Contains(body, "boleto de abril") || strings.Contains(body, "outra empresa") {
			t.Errorf("member search: %d %s", code, body)
		}
	})
}
