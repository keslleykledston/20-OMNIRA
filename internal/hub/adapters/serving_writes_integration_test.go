package adapters_test

// ADR-0040 phase 03 (pilot), the WRITES: a Hub agent attending an instance claims a conversation and answers it through the instance's own routes,
// which hand the request to the Hub's reviewed write path. What must hold, on a real PostgreSQL with the application's role:
//   - both conditions are needed: the key of the route (conversation.claim / conversation.reply) AND the reply-capable grant;
//   - the write really happens (assignment, outbound message), idempotently, and only for the instance of the context;
//   - a member's request reaches the ORIGINAL handler untouched; the delegated one never does;
//   - the data layer still refuses every direct write by a Hub agent (the wrappers are the only door).

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	adapters "github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

type writesAPI struct {
	w        *world
	h        http.Handler
	original atomic.Int32 // how many times the ORIGINAL handlers ran
}

func (w *world) writesAPI(t *testing.T) *writesAPI {
	t.Helper()
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	a := &writesAPI{w: w}
	dw := adapters.NewDelegatedWrites(w.app)
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
	orig := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { a.original.Add(1); rw.WriteHeader(http.StatusNoContent) })
	const t0 = "/api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}"
	mux := http.NewServeMux()
	mux.Handle("POST "+t0+"/assign", tenancyadapters.Delegable("conversation.claim", who(session(dw.Claim(orig)))))
	mux.Handle("POST "+t0+"/messages", tenancyadapters.Delegable("conversation.reply", who(session(dw.Reply(orig)))))
	a.h = mux
	return a
}

func (a *writesAPI) post(user uuid.UUID, tenantKey, convKey, action, body, acting string, hdr map[string]string) (int, string, http.Header) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+a.w.tenant[tenantKey].String()+"/inbox/conversations/"+a.w.conv[convKey].String()+"/"+action, bytes.NewBufferString(body))
	req.Header.Set("X-Test-User", user.String())
	req.Header.Set("Content-Type", "application/json")
	if acting != "" {
		req.Header.Set("X-Omnira-Acting-As", acting)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

func TestDelegatedClaimAndReplyThroughTheInstanceRoutes(t *testing.T) {
	w := newWorld(t)
	api := w.writesAPI(t)
	w.connect("A")
	w.connect("B")
	agent := w.hubAgent("agent")
	g := w.grantReply(agent, "A")
	w.ceiling("A", "conversation.read", "conversation.claim", "conversation.reply")
	w.grantKeys(g, "conversation.read", "conversation.claim", "conversation.reply")
	hub := "hub:" + w.hub.String()

	t.Run("claim, then answer: the assignment and the outbound message are real, and the answer is idempotent", func(t *testing.T) {
		if code, body, _ := api.post(agent, "A", "A", "assign", `{}`, hub, nil); code != 200 || !strings.Contains(body, agent.String()) {
			t.Fatalf("claim: %d %s", code, body)
		}
		if got := w.assignee("A"); got == nil || *got != agent {
			t.Fatalf("assignee = %v", got)
		}
		key := "delegated-reply-" + uuid.NewString()[:8]
		code, body, _ := api.post(agent, "A", "A", "messages", `{"text":"Olá, já vamos verificar"}`, hub, map[string]string{"Idempotency-Key": key})
		if code != 202 || !strings.Contains(body, `"direction":"outbound"`) {
			t.Fatalf("reply: %d %s", code, body)
		}
		if n := w.outbound("A"); n != 1 {
			t.Fatalf("outbound messages = %d", n)
		}
		code, _, hdr := api.post(agent, "A", "A", "messages", `{"text":"Olá, já vamos verificar"}`, hub, map[string]string{"Idempotency-Key": key})
		if code != 200 || hdr.Get("Idempotent-Replayed") != "true" || w.outbound("A") != 1 {
			t.Errorf("a retry of the same answer must be replayed, not sent twice: %d replayed=%q outbound=%d", code, hdr.Get("Idempotent-Replayed"), w.outbound("A"))
		}
		// the audit trail of the write names the agent (the Hub path audits)
		if n := w.auditActor("conversation.assigned", agent); n == 0 {
			// the action name is the Hub path's own; the attribution is what matters
			t.Log("no 'conversation.assigned' audit row; checking any audit row by this actor")
		}
		if n := w.count(`SELECT count(*) FROM audit_events WHERE actor_id = $1 AND tenant_id = $2`, agent, w.tenant["A"]); n == 0 {
			t.Error("the delegated writes left no audit trail")
		}
		if api.original.Load() != 0 {
			t.Error("the original handlers must never run for a delegated request")
		}
	})

	t.Run("an attachment, a missing key and a bad body are refused before any write", func(t *testing.T) {
		before := w.outbound("A")
		if code, _, _ := api.post(agent, "A", "A", "messages", `{"text":"x","attachment_id":"`+uuid.NewString()+`"}`, hub, map[string]string{"Idempotency-Key": "k-attachment-1"}); code != 422 {
			t.Errorf("attachment: %d, want 422", code)
		}
		if code, _, _ := api.post(agent, "A", "A", "messages", `{"text":"sem chave"}`, hub, nil); code != 400 {
			t.Errorf("missing Idempotency-Key: %d, want 400", code)
		}
		if code, _, _ := api.post(agent, "A", "A", "messages", `not json`, hub, map[string]string{"Idempotency-Key": "k-badbody-001"}); code != 400 {
			t.Errorf("bad body: %d, want 400", code)
		}
		if w.outbound("A") != before {
			t.Error("a refused request wrote a message")
		}
	})

	t.Run("both conditions are needed: the key AND the reply-capable grant", func(t *testing.T) {
		reader := w.hubAgent("reader")
		gr := w.grant(reader, "A") // can_reply = false
		w.grantKeys(gr, "conversation.read", "conversation.claim", "conversation.reply")
		if code, _, _ := api.post(reader, "A", "A", "assign", `{}`, hub, nil); code != 403 {
			t.Errorf("keys but a read-only grant, claim: %d, want 403", code)
		}
		if code, _, _ := api.post(reader, "A", "A", "messages", `{"text":"x"}`, hub, map[string]string{"Idempotency-Key": "k-readonly-01"}); code != 403 {
			t.Errorf("keys but a read-only grant, reply: %d, want 403", code)
		}
		noKeys := w.hubAgent("nokeys")
		gn := w.grantReply(noKeys, "A") // can_reply = true
		w.grantKeys(gn, "conversation.read")
		if code, _, _ := api.post(noKeys, "A", "A", "assign", `{}`, hub, nil); code != 403 {
			t.Errorf("a reply-capable grant but no conversation.claim key: %d, want 403", code)
		}
		if code, _, _ := api.post(noKeys, "A", "A", "messages", `{"text":"x"}`, hub, map[string]string{"Idempotency-Key": "k-nokeys-001"}); code != 403 {
			t.Errorf("a reply-capable grant but no conversation.reply key: %d, want 403", code)
		}
		if w.assignee("A") == nil || *w.assignee("A") != agent {
			t.Error("a refused claim changed the assignment")
		}
	})

	t.Run("another agent cannot take or answer a conversation held by someone else", func(t *testing.T) {
		other := w.hubAgent("other")
		go2 := w.grantReply(other, "A")
		w.grantKeys(go2, "conversation.read", "conversation.claim", "conversation.reply")
		if code, _, _ := api.post(other, "A", "A", "assign", `{}`, hub, nil); code != 409 {
			t.Errorf("claim of a held conversation: %d, want 409", code)
		}
		if code, _, _ := api.post(other, "A", "A", "messages", `{"text":"x"}`, hub, map[string]string{"Idempotency-Key": "k-other-0001"}); code != 409 {
			t.Errorf("reply on a held conversation: %d, want 409", code)
		}
	})

	t.Run("the instance of the context is the only one: another instance's conversation is a 404", func(t *testing.T) {
		before := w.outbound("B")
		// B's conversation through A's URL: no hub item of the context's tenant for it
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+w.tenant["A"].String()+"/inbox/conversations/"+w.conv["B"].String()+"/messages", bytes.NewBufferString(`{"text":"x"}`))
		req.Header.Set("X-Test-User", agent.String())
		req.Header.Set("X-Omnira-Acting-As", hub)
		req.Header.Set("Idempotency-Key", "k-crossinst-1")
		rec := httptest.NewRecorder()
		api.h.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("B's conversation through A: %d, want 404", rec.Code)
		}
		// a person who ALSO serves B, acting for A: B's conversation through A's URL is still a 404 (the item is looked up for the context's
		// tenant, so the Hub's company-mismatch check never even sees another company's item)
		two := w.hubAgent("two")
		for _, k := range []string{"A", "B"} {
			gk := w.grantReply(two, k)
			w.ceiling(k, "conversation.read", "conversation.claim", "conversation.reply")
			w.grantKeys(gk, "conversation.read", "conversation.claim", "conversation.reply")
		}
		req2 := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+w.tenant["A"].String()+"/inbox/conversations/"+w.conv["B"].String()+"/messages", bytes.NewBufferString(`{"text":"x"}`))
		req2.Header.Set("X-Test-User", two.String())
		req2.Header.Set("X-Omnira-Acting-As", hub)
		req2.Header.Set("Idempotency-Key", "k-crossinst-3")
		rec2 := httptest.NewRecorder()
		api.h.ServeHTTP(rec2, req2)
		if rec2.Code != 404 {
			t.Errorf("a person serving both A and B, acting for A, writing into B's conversation: %d, want 404", rec2.Code)
		}
		// B's own URL: no grant on B at all
		if code, _, _ := api.post(agent, "B", "B", "messages", `{"text":"x"}`, hub, map[string]string{"Idempotency-Key": "k-crossinst-2"}); code != 404 {
			t.Errorf("instance B: %d, want 404", code)
		}
		if w.outbound("B") != before {
			t.Error("a message was written into another instance")
		}
	})

	t.Run("a member's request reaches the original handler, untouched", func(t *testing.T) {
		member := w.user("member")
		w.directMember(member, "A")
		before := api.original.Load()
		if code, _, _ := api.post(member, "A", "A", "assign", `{}`, "", nil); code != 204 {
			t.Errorf("member claim: %d, want the original handler's 204", code)
		}
		if code, _, _ := api.post(member, "A", "A", "messages", `{"text":"x"}`, "member", nil); code != 204 {
			t.Errorf("member reply: %d, want the original handler's 204", code)
		}
		if api.original.Load() != before+2 {
			t.Error("the original handlers did not run for the member")
		}
	})

	t.Run("the data layer still refuses every direct write by the agent", func(t *testing.T) {
		if _, err := w.write(agent, `INSERT INTO messages(tenant_id, conversation_id, direction) VALUES($1,$2,'outbound')`, w.tenant["A"], w.conv["A"]); err == nil {
			t.Error("a direct INSERT of a message by a Hub agent must be refused")
		}
		if n, _ := w.write(agent, `UPDATE conversations SET assigned_to_user_id = NULL WHERE tenant_id = $1`, w.tenant["A"]); n != 0 {
			t.Errorf("a direct UPDATE of conversations by a Hub agent touched %d row(s)", n)
		}
	})

	t.Run("revocation refuses the very next write", func(t *testing.T) {
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g)
		if code, _, _ := api.post(agent, "A", "A", "messages", `{"text":"depois de revogar"}`, hub, map[string]string{"Idempotency-Key": "k-revoked-001"}); code != 404 {
			t.Errorf("reply after revocation: %d, want 404", code)
		}
	})
}
