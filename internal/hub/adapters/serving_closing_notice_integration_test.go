package adapters_test

// ADR-0040 phase 08: the closing notice for the customer, asked for in the finalize of a Hub agent, is queued by the SERVER inside the Hub's write transaction.
// Real PostgreSQL, the application's role, the REAL attendance routes and the production wiring of the notice. What must hold:
//   - with the key conversation.reply the message and its delivery job commit together with the closing; without the key the finalize still succeeds and says
//     "not_permitted" (the courtesy is never a precondition), and nothing is queued;
//   - a closed 24 h window refuses the courtesy, not the finalize; a replay never messages twice; the audit names the state, never the text;
//   - a failure after the notice was queued leaves neither the message nor the job; a text with a credential or too long is a 400 that changes nothing.

import (
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
	"github.com/omnira/omnira/internal/platform/httpserver"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

func TestTheDelegatedClosingNoticeIsQueuedWithTheClosingAndNeverWithoutIt(t *testing.T) {
	w := newWorld(t)
	w.connect("A")
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	svc := attendanceapplication.NewService(attendanceadapters.NewPostgresRepository(w.app), attendanceadapters.NewAuthorizer(w.app),
		attendanceadapters.NewAuditor(auditadapters.NewPostgresAuditEventRepository(w.app)))
	member, agentNotice := httpserver.ClosingNoticeSenders(w.app)
	svc.WithClosingNotice(member, agentNotice)
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
	hub := "hub:" + w.hub.String()
	post := func(user uuid.UUID, convKey, body string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/tenants/"+w.tenant["A"].String()+"/inbox/conversations/"+w.conv[convKey].String()+"/finalize", strings.NewReader(body))
		req.Header.Set("X-Test-User", user.String())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Omnira-Acting-As", hub)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	withFin := provisioning.ServingPresets["completo_fin"] // includes conversation.reply
	w.ceiling("A", withFin...)
	agent := w.hubAgent("agent")
	g := w.grantReply(agent, "A")
	w.grantKeys(g, withFin...)
	hold := func(convKey string) {
		w.exec(`UPDATE conversations SET assigned_to_user_id = $2, status = 'open', closed_at = NULL WHERE id = $1`, w.conv[convKey], agent)
	}
	notices := func(convKey string) int {
		return w.count(`SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'outbound'`, w.conv[convKey])
	}
	status := func(convKey string) string {
		return w.ownerOne(`SELECT status FROM conversations WHERE id = $1`, w.conv[convKey])
	}
	const text = "Atendimento encerrado, obrigado pelo contato!"
	body := func(farewell string) string {
		return `{"reason":"resolved","summary":"Cliente voltou a navegar.","farewell":"` + farewell + `"}`
	}

	t.Run("with the key the notice is queued with the closing: message, delivery job, audit state and no text", func(t *testing.T) {
		hold("A")
		code, resp := post(agent, "A", body(text))
		if code != 200 || !strings.Contains(resp, `"farewell":"queued"`) || !strings.Contains(resp, `"changed":true`) {
			t.Fatalf("finalize: %d %s", code, resp)
		}
		if status("A") != "closed" {
			t.Error("the conversation is not closed")
		}
		if n := w.count(`SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'outbound' AND body = $2 AND status = 'queued' AND sent_by_user_id = $3 AND idempotency_key = $4`,
			w.conv["A"], text, agent, "farewell-"+w.conv["A"].String()); n != 1 {
			t.Errorf("queued notices = %d, want 1 (the agent's own outbound text under the per-conversation key)", n)
		}
		if n := w.count(`SELECT count(*) FROM outbox_events o JOIN messages m ON m.id::text = o.aggregate_id WHERE m.conversation_id = $1 AND o.event_type = 'job.channel.send_text.v1'`, w.conv["A"]); n != 1 {
			t.Errorf("delivery jobs = %d, want 1", n)
		}
		if got := w.ownerOne(`SELECT metadata->>'farewell' FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = 'hub.conversation.finalized'`, w.tenant["A"], agent); got != "queued" {
			t.Errorf("audit farewell = %q, want queued", got)
		}
		if n := w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND metadata::text LIKE '%obrigado%'`, w.tenant["A"]); n != 0 {
			t.Error("the text of the notice reached the audit trail")
		}
	})

	t.Run("a replay never messages the customer twice", func(t *testing.T) {
		code, resp := post(agent, "A", body(text))
		if code != 200 || !strings.Contains(resp, `"changed":false`) {
			t.Fatalf("replay: %d %s", code, resp)
		}
		if n := notices("A"); n != 1 {
			t.Errorf("outbound messages after a replay = %d, want 1", n)
		}
	})

	t.Run("without the key conversation.reply the finalize still works and nothing is queued", func(t *testing.T) {
		hold("A2")
		w.grantKeys(g, "conversation.read", "conversation.claim", "conversation.finalize")
		defer w.grantKeys(g, withFin...)
		code, resp := post(agent, "A2", body(text))
		if code != 200 || !strings.Contains(resp, `"farewell":"not_permitted"`) {
			t.Fatalf("finalize without the reply key: %d %s", code, resp)
		}
		if status("A2") != "closed" || notices("A2") != 0 {
			t.Errorf("status=%s notices=%d, want closed and none queued", status("A2"), notices("A2"))
		}
	})

	t.Run("a closed 24 h window refuses the courtesy, not the finalize", func(t *testing.T) {
		hold("A0")
		w.exec(`UPDATE channel_connections SET provider = 'meta_cloud', provider_kind = 'official' WHERE tenant_id = $1`, w.tenant["A"])
		w.exec(`UPDATE messages SET created_at = now() - interval '3 days' WHERE conversation_id = $1 AND direction = 'inbound'`, w.conv["A0"]) // the customer wrote long ago
		defer w.exec(`UPDATE channel_connections SET provider = 'waha', provider_kind = 'unofficial' WHERE tenant_id = $1`, w.tenant["A"])
		code, resp := post(agent, "A0", body(text))
		if code != 200 || !strings.Contains(resp, `"farewell":"window_closed"`) {
			t.Fatalf("finalize with a closed window: %d %s", code, resp)
		}
		if status("A0") != "closed" || notices("A0") != 0 {
			t.Errorf("status=%s notices=%d, want closed and none queued", status("A0"), notices("A0"))
		}
	})

	t.Run("a key already used in the conversation by somebody else gets no second message and is not reported as queued", func(t *testing.T) {
		hold("A")
		w.exec(`DELETE FROM conversation_closures WHERE conversation_id = $1`, w.conv["A"])
		w.exec(`DELETE FROM messages WHERE conversation_id = $1`, w.conv["A"])
		someone := w.user("someone")
		w.directMember(someone, "A")
		w.exec(`INSERT INTO messages(id, tenant_id, conversation_id, direction, message_type, body, status, idempotency_key, request_hash, sent_by_user_id)
		        VALUES($1, $2, $3, 'outbound', 'text', 'outro texto', 'failed', $4, 'not-the-hash', $5)`, uuid.New(), w.tenant["A"], w.conv["A"], "farewell-"+w.conv["A"].String(), someone)
		code, resp := post(agent, "A", body(text))
		if code != 200 || !strings.Contains(resp, `"farewell":"unavailable"`) {
			t.Fatalf("finalize: %d %s", code, resp)
		}
		if n := notices("A"); n != 1 {
			t.Errorf("outbound messages = %d, want only the earlier one", n)
		}
	})

	t.Run("a failure after the notice was queued leaves neither the message nor its job", func(t *testing.T) {
		hold("A")
		w.exec(`DELETE FROM conversation_closures WHERE conversation_id = $1`, w.conv["A"])
		w.exec(`DELETE FROM messages WHERE conversation_id = $1`, w.conv["A"])
		w.exec(`CREATE OR REPLACE FUNCTION zz_boom() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$`)
		w.exec(`CREATE TRIGGER zz_boom BEFORE INSERT ON conversation_closures FOR EACH ROW EXECUTE FUNCTION zz_boom()`)
		defer func() {
			w.exec(`DROP TRIGGER IF EXISTS zz_boom ON conversation_closures`)
			w.exec(`DROP FUNCTION IF EXISTS zz_boom()`)
		}()
		before := w.count(`SELECT count(*) FROM outbox_events WHERE event_type = 'job.channel.send_text.v1'`)
		if code, _ := post(agent, "A", body(text)); code < 500 {
			t.Fatalf("the finalize was expected to fail with a 5xx, got %d", code)
		}
		if status("A") != "open" || notices("A") != 0 {
			t.Errorf("status=%s notices=%d after the failure, want open and none", status("A"), notices("A"))
		}
		if after := w.count(`SELECT count(*) FROM outbox_events WHERE event_type = 'job.channel.send_text.v1'`); after != before {
			t.Errorf("delivery jobs %d -> %d: a job outlived the failed finalize", before, after)
		}
	})

	t.Run("a credential or an over-long text is a 400 that changes nothing", func(t *testing.T) {
		hold("A")
		for name, b := range map[string]string{
			"a credential": body("use Bearer abcdefgh12345678 para entrar"),
			"too long":     body(strings.Repeat("a", 1001)),
		} {
			if code, _ := post(agent, "A", b); code != 400 {
				t.Errorf("%s: %d, want 400", name, code)
			}
		}
		if status("A") != "open" || notices("A") != 0 {
			t.Errorf("a refused notice changed something: status=%s notices=%d", status("A"), notices("A"))
		}
	})

}
