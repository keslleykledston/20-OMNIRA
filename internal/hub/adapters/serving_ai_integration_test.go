package adapters_test

// ADR-0040 phase 05, AI SUMMARY: a Hub agent may ask for the AI summary of a conversation they can see, only with the key ai.assist (optional per grant, in
// no default preset). On a real PostgreSQL with the application's role and the REAL handler (a fake text generator stands for the provider):
//   - the key is required by the route (403), and the contract's ceiling must carry it too;
//   - what leaves for the provider is only the text of THE conversation asked for, and only when it is visible to the person (queue scope, instance);
//   - both audit events (before and after the provider call) name the agent and the Hub; a conversation they cannot see never reaches the provider.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	aiadapters "github.com/omnira/omnira/internal/ai/adapters"
	aiports "github.com/omnira/omnira/internal/ai/ports"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/hub/provisioning"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

type fakeProvider struct {
	mu     sync.Mutex
	inputs []string
}

func (f *fakeProvider) Generate(_ context.Context, req aiports.GenerateRequest) (aiports.GenerateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, req.Input)
	return aiports.GenerateResponse{OutputText: "Cliente sem internet desde ontem."}, nil
}

func (f *fakeProvider) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inputs)
}

func TestDelegatedAISummaryNeedsTheKeyAndOnlyReadsWhatTheAgentSees(t *testing.T) {
	w := newWorld(t)
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	provider := &fakeProvider{}
	h := aiadapters.NewSummaryHandler(w.app, provider, auditadapters.NewPostgresAuditEventRepository(w.app), 300)
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
	mux.Handle("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ai/summary", tenancyadapters.Delegable("ai.assist", who(session(http.HandlerFunc(h.Summarize)))))
	post := func(user uuid.UUID, tenantKey, convKey, acting string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/tenants/"+w.tenant[tenantKey].String()+"/conversations/"+w.conv[convKey].String()+"/ai/summary", strings.NewReader("{}"))
		req.Header.Set("X-Test-User", user.String())
		if acting != "" {
			req.Header.Set("X-Omnira-Acting-As", acting)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	for _, k := range []string{"A", "A2", "B"} {
		w.exec(`UPDATE messages SET body = $2 WHERE conversation_id = $1`, w.conv[k], "texto da conversa "+k)
	}
	hub := "hub:" + w.hub.String()
	withAI := append(append([]string(nil), provisioning.ServingPresets["completo"]...), "ai.assist")
	w.ceiling("A", withAI...)
	w.ceiling("B", withAI...)
	agent := w.hubAgent("agent")
	g := w.grantReply(agent, "A")

	t.Run("without the key the route refuses and nothing reaches the provider", func(t *testing.T) {
		w.grantKeys(g, provisioning.ServingPresets["completo"]...)
		if code, _ := post(agent, "A", "A", hub); code != 403 {
			t.Errorf("without ai.assist: %d, want 403", code)
		}
		if provider.calls() != 0 {
			t.Errorf("the provider was called %d times", provider.calls())
		}
	})

	t.Run("ai.assist alone never sends text the agent cannot read: without conversation.read the handler refuses before anything is read or sent", func(t *testing.T) {
		w.grantKeys(g, "ai.assist", "contact.read")
		before := provider.calls()
		if code, _ := post(agent, "A", "A", hub); code != 403 {
			t.Errorf("ai.assist without conversation.read: %d, want 403", code)
		}
		if provider.calls() != before {
			t.Errorf("the provider was called")
		}
		w.grantKeys(g, withAI...)
	})

	t.Run("a key in the grant is not enough when the contract's ceiling does not carry it", func(t *testing.T) {
		w.grantKeys(g, withAI...)
		w.ceiling("A", provisioning.ServingPresets["completo"]...)
		if code, _ := post(agent, "A", "A", hub); code != 403 {
			t.Errorf("ceiling without ai.assist: %d, want 403", code)
		}
		w.ceiling("A", withAI...)
		if provider.calls() != 0 {
			t.Errorf("the provider was called %d times", provider.calls())
		}
	})

	t.Run("with the key and the ceiling the summary comes back, built only from that conversation, and both audit events name the agent and the hub", func(t *testing.T) {
		w.grantKeys(g, withAI...)
		code, body := post(agent, "A", "A", hub)
		if code != 200 || !strings.Contains(body, "Cliente sem internet") {
			t.Fatalf("summary: %d %s", code, body)
		}
		if provider.calls() != 1 {
			t.Fatalf("provider calls = %d, want 1", provider.calls())
		}
		in := provider.inputs[0]
		if !strings.Contains(in, "texto da conversa A") || strings.Contains(in, "conversa A2") || strings.Contains(in, "conversa B") {
			t.Errorf("the provider received %q, want only the text of the conversation asked for", in)
		}
		n := w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = 'ai.conversation.summarize' AND metadata->>'acting_as' = $3`, w.tenant["A"], agent, hub)
		if n != 2 {
			t.Errorf("audit events naming the agent and the hub = %d, want 2 (before and after the provider call)", n)
		}
	})

	t.Run("a conversation outside the contract's queues or in another instance never reaches the provider", func(t *testing.T) {
		before := provider.calls()
		w.queueScope("A", w.queue1["A"])
		t.Cleanup(func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb WHERE id = $1`, w.contract["A"])
		})
		if code, _ := post(agent, "A", "A2", hub); code != 404 {
			t.Errorf("a conversation outside the queues: %d, want 404", code)
		}
		req := httptest.NewRequest("POST", "/api/v1/tenants/"+w.tenant["A"].String()+"/conversations/"+w.conv["B"].String()+"/ai/summary", strings.NewReader("{}"))
		req.Header.Set("X-Test-User", agent.String())
		req.Header.Set("X-Omnira-Acting-As", hub)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("another instance's conversation under this URL: %d, want 404", rec.Code)
		}
		if code, _ := post(agent, "B", "B", hub); code != 404 {
			t.Errorf("an instance without a grant: %d, want 404", code)
		}
		if provider.calls() != before {
			t.Errorf("the provider received %d extra calls", provider.calls()-before)
		}
	})

	t.Run("a member keeps the route exactly as before (no header, no key)", func(t *testing.T) {
		member := w.user("member")
		w.directMember(member, "A")
		if code, _ := post(member, "A", "A", ""); code != 200 {
			t.Errorf("member summary: %d, want 200", code)
		}
	})
}
