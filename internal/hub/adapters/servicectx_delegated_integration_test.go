package adapters_test

// ADR-0049 phase 5 on a real PostgreSQL, through the REAL middleware and the module's own routes: a Hub agent attending a company reaches Service Context &
// Operations only through a live grant (never as a member), with the keys the grant AND the contract's ceiling hold, inside the contract's queue scope, one
// instance at a time. Reading and writing follow the data domain `service`; asking the ERP for anything and changing the company's configuration are not delegable.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	servicectxadapters "github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/gatewaytest"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type svcWorld struct {
	*world
	gw  *gatewaytest.Fake
	mux http.Handler
}

func newSvcWorld(t *testing.T) *svcWorld {
	t.Helper()
	w := newWorld(t)
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	gw := gatewaytest.New()
	_, _, h := servicectxadapters.Build(w.app, servicectxadapters.Options{ExternalOrders: true, Gateways: gatewaytest.Resolver{F: gw}})
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))
	mw := tenancyadapters.AuthorizationMiddleware(w.app, authz)
	inner := http.NewServeMux()
	h.Routes(inner, func(fn http.HandlerFunc) http.Handler { return mw(fn) }, tenancyadapters.Delegable)
	sw := &svcWorld{world: w, gw: gw}
	sw.mux = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if u := r.Header.Get("X-Test-User"); u != "" {
			id, _ := uuid.Parse(u)
			r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: id}))
		}
		inner.ServeHTTP(rw, r)
	})
	// the contract of A reaches only queue 1: the delegate must never see the conversation of queue 2 (nor the one with no queue)
	w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = $2::jsonb WHERE id = $1`, w.contract["A"], fmt.Sprintf(`{"queue_ids":["%s"]}`, w.queue1["A"]))
	return sw
}

type svcReply struct {
	code int
	body map[string]any
}

func (s *svcWorld) call(user uuid.UUID, tenantKey string, acting bool, method, path, body string, headers ...string) svcReply {
	s.t.Helper()
	req := httptest.NewRequest(method, "/api/v1/tenants/"+s.tenant[tenantKey].String()+path, bytes.NewReader([]byte(body)))
	req.Header.Set("X-Test-User", user.String())
	if acting {
		req.Header.Set(tenancydomain.ActingAsHeader, "hub:"+s.hub.String())
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return svcReply{rec.Code, out}
}

var serviceKeys = []string{"service.read", "service.create", "service.update", "service.close", "service.assign", "service.link_external", "conversation.read"}

// agentWith makes a Hub agent whose grant AND the contract's ceiling hold exactly the keys.
func (s *svcWorld) agentWith(keys ...string) uuid.UUID {
	s.t.Helper()
	agent := s.hubAgent("svc-agent-" + uuid.NewString()[:6])
	g := s.grant(agent, "A")
	// the contract (shared by every agent of the instance) "delegates" everything, even what must NEVER be delegable; each grant narrows it
	s.ceiling("A", append([]string{"service.request_external", "tenant.manage", "contact.read"}, serviceKeys...)...)
	s.grantKeys(g, append([]string{"service.request_external", "tenant.manage"}, keys...)...)
	return agent
}

func TestAHubAgentWorksASessionInsideItsScopeAndNowhereElse(t *testing.T) {
	s := newSvcWorld(t)
	agent := s.agentWith(serviceKeys...)
	convA, convA2, convA0 := s.conv["A"], s.conv["A2"], s.conv["A0"]

	// not acting for the hub, the same person is nobody here
	if r := s.call(agent, "A", false, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, convA)); r.code != http.StatusForbidden && r.code != http.StatusNotFound {
		t.Fatalf("outside the delegated context: %d %v", r.code, r.body)
	}
	r := s.call(agent, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, convA))
	if r.code != http.StatusCreated || !strings.HasSuffix(fmt.Sprint(r.body["protocol"]), "-1001") || r.body["opened_by_user_id"] != agent.String() {
		t.Fatalf("a delegate opens a session on a conversation of its scope: %d %v", r.code, r.body)
	}
	id := r.body["id"].(string)
	for name, conv := range map[string]uuid.UUID{"another queue": convA2, "no queue": convA0} {
		if r := s.call(agent, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, conv)); r.code != http.StatusNotFound {
			t.Errorf("%s: a conversation outside the contract's scope is not there for the delegate: %d %v", name, r.code, r.body)
		}
	}
	// reading: only what is visible
	member := s.user("svc-member")
	s.directMember(member, "A")
	for _, c := range []uuid.UUID{convA2, convA0} { // a member opens sessions the delegate must never see
		if r := s.call(member, "A", false, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, c)); r.code != http.StatusCreated {
			t.Fatalf("member: %d %v", r.code, r.body)
		}
	}
	if r := s.call(agent, "A", true, "GET", "/service-sessions", ""); r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("the delegate lists only the sessions of its scope: %d %v", r.code, r.body)
	}
	if r := s.call(member, "A", false, "GET", "/service-sessions", ""); len(r.body["items"].([]any)) != 3 {
		t.Fatalf("the member sees all three: %v", r.body)
	}
	invisible := s.ownerOne(`SELECT id::text FROM service_sessions WHERE conversation_id = $1`, convA2)
	if r := s.call(agent, "A", true, "GET", "/service-sessions/"+invisible, ""); r.code != http.StatusNotFound {
		t.Fatalf("a session of another queue: %d", r.code)
	}
	// working it
	if r := s.call(agent, "A", true, "POST", "/service-sessions/"+id+"/status", `{"status":"in_progress"}`, "If-Match", `"1"`); r.code != http.StatusOK {
		t.Fatalf("status: %d %v", r.code, r.body)
	}
	task := s.call(agent, "A", true, "POST", "/service-tasks", fmt.Sprintf(`{"title":"Ligar amanhã","session_id":"%s"}`, id))
	if task.code != http.StatusCreated || task.body["origin_type"] != "human" {
		t.Fatalf("task: %d %v", task.code, task.body)
	}
	cs := s.call(agent, "A", true, "POST", "/service-cases", fmt.Sprintf(`{"title":"Link instável","session_id":"%s"}`, id))
	if cs.code != http.StatusCreated {
		t.Fatalf("case from a visible session: %d %v", cs.code, cs.body)
	}
	if r := s.call(agent, "A", true, "POST", "/service-cases", `{"title":"Caso solto"}`); r.code != http.StatusForbidden {
		t.Fatalf("a case that hangs from nothing the delegate can see: %d %v", r.code, r.body)
	}
	if r := s.call(agent, "A", true, "POST", "/service-cases", fmt.Sprintf(`{"title":"Do outro lado","session_id":"%s"}`, invisible)); r.code != http.StatusNotFound {
		t.Fatalf("a case from a session outside the scope: %d %v", r.code, r.body)
	}
	if r := s.call(agent, "A", true, "GET", "/service-sessions/"+id+"/timeline?order=asc", ""); r.code != http.StatusOK || len(r.body["items"].([]any)) < 3 {
		t.Fatalf("timeline: %d %v", r.code, r.body)
	}
	closed := s.call(agent, "A", true, "POST", "/service-sessions/"+id+"/close", `{"resolution_code":"information_provided"}`, "If-Match", `"2"`)
	if closed.code != http.StatusOK || closed.body["status"] != "closed" {
		t.Fatalf("close: %d %v", closed.code, closed.body)
	}
	// the trail names who acted, through which hub, contract and grant
	var via, hubID, contract, grant string
	if err := s.owner.QueryRow(s.ctx, `SELECT metadata->>'via', metadata->>'hub_id', metadata->>'contract_id', metadata->>'grant_id' FROM audit_events
		WHERE tenant_id=$1 AND action='service_session.closed' AND resource_id=$2`, s.tenant["A"], id).Scan(&via, &hubID, &contract, &grant); err != nil {
		t.Fatalf("a delegated action is audited: %v", err)
	}
	if via != "hub" || hubID != s.hub.String() || contract != s.contract["A"].String() || grant == "" {
		t.Fatalf("who, through which hub, contract and grant: %s %s %s %s", via, hubID, contract, grant)
	}
	var actor uuid.UUID
	_ = s.owner.QueryRow(s.ctx, `SELECT actor_id FROM audit_events WHERE tenant_id=$1 AND action='service_session.closed' AND resource_id=$2`, s.tenant["A"], id).Scan(&actor)
	if actor != agent {
		t.Fatalf("the audited actor is the agent: %v", actor)
	}
	// the events of the delegate's own actions are signed by the agent
	if n := s.count(`SELECT count(*) FROM context_events WHERE tenant_id=$1 AND session_id=$2 AND actor_id=$3 AND actor_type='user'`, s.tenant["A"], id, agent); n < 3 {
		t.Fatalf("history signed by the agent: %d", n)
	}
}

func TestAReadOnlyGrantReadsAndChangesNothing(t *testing.T) {
	s := newSvcWorld(t)
	writer := s.agentWith(serviceKeys...)
	reader := s.agentWith("service.read", "conversation.read")
	r := s.call(writer, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s.conv["A"]))
	id := r.body["id"].(string)
	if g := s.call(reader, "A", true, "GET", "/service-sessions/"+id, ""); g.code != http.StatusOK {
		t.Fatalf("read: %d %v", g.code, g.body)
	}
	for name, req := range map[string][3]string{
		"open":   {"POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s.conv["A"])},
		"patch":  {"PATCH", "/service-sessions/" + id, `{"queue_id":"` + s.queue1["A"].String() + `"}`},
		"status": {"POST", "/service-sessions/" + id + "/status", `{"status":"waiting"}`},
		"close":  {"POST", "/service-sessions/" + id + "/close", `{"resolution_code":"other"}`},
		"task":   {"POST", "/service-tasks", fmt.Sprintf(`{"title":"x","session_id":"%s"}`, id)},
		"case":   {"POST", "/service-cases", fmt.Sprintf(`{"title":"x","session_id":"%s"}`, id)},
		"link":   {"POST", "/external-records", fmt.Sprintf(`{"integration_id":"%s","external_type":"service_order","external_id":"1","session_id":"%s"}`, uuid.New(), id)},
	} {
		if r := s.call(reader, "A", true, req[0], req[1], req[2], "If-Match", `"1"`); r.code != http.StatusForbidden {
			t.Errorf("%s with a read-only grant: %d %v", name, r.code, r.body)
		}
	}
	// and the database says the same to plain SQL, in the delegated context
	s.attend(reader, "A", func(ctx context.Context, q platformdb.Querier) {
		if tag, err := q.Exec(ctx, `UPDATE service_sessions SET queue_id = NULL WHERE id = $1`, id); err != nil || tag.RowsAffected() != 0 {
			t.Errorf("read-only keys updated a session by SQL: rows=%d err=%v", tag.RowsAffected(), err)
		}
		if err := s.try(ctx, q, `INSERT INTO service_sessions(tenant_id, protocol, protocol_sequence, opened_source) VALUES($1,'AT-1',1,'api')`, s.tenant["A"]); err == nil {
			t.Error("read-only keys inserted a session by SQL")
		}
	})
	// a key the grant holds but the contract's ceiling does not is not a key
	capped := s.hubAgent("capped")
	g := s.grant(capped, "A")
	s.ceiling("A", "service.read", "conversation.read") // the ceiling shrinks: every agent of the instance feels it
	s.grantKeys(g, "service.read", "service.create", "conversation.read")
	if r := s.call(capped, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s.conv["A"])); r.code != http.StatusForbidden {
		t.Fatalf("beyond the ceiling: %d %v", r.code, r.body)
	}
	// revoked: the next call is a 404, nothing copied
	s.ceiling("A", serviceKeys...)
	w2 := s.agentWith(serviceKeys...)
	if g := s.call(w2, "A", true, "GET", "/service-sessions/"+id, ""); g.code != http.StatusOK {
		t.Fatalf("before revoking: %d", g.code)
	}
	s.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE user_id = $1`, w2)
	if g := s.call(w2, "A", true, "GET", "/service-sessions/"+id, ""); g.code == http.StatusOK {
		t.Fatalf("a revoked grant reads nothing: %d", g.code)
	}
	s.exec(`UPDATE service_hubs SET status = 'suspended' WHERE id = $1`, s.hub)
	if g := s.call(writer, "A", true, "GET", "/service-sessions/"+id, ""); g.code == http.StatusOK {
		t.Fatalf("a suspended hub reads nothing: %d", g.code)
	}
	s.exec(`UPDATE service_hubs SET status = 'active' WHERE id = $1`, s.hub)
	// a contract that is no longer in force closes the door on the very next call
	if g := s.call(writer, "A", true, "GET", "/service-sessions/"+id, ""); g.code != http.StatusOK {
		t.Fatalf("before suspending the contract: %d", g.code)
	}
	s.exec(`UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE id = $1`, s.contract["A"])
	if g := s.call(writer, "A", true, "GET", "/service-sessions/"+id, ""); g.code == http.StatusOK {
		t.Fatalf("a suspended contract reads nothing: %d", g.code)
	}
	s.exec(`UPDATE hub_tenant_service_contracts SET status = 'active' WHERE id = $1`, s.contract["A"])
}

func TestWhatIsNotDelegableStaysOutOfReach(t *testing.T) {
	s := newSvcWorld(t)
	agent := s.agentWith(serviceKeys...) // (the contract and the grant even list service.request_external and tenant.manage)
	conv := s.conv["A"]
	r := s.call(agent, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, conv))
	id := r.body["id"].(string)
	integ := uuid.New()
	s.exec(`INSERT INTO channel_connections(id, tenant_id, channel, provider, provider_kind, external_number_id) VALUES($1,$2,'erp','fake_erp','official',$3)`, integ, s.tenant["A"], "erp-"+integ.String())
	for name, req := range map[string][3]string{
		"ask the ERP for an order": {"POST", "/service-sessions/" + id + "/external-service-orders", fmt.Sprintf(`{"integration_id":"%s","title":"Visita"}`, integ)},
		"reconcile":                {"POST", "/external-requests/" + uuid.NewString() + "/reconcile", ``},
		"resolve":                  {"POST", "/external-requests/" + uuid.NewString() + "/resolve", `{"created":false,"reason":"nada"}`},
		"create a category":        {"POST", "/service-categories", `{"code":"x","label":"X"}`},
		"create a result code":     {"POST", "/service-result-codes", `{"code":"x","canonical_code":"other","label":"X"}`},
	} {
		if r := s.call(agent, "A", true, req[0], req[1], req[2], "Idempotency-Key", "deleg-"+uuid.NewString()); r.code != http.StatusNotFound {
			t.Errorf("%s: an unmarked route is a 404 in the delegated context, got %d %v", name, r.code, r.body)
		}
	}
	// the key itself is not delegable: the grant lists it, the answer does not
	keys := s.delegated(agent, "A")
	for _, k := range keys {
		if k == "service.request_external" || k == "tenant.manage" {
			t.Fatalf("%s must never be a delegated key: %v", k, keys)
		}
	}
	// and the requests made to the ERP are closed to a delegate even for reading
	req := s.owner
	_ = req
	s.exec(`INSERT INTO external_record_requests(tenant_id, integration_id, session_id, operation, title, idempotency_key, request_hash, correlation_key) VALUES($1,$2,$3,'create_service_order','t','deleg-key-0001','h','deleg-corr-0001')`, s.tenant["A"], integ, id)
	s.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
		var n int
		s.must(q.QueryRow(ctx, `SELECT count(*) FROM external_record_requests`).Scan(&n))
		if n != 0 {
			t.Errorf("a delegate reads %d requests made to the ERP", n)
		}
		if err := s.try(ctx, q, `INSERT INTO external_record_requests(tenant_id, integration_id, session_id, operation, title, idempotency_key, request_hash, correlation_key) VALUES($1,$2,$3,'create_service_order','t','deleg-key-0002','h','deleg-corr-0002')`, s.tenant["A"], integ, id); err == nil {
			t.Error("a delegate wrote a request to the ERP")
		}
	})
	// the catalog is readable (the agent needs it to classify) but never writable
	if r := s.call(agent, "A", true, "GET", "/service-categories", ""); r.code != http.StatusOK {
		t.Fatalf("the catalog is readable by a delegate: %d %v", r.code, r.body)
	}
	s.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
		if err := s.try(ctx, q, `INSERT INTO service_categories(tenant_id, code, label) VALUES($1,'sneaky','Sneaky')`, s.tenant["A"]); err == nil {
			t.Error("a delegate wrote the company's catalog")
		}
	})
}

func TestADelegateLinksWhatTheERPConfirmsAndOneInstanceAtATime(t *testing.T) {
	s := newSvcWorld(t)
	agent := s.agentWith(serviceKeys...)
	integ := uuid.New()
	s.exec(`INSERT INTO channel_connections(id, tenant_id, channel, provider, provider_kind, external_number_id) VALUES($1,$2,'erp','fake_erp','official',$3)`, integ, s.tenant["A"], "erp-"+integ.String())
	at := time.Now().UTC().Add(-time.Hour)
	s.gw.Put(snapshotOS("7001", at))
	r := s.call(agent, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s.conv["A"]))
	id := r.body["id"].(string)
	link := s.call(agent, "A", true, "POST", "/external-records", fmt.Sprintf(`{"integration_id":"%s","external_type":"service_order","external_id":"7001","session_id":"%s"}`, integ, id))
	if link.code != http.StatusCreated {
		t.Fatalf("a delegate with service.link_external links a confirmed record: %d %v", link.code, link.body)
	}
	if g := s.call(agent, "A", true, "GET", "/external-records?session_id="+id, ""); g.code != http.StatusOK || len(g.body["items"].([]any)) != 1 {
		t.Fatalf("it reads the record it can reach: %d %v", g.code, g.body)
	}
	// a record that hangs from a session of ANOTHER queue is invisible, and so is the fact that it exists
	memberConvA2 := s.conv["A2"]
	member := s.user("svc-m2")
	s.directMember(member, "A")
	other := s.call(member, "A", false, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, memberConvA2))
	s.gw.Put(snapshotOS("7002", at))
	if l := s.call(member, "A", false, "POST", "/external-records", fmt.Sprintf(`{"integration_id":"%s","external_type":"service_order","external_id":"7002","session_id":"%s"}`, integ, other.body["id"])); l.code != http.StatusCreated {
		t.Fatalf("member links: %d %v", l.code, l.body)
	}
	if g := s.call(agent, "A", true, "GET", "/external-records", ""); len(g.body["items"].([]any)) != 1 {
		t.Fatalf("the delegate sees only the record of its scope: %v", g.body)
	}
	if l := s.call(agent, "A", true, "POST", "/external-records", fmt.Sprintf(`{"integration_id":"%s","external_type":"service_order","external_id":"7002","session_id":"%s"}`, integ, id)); l.code == http.StatusCreated || l.code == http.StatusOK {
		t.Fatalf("a delegate cannot attach a record it cannot see: %d %v", l.code, l.body)
	}

	// ONE instance at a time: a person with grants on A and B, acting for A, writes nothing in B
	both := s.hubAgent("both-instances")
	ga, gb := s.grant(both, "A"), s.grant(both, "B")
	s.ceiling("A", serviceKeys...)
	s.ceiling("B", serviceKeys...)
	s.grantKeys(ga, serviceKeys...)
	s.grantKeys(gb, serviceKeys...)
	if r := s.call(both, "B", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s.conv["B"])); r.code != http.StatusCreated {
		t.Fatalf("acting for B, B is reachable: %d %v", r.code, r.body)
	}
	sessB := s.ownerOne(`SELECT id::text FROM service_sessions WHERE conversation_id = $1`, s.conv["B"])
	s.attend(both, "A", func(ctx context.Context, q platformdb.Querier) {
		var n int
		s.must(q.QueryRow(ctx, `SELECT count(*) FROM service_sessions WHERE tenant_id = $1`, s.tenant["B"]).Scan(&n))
		if n != 0 {
			t.Errorf("acting for A, the delegate reads %d sessions of B", n)
		}
		if tag, err := q.Exec(ctx, `UPDATE service_sessions SET queue_id = NULL WHERE id = $1`, sessB); err != nil || tag.RowsAffected() != 0 {
			t.Errorf("acting for A, the delegate updated a session of B: rows=%d err=%v", tag.RowsAffected(), err)
		}
		if err := s.try(ctx, q, `INSERT INTO service_tasks(tenant_id, title, origin_type, session_id) VALUES($1,'x','human',$2)`, s.tenant["B"], sessB); err == nil {
			t.Error("acting for A, the delegate wrote a task into B")
		}
	})
	// a session id of B used in A's URL is just not there
	if r := s.call(both, "A", true, "GET", "/service-sessions/"+sessB, ""); r.code != http.StatusNotFound {
		t.Fatalf("B's session through A's URL: %d", r.code)
	}
}

func TestAMemberWhoIsAlsoADelegateGetsExactlyTheContextTheyAskedFor(t *testing.T) {
	s := newSvcWorld(t)
	both := s.agentWith("service.read", "conversation.read") // as a delegate: read only
	s.directMember(both, "A")                                // as a member of A: the stock tenant_agent role (read, create, update, close, link)
	conv := s.conv["A2"]                                     // a queue the delegate's scope does not reach
	asMember := s.call(both, "A", false, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, conv))
	if asMember.code != http.StatusCreated {
		t.Fatalf("as a member, with the member's keys and no queue scope: %d %v", asMember.code, asMember.body)
	}
	id := asMember.body["id"].(string)
	if r := s.call(both, "A", true, "GET", "/service-sessions/"+id, ""); r.code != http.StatusNotFound {
		t.Fatalf("acting as a delegate, the member's own session in an out-of-scope queue is not there: %d", r.code)
	}
	if r := s.call(both, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s.conv["A"])); r.code != http.StatusForbidden {
		t.Fatalf("acting as a delegate the member's keys do not apply: %d %v", r.code, r.body)
	}
	if !strings.Contains(fmt.Sprint(asMember.body["opened_by_user_id"]), both.String()) {
		t.Fatalf("opened by the person: %v", asMember.body)
	}
}

func snapshotOS(id string, at time.Time) domain.Snapshot {
	return domain.Snapshot{ExternalType: "service_order", ExternalID: id, DisplayRef: "OS " + id, ExternalStatus: "Aberta", StatusClass: domain.StatusOpen, UpdatedAt: &at}
}

// A Hub delegate cannot lock a conversation (it has no UPDATE policy on it), so what keeps its opening of a session from interleaving with a finalization is the
// database: the row guard of the session takes a KEY SHARE lock that waits for the finalization and then reads the fresh status.
func TestADelegateOpeningWaitsForAFinalizationAndIsRefusedAfterIt(t *testing.T) {
	s := newSvcWorld(t)
	agent := s.agentWith(serviceKeys...)
	conv := s.conv["A"]
	tx, err := s.owner.Begin(s.ctx)
	s.must(err)
	defer func() { _ = tx.Rollback(s.ctx) }()
	_, err = tx.Exec(s.ctx, `SELECT 1 FROM conversations WHERE id = $1 FOR UPDATE`, conv) // what Finalize does first
	s.must(err)
	_, err = tx.Exec(s.ctx, `UPDATE conversations SET status = 'closed', closed_at = now() WHERE id = $1`, conv)
	s.must(err)

	done := make(chan svcReply, 1)
	go func() {
		done <- s.call(agent, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, conv))
	}()
	select {
	case r := <-done:
		t.Fatalf("the delegate's opening did not wait for the finalization: %d %v", r.code, r.body)
	case <-time.After(700 * time.Millisecond):
	}
	s.must(tx.Commit(s.ctx))
	select {
	case r := <-done:
		if r.code != http.StatusConflict || r.body["error"] != "conversation_closed" {
			t.Fatalf("after the finalization committed the conversation is closed: %d %v", r.code, r.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the delegate's opening never resumed")
	}
	if n := s.count(`SELECT count(*) FROM service_sessions WHERE conversation_id = $1`, conv); n != 0 {
		t.Fatalf("no session was left on the closed conversation: %d", n)
	}
}

// Several delegates open an attendance on the same conversation at once: one session stands, everybody is answered with it, and the protocol has no hole.
func TestDelegatesOpeningTheSameConversationTogetherMakeOneSession(t *testing.T) {
	s := newSvcWorld(t)
	var agents []uuid.UUID
	for i := 0; i < 6; i++ {
		agents = append(agents, s.agentWith(serviceKeys...))
	}
	conv := s.conv["A"]
	codes := make(chan int, len(agents))
	for _, a := range agents {
		go func(a uuid.UUID) {
			codes <- s.call(a, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, conv)).code
		}(a)
	}
	created, ok := 0, 0
	for range agents {
		switch c := <-codes; c {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			ok++
		default:
			t.Errorf("unexpected %d", c)
		}
	}
	if created != 1 || ok != len(agents)-1 {
		t.Fatalf("one creation and everybody else answered with it: created=%d ok=%d", created, ok)
	}
	if n := s.count(`SELECT count(*) FROM service_sessions WHERE conversation_id = $1`, conv); n != 1 {
		t.Fatalf("one session: %d", n)
	}
	if seq := s.ownerOne(`SELECT last_value::text FROM service_counters WHERE tenant_id = $1 AND kind = 'session'`, s.tenant["A"]); seq != "1001" {
		t.Fatalf("the numbers the losers took went back with their savepoints: last=%s", seq)
	}
}

// Codex phase 5: seeing a contact through one conversation of the contract's scope says nothing about work done for the same person elsewhere. A task (or an
// event) tied to the contact ALONE is not visible to a delegate; one that hangs from a session of its scope is.
func TestAContactOnlyTaskIsNotVisibleToADelegate(t *testing.T) {
	s := newSvcWorld(t)
	// the delegate may read contacts too: that is exactly what made a contact-only task look reachable
	agent := s.agentWith(append([]string{"contact.read"}, serviceKeys...)...)
	member := s.user("svc-member-contact")
	s.directMember(member, "A")
	contact := s.ownerOne(`SELECT contact_id::text FROM conversations WHERE id = $1`, s.conv["A"])
	made := s.call(member, "A", false, "POST", "/service-tasks", fmt.Sprintf(`{"title":"Só do contato","contact_id":"%s"}`, contact))
	if made.code != http.StatusCreated {
		t.Fatalf("a member makes a task for the contact: %d %v", made.code, made.body)
	}
	id := made.body["id"].(string)
	// the delegate attends a conversation of this contact and holds contact.read, so it DOES see the contact ...
	// ... and still does not see the task that hangs from nothing it can see
	if r := s.call(agent, "A", true, "GET", "/service-tasks/"+id, ""); r.code != http.StatusNotFound {
		t.Fatalf("a contact-only task by id: %d %v", r.code, r.body)
	}
	if r := s.call(agent, "A", true, "GET", "/service-tasks?contact_id="+contact, ""); r.code != http.StatusOK || len(r.body["items"].([]any)) != 0 {
		t.Fatalf("a contact-only task in a list: %d %v", r.code, r.body)
	}
	if r := s.call(member, "A", false, "GET", "/service-tasks?contact_id="+contact, ""); len(r.body["items"].([]any)) != 1 {
		t.Fatalf("the member still sees it: %v", r.body)
	}
	// the same person's task THROUGH a visible session is seen
	sess := s.call(agent, "A", true, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, s.conv["A"]))
	if sess.code != http.StatusCreated {
		t.Fatalf("session: %d %v", sess.code, sess.body)
	}
	task := s.call(agent, "A", true, "POST", "/service-tasks", fmt.Sprintf(`{"title":"Com sessão","session_id":"%s"}`, sess.body["id"]))
	if task.code != http.StatusCreated {
		t.Fatalf("task through a visible session: %d %v", task.code, task.body)
	}
	if r := s.call(agent, "A", true, "GET", "/service-tasks?contact_id="+contact, ""); len(r.body["items"].([]any)) != 1 {
		t.Fatalf("only the task that hangs from the session is listed: %v", r.body)
	}
	// the same for an EVENT tied to the contact alone (the owner writes one: a delegate or a member never could without a session or case to hang it from)
	s.exec(`INSERT INTO context_events(id, tenant_id, event_type, entity_type, entity_id, contact_id, actor_type, occurred_at, note)
		VALUES(gen_random_uuid(), $1, 'note.added', 'contact', $2, $2, 'system', now(), 'so do contato')`, s.tenant["A"], contact)
	if r := s.call(agent, "A", true, "GET", "/contacts/"+contact+"/service-timeline", ""); r.code != http.StatusOK || strings.Contains(fmt.Sprint(r.body), "so do contato") {
		t.Fatalf("a contact-only event is not in the delegate's timeline: %d %v", r.code, r.body)
	}
	if r := s.call(member, "A", false, "GET", "/contacts/"+contact+"/service-timeline", ""); !strings.Contains(fmt.Sprint(r.body), "so do contato") {
		t.Fatalf("the member sees it: %d %v", r.code, r.body)
	}
	// and the delegate cannot create one tied to the contact alone
	if r := s.call(agent, "A", true, "POST", "/service-tasks", fmt.Sprintf(`{"title":"Solta","contact_id":"%s"}`, contact)); r.code == http.StatusCreated {
		t.Fatalf("a delegate cannot create work that hangs from nothing it sees: %d %v", r.code, r.body)
	}
}
