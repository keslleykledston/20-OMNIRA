package adapters_test

// ADR-0040 phase 05b: a Hub agent ENDS the attendance of a conversation they hold (key conversation.finalize), through the instance's own finalize route, run by
// the Hub's write path. On a real PostgreSQL with the application's role and the REAL attendance routes and service. What must hold:
//   - the key AND the reply-capable grant are needed; only the HOLDER finalizes (a delegate has no supervisor path); another instance, a conversation outside the
//     contract's queues and a revoked grant are 404; the body can carry nothing that overrides the conversation;
//   - the effects are the member's: conversation closed, local tickets closed, ERP-linked ones left and counted, closure and follow-ups recorded, repeated call
//     idempotent; follow-up owners must be members of the instance;
//   - one audit row names the agent and the hub, with ids, counts and enums but never the text; the database still refuses every direct write by the agent.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	hubadapters "github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/hub/provisioning"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

func TestDelegatedFinalizeThroughTheRealAttendanceRoutes(t *testing.T) {
	w := newWorld(t)
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	svc := attendanceapplication.NewService(attendanceadapters.NewPostgresRepository(w.app), attendanceadapters.NewAuthorizer(w.app),
		attendanceadapters.NewAuditor(auditadapters.NewPostgresAuditEventRepository(w.app)))
	handler := attendanceadapters.NewHandler(svc).WithDelegatedFinalize(hubadapters.NewDelegatedWrites(w.app).WithAttendance(svc).Finalize)
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
	handler.Routes(mux, func(fn http.HandlerFunc) http.Handler { return who(session(fn)) })
	post := func(user uuid.UUID, tenantKey, convKey, body, acting string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/tenants/"+w.tenant[tenantKey].String()+"/inbox/conversations/"+w.conv[convKey].String()+"/finalize", strings.NewReader(body))
		req.Header.Set("X-Test-User", user.String())
		req.Header.Set("Content-Type", "application/json")
		if acting != "" {
			req.Header.Set("X-Omnira-Acting-As", acting)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	hub := "hub:" + w.hub.String()
	withFin := provisioning.ServingPresets["completo_fin"]
	w.ceiling("A", withFin...)
	w.ceiling("B", withFin...)
	agent := w.hubAgent("agent")
	g := w.grantReply(agent, "A")
	w.grantKeys(g, withFin...)
	other := w.hubAgent("other")
	w.grantKeys(w.grantReply(other, "A"), withFin...)
	member := w.user("member")
	w.directMember(member, "A")
	hold := func(convKey string, user *uuid.UUID) {
		w.exec(`UPDATE conversations SET assigned_to_user_id = $2, status = 'open', closed_at = NULL WHERE id = $1`, w.conv[convKey], user)
	}
	status := func(convKey string) string {
		return w.ownerOne(`SELECT status FROM conversations WHERE id = $1`, w.conv[convKey])
	}
	body := `{"reason":"resolved","summary":"Cliente voltou a navegar.","note":"nota interna","follow_ups":[{"kind":"pending","text":"ligar amanhã"}]}`

	// a local ticket (closed by the finalization); the ERP-linked one (left alone and counted) is on another conversation, one active ticket per conversation
	w.exec(`INSERT INTO tickets (tenant_id, conversation_id, status, priority) VALUES ($1, $2, 'open', 'low')`, w.tenant["A"], w.conv["A"])
	w.exec(`INSERT INTO tickets (tenant_id, conversation_id, status, priority, external_ticket_id) VALUES ($1, $2, 'in_progress', 'low', '28180')`, w.tenant["A"], w.conv["A0"])

	t.Run("without the key the route refuses and nothing changes", func(t *testing.T) {
		hold("A", &agent)
		w.grantKeys(g, provisioning.ServingPresets["completo"]...)
		if code, _ := post(agent, "A", "A", body, hub); code != 403 {
			t.Errorf("without conversation.finalize: %d, want 403", code)
		}
		w.grantKeys(g, withFin...)
		if status("A") != "open" {
			t.Error("the conversation was closed")
		}
	})

	t.Run("only the holder finalizes: not a colleague, not an unassigned conversation", func(t *testing.T) {
		hold("A", &other)
		if code, _ := post(agent, "A", "A", body, hub); code != 403 {
			t.Errorf("finalize by someone who does not hold it: %d, want 403", code)
		}
		hold("A", nil)
		if code, _ := post(agent, "A", "A", body, hub); code != 403 {
			t.Errorf("finalize of an unassigned conversation: %d, want 403 (no supervisor path for a delegate)", code)
		}
		if status("A") != "open" {
			t.Error("the conversation was closed")
		}
	})

	t.Run("the walls: unknown fields, a bad reason, a follow-up owned by a non-member", func(t *testing.T) {
		hold("A", &agent)
		for name, b := range map[string]string{
			"a tenant smuggled in the body":      `{"reason":"resolved","tenant_id":"` + w.tenant["B"].String() + `"}`,
			"an unknown reason":                  `{"reason":"because"}`,
			"a follow-up owned by the hub agent": `{"reason":"resolved","follow_ups":[{"kind":"pending","text":"x","owner_user_id":"` + agent.String() + `"}]}`,
		} {
			if code, _ := post(agent, "A", "A", b, hub); code != 400 {
				t.Errorf("%s: %d, want 400", name, code)
			}
		}
		if status("A") != "open" {
			t.Error("a refused request closed the conversation")
		}
	})

	t.Run("the holder finalizes: the member's effects, one audit row naming the agent and the hub, never the text", func(t *testing.T) {
		hold("A", &agent)
		withOwner := strings.Replace(body, `"text":"ligar amanhã"`, `"text":"ligar amanhã","owner_user_id":"`+member.String()+`"`, 1)
		code, resp := post(agent, "A", "A", withOwner, hub)
		if code != 200 || !strings.Contains(resp, `"changed":true`) {
			t.Fatalf("finalize: %d %s", code, resp)
		}
		if status("A") != "closed" {
			t.Error("the conversation is not closed")
		}
		if got := w.ownerOne(`SELECT closed_by_user_id::text || '|' || source || '|' || reason || '|' || local_tickets_closed || '|' || tickets_kept FROM conversation_closures WHERE conversation_id = $1`, w.conv["A"]); got != agent.String()+"|agent|resolved|1|0" {
			t.Errorf("closure = %q, want the agent, source agent, 1 local ticket closed", got)
		}
		if n := w.count(`SELECT count(*) FROM follow_up_items WHERE conversation_id = $1 AND owner_user_id = $2 AND created_by_user_id = $3`, w.conv["A"], member, agent); n != 1 {
			t.Errorf("follow-ups = %d, want 1", n)
		}
		if st := w.ownerOne(`SELECT status FROM tickets WHERE conversation_id = $1`, w.conv["A"]); st != "closed" {
			t.Errorf("local ticket = %q, want closed", st)
		}
		n := w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = 'hub.conversation.finalized' AND metadata->>'hub_id' = $3`, w.tenant["A"], agent, w.hub.String())
		if n != 1 {
			t.Errorf("audit rows = %d, want 1", n)
		}
		if n := w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND (metadata::text LIKE '%navegar%' OR metadata::text LIKE '%nota interna%' OR metadata::text LIKE '%ligar amanh%')`, w.tenant["A"]); n != 0 {
			t.Error("customer or attendance text reached the audit trail")
		}
	})

	t.Run("repeating it is idempotent: the same closure, no second row, no second audit", func(t *testing.T) {
		code, resp := post(agent, "A", "A", body, hub)
		if code != 200 || !strings.Contains(resp, `"changed":false`) {
			t.Fatalf("repeat: %d %s", code, resp)
		}
		if n := w.count(`SELECT count(*) FROM conversation_closures WHERE conversation_id = $1`, w.conv["A"]); n != 1 {
			t.Errorf("closures = %d, want 1", n)
		}
		if n := w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND action = 'hub.conversation.finalized'`, w.tenant["A"]); n != 1 {
			t.Errorf("audit rows = %d, want still 1", n)
		}
	})

	t.Run("outside the contract's queues, another instance, a revoked grant and a read-only grant", func(t *testing.T) {
		hold("A2", &agent)
		w.queueScope("A", w.queue1["A"])
		t.Cleanup(func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb WHERE id = $1`, w.contract["A"])
		})
		if code, _ := post(agent, "A", "A2", body, hub); code != 404 {
			t.Errorf("a conversation outside the queues: %d, want 404", code)
		}
		req := httptest.NewRequest("POST", "/api/v1/tenants/"+w.tenant["A"].String()+"/inbox/conversations/"+w.conv["B"].String()+"/finalize", strings.NewReader(body))
		req.Header.Set("X-Test-User", agent.String())
		req.Header.Set("X-Omnira-Acting-As", hub)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("another instance's conversation under this URL: %d, want 404", rec.Code)
		}
		w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb WHERE id = $1`, w.contract["A"])
		w.exec(`UPDATE effective_access_grants SET can_reply = false WHERE id = $1`, g)
		if code, _ := post(agent, "A", "A2", body, hub); code != 403 {
			t.Errorf("without the reply capability: %d, want 403", code)
		}
		w.exec(`UPDATE effective_access_grants SET can_reply = true WHERE id = $1`, g)
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g)
		if code, _ := post(agent, "A", "A2", body, hub); code != 404 {
			t.Errorf("a revoked grant: %d, want 404", code)
		}
		w.exec(`UPDATE effective_access_grants SET status = 'active' WHERE id = $1`, g)
		if status("A2") != "open" {
			t.Error("a refused request closed the conversation")
		}
	})

	t.Run("an ERP-linked ticket is left alone and counted", func(t *testing.T) {
		hold("A0", &agent)
		if code, resp := post(agent, "A", "A0", `{"reason":"resolved"}`, hub); code != 200 {
			t.Fatalf("finalize: %d %s", code, resp)
		}
		if got := w.ownerOne(`SELECT local_tickets_closed || '|' || tickets_kept FROM conversation_closures WHERE conversation_id = $1`, w.conv["A0"]); got != "0|1" {
			t.Errorf("closure counts = %q, want 0|1", got)
		}
		if st := w.ownerOne(`SELECT status FROM tickets WHERE conversation_id = $1`, w.conv["A0"]); st != "in_progress" {
			t.Errorf("ERP-linked ticket = %q, want untouched", st)
		}
	})

	t.Run("the database still refuses a direct write by the agent, and a member keeps the original route", func(t *testing.T) {
		hold("A2", &agent)
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			tag, err := q.Exec(ctx, `UPDATE conversations SET status = 'closed' WHERE id = $1`, w.conv["A2"])
			if err == nil && tag.RowsAffected() != 0 {
				t.Error("the delegate closed a conversation directly")
			}
		})
		if status("A2") != "open" {
			t.Error("the conversation was closed")
		}
		hold("A2", &member)
		if code, resp := post(member, "A", "A2", `{"reason":"resolved"}`, ""); code != 200 {
			t.Errorf("member finalize: %d %s", code, resp)
		}
	})
}
