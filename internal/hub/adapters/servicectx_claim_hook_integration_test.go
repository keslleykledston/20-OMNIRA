package adapters_test

// ADR-0049 addendum on a real PostgreSQL: a Hub agent who assumes a conversation through the workspace's assign route gets its protocol when the company turned
// the hook on and the agent's grant holds service.create. Claiming never depends on it.

import (
	"fmt"
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
		if s.announce {
			svc.WithAnnouncer(servicectxadapters.NewNotice(s.app))
		}
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
		if s.announce {
			svc.WithAnnouncer(servicectxadapters.NewNotice(s.app))
		}
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

// ---- the customer is told about the protocol (ADR-0049 addendum) through the Hub too

// noticeAgent is a Hub agent who may reply and hold the module's keys: the grant AND the contract's ceiling hold them all.
func (s *svcWorld) noticeAgent(keys ...string) uuid.UUID {
	s.t.Helper()
	s.exec(`INSERT INTO tenant_entitlements(tenant_id, capability, enabled) VALUES($1,'service_protocol_notice',true) ON CONFLICT (tenant_id, capability) DO UPDATE SET enabled = true`, s.tenant["A"]) // the company's switch (starts off)
	s.connect("A")
	agent := s.hubAgent("notice-agent-" + uuid.NewString()[:6])
	g := s.grantReply(agent, "A")
	s.ceiling("A", keys...)
	s.grantKeys(g, keys...)
	return agent
}

func (s *svcWorld) told(convKey string) (out []string) {
	s.t.Helper()
	rows, err := s.owner.Query(s.ctx, `SELECT body FROM messages WHERE conversation_id = $1 AND direction = 'outbound' ORDER BY created_at, id`, s.conv[convKey])
	s.must(err)
	defer rows.Close()
	for rows.Next() {
		var b string
		s.must(rows.Scan(&b))
		out = append(out, b)
	}
	return out
}

var noticeKeys = []string{"conversation.read", "conversation.claim", "conversation.reply", "service.read", "service.create", "service.update", "service.close"}

func TestAHubAgentWhoAssumesAConversationTellsTheCustomerItsProtocol(t *testing.T) {
	for name, route := range map[string]func(*svcWorld) (http.Handler, func(http.Handler, uuid.UUID) int){
		"the instance's assign route": func(s *svcWorld) (http.Handler, func(http.Handler, uuid.UUID) int) {
			return s.claimRoute(true), s.claim
		},
		"the Hub's own claim route": func(s *svcWorld) (http.Handler, func(http.Handler, uuid.UUID) int) {
			return s.hubClaimRoute(true), s.hubClaim
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSvcWorld(t)
			s.announce = true
			agent := s.noticeAgent(noticeKeys...)
			h, do := route(s)
			if code := do(h, agent); code != http.StatusOK {
				t.Fatalf("claim: %d", code)
			}
			var protocol string
			s.must(s.owner.QueryRow(t.Context(), `SELECT protocol FROM service_sessions WHERE conversation_id = $1`, s.conv["A"]).Scan(&protocol))
			got := s.told("A")
			if len(got) != 1 || !strings.Contains(got[0], "Protocolo de atendimento: "+protocol) {
				t.Fatalf("the customer is told the protocol: %q", got)
			}
			var by uuid.UUID
			s.must(s.owner.QueryRow(t.Context(), `SELECT sent_by_user_id FROM messages WHERE conversation_id = $1 AND direction = 'outbound'`, s.conv["A"]).Scan(&by))
			if by != agent {
				t.Fatalf("the notice is attributed to the agent who assumed it: %s", by)
			}
		})
	}
}

func TestAHubAgentOpeningAndClosingAProtocolInTheDelegatedPaneTellsTheCustomer(t *testing.T) {
	s := newSvcWorld(t)
	s.svc.WithAnnouncer(servicectxadapters.NewNotice(s.app))
	agent := s.noticeAgent(noticeKeys...)
	s.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, s.conv["A"], agent) // the agent holds it
	r := s.call(agent, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s.conv["A"]))
	if r.code != http.StatusCreated {
		t.Fatalf("open: %d %v", r.code, r.body)
	}
	protocol, id := fmt.Sprint(r.body["protocol"]), r.body["id"].(string)
	if got := s.told("A"); len(got) != 1 || !strings.Contains(got[0], "Seu atendimento foi registrado") || !strings.Contains(got[0], protocol) {
		t.Fatalf("opening notice through the delegated session: %q", got)
	}
	version := fmt.Sprint(r.body["version"])
	if c := s.call(agent, "A", true, "POST", "/service-sessions/"+id+"/close", `{"resolution_code":"information_provided"}`, "If-Match", `"`+version+`"`); c.code != http.StatusOK {
		t.Fatalf("close: %d %v", c.code, c.body)
	}
	if got := s.told("A"); len(got) != 2 || !strings.Contains(got[1], "Seu atendimento foi encerrado") || !strings.Contains(got[1], protocol) {
		t.Fatalf("closing notice: %q", got)
	}
}

func TestAHubAgentWithoutTheReplyKeyOrNotHoldingTheConversationOpensButTellsNobody(t *testing.T) {
	s := newSvcWorld(t)
	s.svc.WithAnnouncer(servicectxadapters.NewNotice(s.app))
	noReply := s.noticeAgent("conversation.read", "service.read", "service.create") // no conversation.reply in the grant
	s.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, s.conv["A"], noReply)
	if r := s.call(noReply, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s.conv["A"])); r.code != http.StatusCreated {
		t.Fatalf("open without the reply key: %d %v", r.code, r.body)
	}
	if got := s.told("A"); len(got) != 0 {
		t.Fatalf("no reply key: nothing is sent in the agent's name: %q", got)
	}
	s2 := newSvcWorld(t)
	s2.svc.WithAnnouncer(servicectxadapters.NewNotice(s2.app))
	agent := s2.noticeAgent(noticeKeys...) // may reply, but the conversation is nobody's
	if r := s2.call(agent, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s2.conv["A"])); r.code != http.StatusCreated {
		t.Fatalf("open: %d %v", r.code, r.body)
	}
	if got := s2.told("A"); len(got) != 0 {
		t.Fatalf("not the holder: %q", got)
	}
}

// R-7 (Codex 2026-10-16): the notice of the delegated route is queued in its own transaction, so it must wait for the request's commit. A request that dies after
// opening the protocol rolls the protocol back; the customer must not hear about it.
func TestTheCustomerIsNotToldAboutAProtocolTheRequestRolledBack(t *testing.T) {
	s := newSvcWorld(t)
	s.svc.WithAnnouncer(servicectxadapters.NewNotice(s.app))
	agent := s.noticeAgent(noticeKeys...)
	s.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, s.conv["A"], agent)
	r := s.call(agent, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s.conv["A"]), "X-Test-Die", "1")
	if r.code != 599 {
		t.Fatalf("the request was meant to die after the handler: %d %v", r.code, r.body)
	}
	if n := s.count(`SELECT count(*) FROM service_sessions WHERE conversation_id = $1`, s.conv["A"]); n != 0 {
		t.Fatalf("the protocol survived a rolled-back request: %d", n)
	}
	if got := s.told("A"); len(got) != 0 {
		t.Fatalf("the customer was told about a protocol that does not exist: %q", got)
	}
	// the same request that commits does tell (the other test proves the wording)
	if r := s.call(agent, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s.conv["A"])); r.code != http.StatusCreated {
		t.Fatalf("open: %d %v", r.code, r.body)
	}
	if got := s.told("A"); len(got) != 1 {
		t.Fatalf("a committed request tells once: %q", got)
	}
}
