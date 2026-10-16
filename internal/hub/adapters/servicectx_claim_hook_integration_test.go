package adapters_test

// ADR-0049 addendum on a real PostgreSQL: a Hub agent who assumes a conversation through the workspace's assign route gets its protocol when the company turned
// the hook on and the agent's grant holds service.create. Claiming never depends on it.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/platform/claimhook"
	servicectxadapters "github.com/omnira/omnira/internal/servicectx/adapters"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

// claimRoute serves the instance's assign route the way the API wires it: the Hub's write path for a delegated request, with the claim hook installed or not.
func (s *svcWorld) claimRoute(withHook bool) http.Handler {
	s.t.Helper()
	hook := &claimhook.Hook{}
	if withHook {
		svc, _, _ := servicectxadapters.Build(s.app, servicectxadapters.Options{})
		hook.Set(servicectxadapters.ClaimHook(s.app, svc))
	}
	dw := adapters.NewDelegatedWrites(s.app).WithClaimHook(hook)
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(s.app), tenancyadapters.NewPostgresTenantRepository(s.app))
	session := tenancyadapters.AuthorizationMiddleware(s.app, authz)
	orig := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(http.StatusNoContent) })
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/assign", tenancyadapters.Delegable("conversation.claim",
		http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if u, err := uuid.Parse(r.Header.Get("X-Test-User")); err == nil {
				r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: u}))
			}
			session(dw.Claim(orig)).ServeHTTP(rw, r)
		})))
	return mux
}

func (s *svcWorld) claim(h http.Handler, agent uuid.UUID) int {
	s.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+s.tenant["A"].String()+"/inbox/conversations/"+s.conv["A"].String()+"/assign", strings.NewReader(`{}`))
	req.Header.Set("X-Test-User", agent.String())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Omnira-Acting-As", "hub:"+s.hub.String())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// replyAgent is an agent that may attend the instance with exactly these keys (the contract's ceiling holds them all).
func (s *svcWorld) replyAgent(keys ...string) uuid.UUID {
	s.t.Helper()
	s.connect("A")
	agent := s.hubAgent("claim-agent-" + uuid.NewString()[:6])
	g := s.grantReply(agent, "A")
	s.ceiling("A", "conversation.read", "conversation.claim", "conversation.reply", "service.read", "service.create")
	s.grantKeys(g, keys...)
	return agent
}

func TestAHubAgentWhoAssumesAConversationGetsItsProtocol(t *testing.T) {
	s := newSvcWorld(t)
	agent := s.replyAgent("conversation.read", "conversation.claim", "conversation.reply", "service.read", "service.create")
	if code := s.claim(s.claimRoute(true), agent); code != http.StatusOK {
		t.Fatalf("claim: %d", code)
	}
	if got := s.assignee("A"); got == nil || *got != agent {
		t.Fatalf("the agent holds the conversation: %v", got)
	}
	var protocol, source string
	var openedBy uuid.UUID
	if err := s.owner.QueryRow(t.Context(), `SELECT protocol, opened_source, opened_by_user_id FROM service_sessions WHERE conversation_id = $1`, s.conv["A"]).Scan(&protocol, &source, &openedBy); err != nil {
		t.Fatalf("the assumed conversation has its protocol: %v", err)
	}
	if !strings.HasSuffix(protocol, "-1001") || source != "operator" || openedBy != agent {
		t.Fatalf("protocol %s source %s opened_by %s", protocol, source, openedBy)
	}
	if n := s.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = 'hub.service.session_opened_on_claim'`, s.tenant["A"], agent); n != 1 {
		t.Fatalf("the Hub path audits the protocol it opened for the agent: %d", n)
	}
	// claiming again is a no-op for the holder: still one protocol
	if code := s.claim(s.claimRoute(true), agent); code != http.StatusOK {
		t.Fatalf("repeat: %d", code)
	}
	if n := s.count(`SELECT count(*) FROM service_sessions WHERE conversation_id = $1`, s.conv["A"]); n != 1 {
		t.Fatalf("one protocol: %d", n)
	}
}

func TestAHubAgentWithoutServiceCreateOrWithoutTheHookJustClaims(t *testing.T) {
	s := newSvcWorld(t)
	agent := s.replyAgent("conversation.read", "conversation.claim", "conversation.reply", "service.read") // no service.create
	if code := s.claim(s.claimRoute(true), agent); code != http.StatusOK {
		t.Fatalf("claim: %d", code)
	}
	if got := s.assignee("A"); got == nil || *got != agent {
		t.Fatalf("the agent holds the conversation: %v", got)
	}
	if n := s.count(`SELECT count(*) FROM service_sessions WHERE conversation_id = $1`, s.conv["A"]); n != 0 {
		t.Fatalf("without the key there is no protocol: %d", n)
	}
	s2 := newSvcWorld(t)
	agent2 := s2.replyAgent("conversation.read", "conversation.claim", "conversation.reply", "service.read", "service.create")
	if code := s2.claim(s2.claimRoute(false), agent2); code != http.StatusOK {
		t.Fatalf("claim without the hook: %d", code)
	}
	if n := s2.count(`SELECT count(*) FROM service_sessions WHERE conversation_id = $1`, s2.conv["A"]); n != 0 {
		t.Fatalf("hook off: %d", n)
	}
}

// hubClaimRoute serves the Hub's OWN claim route (the one the aggregated "Todas" tab uses) the way the API wires it, with the hook installed or not.
func (s *svcWorld) hubClaimRoute(withHook bool) http.Handler {
	s.t.Helper()
	hook := &claimhook.Hook{}
	if withHook {
		svc, _, _ := servicectxadapters.Build(s.app, servicectxadapters.Options{})
		hook.Set(servicectxadapters.ClaimHook(s.app, svc))
	}
	h := adapters.NewHTTPHandler(s.app).WithClaimHook(hook)
	session := tenancyadapters.UserSessionMiddleware(s.app)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/hubs/{hub_id}/inbox/{item_id}/claim", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if u, err := uuid.Parse(r.Header.Get("X-Test-User")); err == nil {
			r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: u}))
		}
		session(http.HandlerFunc(h.ClaimItem)).ServeHTTP(rw, r)
	}))
	return mux
}

func (s *svcWorld) hubClaim(h http.Handler, agent uuid.UUID) int {
	s.t.Helper()
	body := `{"expected_tenant_id":"` + s.tenant["A"].String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hubs/"+s.hub.String()+"/inbox/"+s.itemID("A").String()+"/claim", strings.NewReader(body))
	req.Header.Set("X-Test-User", agent.String())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestTheHubsOwnClaimRouteOpensTheProtocolToo(t *testing.T) {
	s := newSvcWorld(t)
	agent := s.replyAgent("conversation.read", "conversation.claim", "conversation.reply", "service.read", "service.create")
	if code := s.hubClaim(s.hubClaimRoute(true), agent); code != http.StatusOK {
		t.Fatalf("claim: %d", code)
	}
	if got := s.assignee("A"); got == nil || *got != agent {
		t.Fatalf("the agent holds the conversation: %v", got)
	}
	var protocol string
	var openedBy uuid.UUID
	if err := s.owner.QueryRow(t.Context(), `SELECT protocol, opened_by_user_id FROM service_sessions WHERE conversation_id = $1`, s.conv["A"]).Scan(&protocol, &openedBy); err != nil {
		t.Fatalf("the conversation assumed through the Hub's own route has its protocol: %v", err)
	}
	if !strings.HasSuffix(protocol, "-1001") || openedBy != agent {
		t.Fatalf("protocol %s opened_by %s", protocol, openedBy)
	}
	if n := s.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = 'hub.service.session_opened_on_claim'`, s.tenant["A"], agent); n != 1 {
		t.Fatalf("audited: %d", n)
	}
	// repeating the claim is a no-op: still one protocol
	if code := s.hubClaim(s.hubClaimRoute(true), agent); code != http.StatusOK {
		t.Fatalf("repeat: %d", code)
	}
	if n := s.count(`SELECT count(*) FROM service_sessions WHERE conversation_id = $1`, s.conv["A"]); n != 1 {
		t.Fatalf("one protocol: %d", n)
	}
}

func TestTheHubsOwnClaimRouteWithoutTheKeyOrTheHookJustClaims(t *testing.T) {
	s := newSvcWorld(t)
	agent := s.replyAgent("conversation.read", "conversation.claim", "conversation.reply", "service.read") // the grant lacks service.create
	if code := s.hubClaim(s.hubClaimRoute(true), agent); code != http.StatusOK {
		t.Fatalf("claim: %d", code)
	}
	if got := s.assignee("A"); got == nil || *got != agent {
		t.Fatalf("the agent holds the conversation: %v", got)
	}
	if n := s.count(`SELECT count(*) FROM service_sessions WHERE conversation_id = $1`, s.conv["A"]); n != 0 {
		t.Fatalf("without the key there is no protocol: %d", n)
	}
	s2 := newSvcWorld(t)
	agent2 := s2.replyAgent("conversation.read", "conversation.claim", "conversation.reply", "service.read", "service.create")
	if code := s2.hubClaim(s2.hubClaimRoute(false), agent2); code != http.StatusOK {
		t.Fatalf("claim without the hook: %d", code)
	}
	if n := s2.count(`SELECT count(*) FROM service_sessions WHERE conversation_id = $1`, s2.conv["A"]); n != 0 {
		t.Fatalf("hook off: %d", n)
	}
}
