package adapters_test

// ADR-0040 phase 02 on a real PostgreSQL, with the application's own role and RLS session (never the owner, never a system session):
// the CORE of delegated serving (migration 108). What must hold:
//   - what a person may do through a Hub is the grant AND the contract's ceiling AND only keys that are delegable at all;
//   - every link of the chain is evaluated at call time: a revoked grant, a suspended hub, instance or contract, an expired validity, an
//     inactive account, a shrunk ceiling each take effect on the very next call, nothing is copied;
//   - a request acts as a member OR for one hub, never as the sum: a person who is both gets exactly the context they asked for;
//   - the tenant in the URL and the hub in the header are targets, never authority: another instance or another hub is a uniform 404;
//   - the audit trail of a delegated action says who acted, through which hub, contract and grant.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func (w *world) ceiling(tenantKey string, keys ...string) {
	if keys == nil {
		keys = []string{}
	}
	w.exec(`UPDATE hub_tenant_service_contracts SET delegable_permissions = $2 WHERE id = $1`, w.contract[tenantKey], keys)
}

func (w *world) grantKeys(grant uuid.UUID, keys ...string) {
	if keys == nil {
		keys = []string{}
	}
	w.exec(`UPDATE effective_access_grants SET permissions = $2 WHERE id = $1`, grant, keys)
}

// inSession runs fn as the user in a request-like transaction (the application's session machinery).
func (w *world) inSession(user uuid.UUID, fn func(ctx context.Context, q platformdb.Querier)) {
	w.t.Helper()
	w.must(platformdb.WithTenantSession(w.ctx, w.app, user, false, func(c context.Context) error {
		fn(c, platformdb.QuerierFromContext(c, w.app))
		return nil
	}))
}

// enter puts the request in the delegated context for one instance through w.hub, the way the middleware does.
func (w *world) enter(ctx context.Context, q platformdb.Querier, user uuid.UUID, tenantKey string) bool {
	w.t.Helper()
	var ok *bool
	w.must(q.QueryRow(ctx, `SELECT lock_served_tenant($1, $2, $3)`, w.tenant[tenantKey], user, w.hub).Scan(&ok))
	return ok != nil && *ok
}

func (w *world) delegated(user uuid.UUID, tenantKey string) []string {
	w.t.Helper()
	var keys []string
	w.inSession(user, func(ctx context.Context, q platformdb.Querier) {
		w.must(q.QueryRow(ctx, `SELECT delegated_permissions($1, $2, $3)`, w.tenant[tenantKey], user, w.hub).Scan(&keys))
	})
	sort.Strings(keys)
	return keys
}

func (w *world) acting(ctx context.Context, q platformdb.Querier) string {
	var s string
	w.must(q.QueryRow(ctx, `SELECT COALESCE(current_setting('app.acting_hub', true), '')`).Scan(&s))
	return s
}

func (w *world) hasPerm(ctx context.Context, q platformdb.Querier, user uuid.UUID, tenantKey, perm string) bool {
	w.t.Helper()
	var ok bool
	w.must(q.QueryRow(ctx, `SELECT actor_has_permission($1, $2, $3)`, w.tenant[tenantKey], user, perm).Scan(&ok))
	return ok
}

func (w *world) domainAccess(ctx context.Context, q platformdb.Querier, user uuid.UUID, tenantKey, domain, need string) bool {
	w.t.Helper()
	var ok bool
	w.must(q.QueryRow(ctx, `SELECT has_delegated_access($1, $2, $3, $4)`, w.tenant[tenantKey], user, domain, need).Scan(&ok))
	return ok
}

func TestDelegatedPermissionsAreTheGrantAndTheCeilingAndOnlyDelegableKeys(t *testing.T) {
	w := newWorld(t)
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	// the contract delegates reading, replying, contact reading, and (by mistake or malice) membership and channel management
	w.ceiling("A", "conversation.read", "conversation.reply", "contact.read", "membership.manage", "channel.manage", "tenant.manage", "no.such.key")
	// the grant holds some of those, plus one the contract does NOT delegate (classify), plus the same dangerous keys
	w.grantKeys(g, "conversation.read", "contact.read", "contact.classify", "membership.manage", "tenant.manage", "no.such.key")
	got := w.delegated(agent, "A")
	if want := []string{"contact.read", "conversation.read"}; !reflect.DeepEqual(got, want) {
		t.Errorf("grant AND ceiling AND delegable = %v, want %v (classify is above the ceiling; membership/tenant keys are never delegable, even when both sides hold them)", got, want)
	}
	// nothing delegated by the contract: the grant alone confers nothing
	w.ceiling("A")
	if got := w.delegated(agent, "A"); len(got) != 0 {
		t.Errorf("an empty ceiling must leave nothing: %v", got)
	}
	// nothing granted: the ceiling alone confers nothing
	w.ceiling("A", "conversation.read")
	w.grantKeys(g)
	if got := w.delegated(agent, "A"); len(got) != 0 {
		t.Errorf("an empty grant must leave nothing: %v", got)
	}
}

func TestEverySecretAndAdministrativeKeyIsOutsideTheDelegableSet(t *testing.T) {
	w := newWorld(t)
	// not by a list in the test: the structural fact is that these keys have no row in permission_domains
	var keys []string
	w.must(w.owner.QueryRow(w.ctx, `SELECT array_agg(permission_key ORDER BY permission_key) FROM permission_domains`).Scan(&keys))
	for _, k := range keys {
		for _, banned := range []string{"membership.", "tenant.manage", "agent.manage", "channel.manage", "identity.manage", "audit.read", "grant.", "hub.", "group.", "dashboard.", "flow.archive", "flow.publish", "flow.edit", "flow.create", "account.manage", "ticket.reconcile", "conversation.manage"} {
			if len(k) >= len(banned) && k[:len(banned)] == banned {
				t.Errorf("%q is delegable but must never be (team, contract, credentials, security, administration)", k)
			}
		}
	}
	if len(keys) == 0 {
		t.Fatal("permission_domains is empty")
	}
}

func TestDelegatedAccessIsEvaluatedLiveAtEveryLink(t *testing.T) {
	w := newWorld(t)
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read")
	w.grantKeys(g, "conversation.read")
	want := []string{"conversation.read"}
	if got := w.delegated(agent, "A"); !reflect.DeepEqual(got, want) {
		t.Fatalf("baseline: %v", got)
	}
	// each case breaks ONE link, expects nothing, then restores it
	for _, c := range []struct{ name, break_, restore string }{
		{"grant revoked", `UPDATE effective_access_grants SET status='revoked' WHERE id=$1`, `UPDATE effective_access_grants SET status='active' WHERE id=$1`},
		{"grant suspended", `UPDATE effective_access_grants SET status='suspended' WHERE id=$1`, `UPDATE effective_access_grants SET status='active' WHERE id=$1`},
		{"grant expired", `UPDATE effective_access_grants SET valid_until = now() - interval '1 minute' WHERE id=$1`, `UPDATE effective_access_grants SET valid_until = NULL WHERE id=$1`},
		{"grant not started", `UPDATE effective_access_grants SET valid_from = now() + interval '1 hour', valid_until = NULL WHERE id=$1`, `UPDATE effective_access_grants SET valid_from = now() - interval '1 day' WHERE id=$1`},
		{"contract suspended", `UPDATE hub_tenant_service_contracts SET status='suspended' WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`, `UPDATE hub_tenant_service_contracts SET status='active' WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`},
		{"contract revoked", `UPDATE hub_tenant_service_contracts SET status='revoked' WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`, `UPDATE hub_tenant_service_contracts SET status='active' WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`},
		{"contract expired", `UPDATE hub_tenant_service_contracts SET valid_until = now() - interval '1 minute' WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`, `UPDATE hub_tenant_service_contracts SET valid_until = NULL WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`},
		{"contract not started", `UPDATE hub_tenant_service_contracts SET valid_from = now() + interval '1 hour', valid_until = NULL WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`, `UPDATE hub_tenant_service_contracts SET valid_from = now() - interval '1 day' WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`},
		{"instance suspended", `UPDATE tenants SET status='suspended' WHERE id=(SELECT tenant_id FROM effective_access_grants WHERE id=$1)`, `UPDATE tenants SET status='active' WHERE id=(SELECT tenant_id FROM effective_access_grants WHERE id=$1)`},
		{"hub suspended", `UPDATE service_hubs SET status='suspended' WHERE id=(SELECT hub_id FROM effective_access_grants WHERE id=$1)`, `UPDATE service_hubs SET status='active' WHERE id=(SELECT hub_id FROM effective_access_grants WHERE id=$1)`},
		{"account inactive", `UPDATE users SET status='inactive' WHERE id=(SELECT user_id FROM effective_access_grants WHERE id=$1)`, `UPDATE users SET status='active' WHERE id=(SELECT user_id FROM effective_access_grants WHERE id=$1)`},
		{"ceiling shrunk", `UPDATE hub_tenant_service_contracts SET delegable_permissions='{}' WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`, `UPDATE hub_tenant_service_contracts SET delegable_permissions=ARRAY['conversation.read'] WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`},
	} {
		w.exec(c.break_, g)
		if got := w.delegated(agent, "A"); len(got) != 0 {
			t.Errorf("%s: must leave nothing at once, got %v", c.name, got)
		}
		var entered bool
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) { entered = w.enter(ctx, q, agent, "A") })
		if entered {
			t.Errorf("%s: the door into the delegated context must refuse", c.name)
		}
		w.exec(c.restore, g)
		if got := w.delegated(agent, "A"); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: restoring must bring it back (the test is wrong otherwise): %v", c.name, got)
		}
	}
	// the hub membership going away takes the grant with it (cascade): nothing is left to serve with
	w.exec(`DELETE FROM hub_memberships WHERE hub_id=$1 AND user_id=$2`, w.hub, agent)
	if got := w.delegated(agent, "A"); len(got) != 0 {
		t.Errorf("hub membership removed: %v", got)
	}
}

func TestDelegatedAccessIsForOneInstanceOneHubAndOnlyForTheCaller(t *testing.T) {
	w := newWorld(t)
	agent := w.hubAgent("agent")
	other := w.hubAgent("other")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read")
	w.ceiling("B", "conversation.read")
	w.grantKeys(g, "conversation.read")
	if got := w.delegated(agent, "B"); len(got) != 0 {
		t.Errorf("a grant on A confers nothing on B: %v", got)
	}
	// somebody else's relationship cannot be asked about (the guard is the caller's own id)
	var asked []string
	w.inSession(other, func(ctx context.Context, q platformdb.Querier) {
		w.must(q.QueryRow(ctx, `SELECT delegated_permissions($1, $2, $3)`, w.tenant["A"], agent, w.hub).Scan(&asked))
	})
	if len(asked) != 0 {
		t.Errorf("asking about another person must answer nothing: %v", asked)
	}
	// another hub's id with the same tenant and user: no relationship
	var wrongHub []string
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		w.must(q.QueryRow(ctx, `SELECT delegated_permissions($1, $2, $3)`, w.tenant["A"], agent, uuid.New()).Scan(&wrongHub))
	})
	if len(wrongHub) != 0 {
		t.Errorf("another hub: %v", wrongHub)
	}
	// no hub named: serving is always for ONE hub, so there is nothing to answer for "any hub"
	var noHub []string
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		w.must(q.QueryRow(ctx, `SELECT delegated_permissions($1, $2, NULL)`, w.tenant["A"], agent).Scan(&noHub))
	})
	if len(noHub) != 0 {
		t.Errorf("no hub named: %v", noHub)
	}
	// a hub admin is served by a grant like anybody else: the role alone confers nothing here
	admin := w.hubAdmin("admin")
	if got := w.delegated(admin, "A"); len(got) != 0 {
		t.Errorf("a hub admin without a grant: %v", got)
	}
}

func TestTheDoorSetsTheActingContextOnlyWhenTheRelationshipIsLive(t *testing.T) {
	w := newWorld(t)
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read")
	w.grantKeys(g, "conversation.read")
	stranger := w.user("stranger")
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if w.acting(ctx, q) != "" {
			t.Error("a fresh request must not be in a delegated context")
		}
		if !w.enter(ctx, q, agent, "A") {
			t.Fatal("a live relationship must enter")
		}
		if got := w.acting(ctx, q); got != w.hub.String() {
			t.Errorf("acting hub = %q, want %q", got, w.hub)
		}
	})
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if w.enter(ctx, q, agent, "B") { // no grant on B
			t.Error("another instance must be refused")
		}
		if w.acting(ctx, q) != "" {
			t.Error("a refused entry must not leave the context set")
		}
	})
	w.inSession(stranger, func(ctx context.Context, q platformdb.Querier) {
		if w.enter(ctx, q, stranger, "A") || w.acting(ctx, q) != "" {
			t.Error("a stranger must not enter")
		}
		// and cannot enter on somebody else's behalf
		var ok *bool
		w.must(q.QueryRow(ctx, `SELECT lock_served_tenant($1, $2, $3)`, w.tenant["A"], agent, w.hub).Scan(&ok))
		if ok != nil && *ok {
			t.Error("entering as another person must be refused")
		}
	})
	// the context lives and dies with the transaction: the next request on the pooled connection starts clean
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if w.acting(ctx, q) != "" {
			t.Error("the acting context leaked into the next request")
		}
	})
}

func TestARequestActsAsAMemberOrForAHubNeverAsTheSum(t *testing.T) {
	w := newWorld(t)
	both := w.hubAgent("both")
	w.directMember(both, "A") // tenant_agent: has conversation.claim, ticket.create, contact.classify... as a MEMBER
	g := w.grant(both, "A")
	w.ceiling("A", "conversation.read", "conversation.reply")
	w.grantKeys(g, "conversation.read") // delegated: read only
	// as a member (not acting for a hub): the role permissions, and nothing of the grant
	w.inSession(both, func(ctx context.Context, q platformdb.Querier) {
		if !w.hasPerm(ctx, q, both, "A", "conversation.claim") || !w.hasPerm(ctx, q, both, "A", "ticket.create") {
			t.Error("as a member the role permissions must hold")
		}
		if w.hasPerm(ctx, q, both, "A", "conversation.read") {
			t.Error("a delegated key must not leak into the member context")
		}
		if w.domainAccess(ctx, q, both, "A", "conversation", "read") {
			t.Error("domain access is a delegated-context predicate: false when not acting for a hub")
		}
	})
	// acting for the hub: only the grant, nothing of the membership
	w.inSession(both, func(ctx context.Context, q platformdb.Querier) {
		if !w.enter(ctx, q, both, "A") {
			t.Fatal("enter")
		}
		if !w.hasPerm(ctx, q, both, "A", "conversation.read") {
			t.Error("acting for the hub: the granted key must hold")
		}
		for _, k := range []string{"conversation.claim", "ticket.create", "contact.classify", "membership.read", "conversation.reply"} {
			if w.hasPerm(ctx, q, both, "A", k) {
				t.Errorf("acting for the hub, %q must be false: the member's role and the contract's unused ceiling contribute nothing", k)
			}
		}
		if !w.domainAccess(ctx, q, both, "A", "conversation", "read") || w.domainAccess(ctx, q, both, "A", "conversation", "write") {
			t.Error("a read-only grant: domain read yes, domain write no")
		}
	})
}

func TestAForgedActingContextConfersNothingItWasNotGranted(t *testing.T) {
	w := newWorld(t)
	member := w.user("member")
	w.directMember(member, "A")
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read")
	w.grantKeys(g, "conversation.read")
	// the application role can write the setting itself (the trust limit documented in ADR-0040 section 7); the predicates still decide from the live relationship
	w.inSession(member, func(ctx context.Context, q platformdb.Querier) {
		_, err := q.Exec(ctx, `SELECT set_config('app.acting_hub', $1, true)`, w.hub.String())
		w.must(err)
		for _, k := range []string{"conversation.read", "conversation.claim", "ticket.create"} {
			if w.hasPerm(ctx, q, member, "A", k) {
				t.Errorf("a member forging the context must get nothing (and lose the member privileges, which is harmless): %q", k)
			}
		}
		if w.domainAccess(ctx, q, member, "A", "conversation", "read") {
			t.Error("forged context + no grant = no domain access")
		}
	})
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		_, err := q.Exec(ctx, `SELECT set_config('app.acting_hub', $1, true)`, w.hub.String())
		w.must(err)
		if !w.hasPerm(ctx, q, agent, "A", "conversation.read") || w.hasPerm(ctx, q, agent, "A", "conversation.reply") {
			t.Error("forging the context without the door still yields exactly the live grant, no more")
		}
		if w.hasPerm(ctx, q, agent, "B", "conversation.read") {
			t.Error("the forged context must not extend to another instance")
		}
	})
}

func TestDomainAccessFollowsTheMappingAndWriteImpliesRead(t *testing.T) {
	w := newWorld(t)
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "ticket.read", "contact.classify", "media.read")
	w.grantKeys(g, "ticket.read", "contact.classify", "media.read")
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if !w.enter(ctx, q, agent, "A") {
			t.Fatal("enter")
		}
		for _, c := range []struct {
			domain, need string
			want         bool
		}{
			{"ticket", "read", true}, {"ticket", "write", false},
			{"contact", "write", true}, {"contact", "read", true}, // write implies read inside a domain
			{"media", "read", true}, {"media", "write", false},
			{"conversation", "read", false},                // never granted
			{"flow", "read", false}, {"ai", "read", false}, // reserved domains: nothing maps to them yet
		} {
			if got := w.domainAccess(ctx, q, agent, "A", c.domain, c.need); got != c.want {
				t.Errorf("%s/%s = %v, want %v", c.domain, c.need, got, c.want)
			}
		}
		if w.domainAccess(ctx, q, agent, "B", "ticket", "read") {
			t.Error("another instance")
		}
	})
}

// --- the middleware, real chain: authentication -> AuthorizationMiddleware -> handler

type servedProbe struct {
	mu     sync.Mutex
	called int
	tc     *tenancydomain.TenantContext
	perms  map[string]bool
}

func (w *world) servedChain(probe *servedProbe, asks ...string) http.Handler {
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))
	mw := tenancyadapters.AuthorizationMiddleware(w.app, authz)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/probe", mw(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		tc, err := tenancydomain.FromContext(r.Context())
		if err != nil {
			http.Error(rw, "no context", http.StatusInternalServerError)
			return
		}
		probe.mu.Lock()
		defer probe.mu.Unlock()
		probe.called++
		probe.tc = tc
		probe.perms = map[string]bool{}
		for _, k := range asks {
			ok, err := tenancyadapters.ActorHasPermission(r.Context(), platformdb.QuerierFromContext(r.Context(), w.app), tc.TenantID, tc.ActorID, k)
			if err != nil {
				http.Error(rw, err.Error(), http.StatusInternalServerError)
				return
			}
			probe.perms[k] = ok
		}
		rw.WriteHeader(http.StatusOK)
	})))
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if u := r.Header.Get("X-Test-User"); u != "" {
			id, _ := uuid.Parse(u)
			r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: id}))
		}
		mux.ServeHTTP(rw, r)
	})
}

func (w *world) serve(h http.Handler, user uuid.UUID, tenantKey, acting string) int {
	w.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+w.tenant[tenantKey].String()+"/probe", nil)
	req.Header.Set("X-Test-User", user.String())
	if acting != "" {
		req.Header.Set(tenancydomain.ActingAsHeader, acting)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestDelegatedServingThroughTheRealMiddleware(t *testing.T) {
	w := newWorld(t)
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })

	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read", "contact.read")
	w.grantKeys(g, "conversation.read", "contact.read")
	member := w.user("member")
	w.directMember(member, "A")
	both := w.hubAgent("both")
	w.directMember(both, "A")
	gb := w.grant(both, "A")
	w.grantKeys(gb, "conversation.read")
	hubHeader := "hub:" + w.hub.String()

	probe := &servedProbe{}
	h := w.servedChain(probe, "conversation.read", "contact.read", "contact.classify", "conversation.claim", "membership.read")
	called := func() int { probe.mu.Lock(); defer probe.mu.Unlock(); return probe.called }

	t.Run("a Hub agent with a grant is admitted with a delegated context that carries the whole chain", func(t *testing.T) {
		if code := w.serve(h, agent, "A", hubHeader); code != http.StatusOK {
			t.Fatalf("code = %d", code)
		}
		tc := probe.tc
		if tc.Source != tenancydomain.AccessSourceHubServe || tc.TenantID != w.tenant["A"] || tc.ActorID != agent ||
			tc.HubID == nil || *tc.HubID != w.hub || tc.ServiceContractID == nil || *tc.ServiceContractID != w.contract["A"] ||
			tc.EffectiveGrantID == nil || *tc.EffectiveGrantID != g {
			t.Errorf("context = %+v", tc)
		}
		if !reflect.DeepEqual(tc.Permissions, []string{"contact.read", "conversation.read"}) {
			t.Errorf("permissions snapshot = %v", tc.Permissions)
		}
		want := map[string]bool{"conversation.read": true, "contact.read": true, "contact.classify": false, "conversation.claim": false, "membership.read": false}
		if !reflect.DeepEqual(probe.perms, want) {
			t.Errorf("the database's answers inside the handler = %v", probe.perms)
		}
		if tc.MayManageAsTenant() {
			t.Error("a delegated serving context must not reach the channel-management services")
		}
	})

	t.Run("the tenant in the URL and the hub in the header are targets, never authority", func(t *testing.T) {
		before := called()
		if code := w.serve(h, agent, "B", hubHeader); code != http.StatusNotFound {
			t.Errorf("another instance: %d, want 404", code)
		}
		if code := w.serve(h, agent, "A", "hub:"+uuid.NewString()); code != http.StatusNotFound {
			t.Errorf("another hub: %d, want 404", code)
		}
		stranger := w.user("stranger")
		if code := w.serve(h, stranger, "A", hubHeader); code != http.StatusNotFound {
			t.Errorf("a stranger: %d, want 404", code)
		}
		if called() != before {
			t.Error("the handler ran for a request that must have been refused")
		}
	})

	t.Run("a malformed acting header is refused, never read as 'member'", func(t *testing.T) {
		before := called()
		for _, bad := range []string{"hub:", "hub:xyz", "Hub:" + w.hub.String(), "tenant:" + w.tenant["A"].String(), "admin", "member,hub:" + w.hub.String()} {
			if code := w.serve(h, member, "A", bad); code != http.StatusBadRequest {
				t.Errorf("%q: %d, want 400", bad, code)
			}
		}
		if called() != before {
			t.Error("the handler ran for a malformed context")
		}
	})

	t.Run("a member keeps exactly what they had: no header, or 'member', is the existing path", func(t *testing.T) {
		for _, acting := range []string{"", "member"} {
			if code := w.serve(h, member, "A", acting); code != http.StatusOK {
				t.Fatalf("member %q: %d", acting, code)
			}
			if probe.tc.Source != tenancydomain.AccessSourceDirect || probe.tc.ActingAs() != "member" {
				t.Errorf("member context = %+v", probe.tc)
			}
		}
		// and a delegated-only person gets nothing from the existing path
		before := called()
		if code := w.serve(h, agent, "A", ""); code == http.StatusOK {
			t.Errorf("a Hub agent with no membership must not pass the member path (code %d)", code)
		}
		if called() != before {
			t.Error("handler ran")
		}
	})

	t.Run("a membership contributes nothing to the delegated context", func(t *testing.T) {
		before := called()
		if code := w.serve(h, member, "A", hubHeader); code != http.StatusNotFound {
			t.Errorf("a member with no grant asking for the Hub context: %d, want 404", code)
		}
		if called() != before {
			t.Error("handler ran")
		}
	})

	t.Run("a person who is both gets the context they ask for, never the sum", func(t *testing.T) {
		if code := w.serve(h, both, "A", ""); code != http.StatusOK || probe.tc.Source != tenancydomain.AccessSourceDirect {
			t.Fatalf("member context: %d %+v", code, probe.tc)
		}
		if !probe.perms["conversation.claim"] || probe.perms["conversation.read"] {
			t.Errorf("member context permissions = %v", probe.perms)
		}
		if code := w.serve(h, both, "A", hubHeader); code != http.StatusOK || probe.tc.Source != tenancydomain.AccessSourceHubServe {
			t.Fatalf("hub context: %d %+v", code, probe.tc)
		}
		if !probe.perms["conversation.read"] || probe.perms["conversation.claim"] || probe.perms["membership.read"] {
			t.Errorf("hub context permissions = %v (the member's role must contribute nothing)", probe.perms)
		}
	})

	t.Run("revocation, suspension and a shrunk ceiling refuse the very next request", func(t *testing.T) {
		for _, c := range []struct{ name, brk, restore string }{
			{"grant revoked", `UPDATE effective_access_grants SET status='revoked' WHERE id=$1`, `UPDATE effective_access_grants SET status='active' WHERE id=$1`},
			{"instance suspended", `UPDATE tenants SET status='suspended' WHERE id=(SELECT tenant_id FROM effective_access_grants WHERE id=$1)`, `UPDATE tenants SET status='active' WHERE id=(SELECT tenant_id FROM effective_access_grants WHERE id=$1)`},
			{"ceiling shrunk to nothing", `UPDATE hub_tenant_service_contracts SET delegable_permissions='{}' WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`, `UPDATE hub_tenant_service_contracts SET delegable_permissions=ARRAY['conversation.read','contact.read'] WHERE id=(SELECT service_contract_id FROM effective_access_grants WHERE id=$1)`},
		} {
			if code := w.serve(h, agent, "A", hubHeader); code != http.StatusOK {
				t.Fatalf("%s: before: %d", c.name, code)
			}
			w.exec(c.brk, g)
			if code := w.serve(h, agent, "A", hubHeader); code != http.StatusNotFound {
				t.Errorf("%s: next request = %d, want 404", c.name, code)
			}
			w.exec(c.restore, g)
		}
	})

	t.Run("the whole path is off until the switch is turned on", func(t *testing.T) {
		tenancyadapters.EnableDelegatedServing(false)
		defer tenancyadapters.EnableDelegatedServing(true)
		before := called()
		if code := w.serve(h, agent, "A", hubHeader); code != http.StatusForbidden {
			t.Errorf("flag off: %d, want 403", code)
		}
		if code := w.serve(h, member, "A", ""); code != http.StatusOK {
			t.Errorf("flag off must not touch members: %d", code)
		}
		if called() != before+1 {
			t.Error("only the member's request may have reached the handler")
		}
	})
}

// --- the audit trail

func TestAuditNamesTheHubContractAndGrantOfADelegatedAction(t *testing.T) {
	w := newWorld(t)
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read")
	w.grantKeys(g, "conversation.read")
	member := w.user("member")
	w.directMember(member, "A")
	repo := auditadapters.NewPostgresAuditEventRepository(w.app)
	store := func(user uuid.UUID, tc *tenancydomain.TenantContext, meta map[string]any) uuid.UUID {
		id := uuid.New()
		w.inSession(user, func(ctx context.Context, q platformdb.Querier) {
			if tc.Source == tenancydomain.AccessSourceHubServe && !w.enter(ctx, q, user, "A") {
				t.Fatal("enter")
			}
			ev := &auditdomain.AuditEvent{ID: id, TenantID: w.tenant["A"], ActorID: user, Action: "contact.updated", ResourceType: "contact", ResourceID: uuid.New(),
				Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(), Metadata: meta, CreatedAt: time.Now().UTC()}
			w.must(repo.Store(tenancydomain.WithTenantContext(ctx, tc), ev))
		})
		return id
	}
	meta := func(id uuid.UUID) map[string]any {
		var raw []byte
		w.must(w.owner.QueryRow(w.ctx, `SELECT metadata FROM audit_events WHERE id = $1`, id).Scan(&raw))
		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	served, err := tenancydomain.NewHubServeTenantContext(w.tenant["A"], agent, w.hub, w.contract["A"], g, []string{"conversation.read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	m := meta(store(agent, served, map[string]any{"field": "alias", "via": "kept-if-the-caller-set-it-first"}))
	for k, want := range map[string]string{
		"acting_as": "hub:" + w.hub.String(), "hub_id": w.hub.String(), "contract_id": w.contract["A"].String(), "grant_id": g.String(),
		"via": "kept-if-the-caller-set-it-first", "field": "alias",
	} {
		if m[k] != want {
			t.Errorf("metadata[%s] = %v, want %v", k, m[k], want)
		}
	}
	m = meta(store(agent, served, nil))
	if m["via"] != "hub" {
		t.Errorf("a delegated action says via=hub: %v", m)
	}

	// the agent can write their own events (needed by ON CONFLICT) but never reads the instance's trail in general
	other := w.user("other-actor")
	w.auditEvent(&[]uuid.UUID{w.tenant["A"]}[0], &other, "contact.updated", `{}`, time.Now())
	var own, all int
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if !w.enter(ctx, q, agent, "A") {
			t.Fatal("enter")
		}
		w.must(q.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2`, w.tenant["A"], agent).Scan(&own))
		w.must(q.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id = $1`, w.tenant["A"]).Scan(&all))
	})
	if own == 0 || all != own {
		t.Errorf("a delegated agent reads only their own audit events: own=%d all=%d", own, all)
	}
	// and not outside the delegated context
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		var n int
		w.must(q.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id = $1`, w.tenant["A"]).Scan(&n))
		if n != 0 {
			t.Errorf("outside the delegated context nothing of the instance's audit trail is readable: %d", n)
		}
	})

	direct, _ := tenancydomain.NewTenantContext(w.tenant["A"], member, tenancydomain.AccessSourceDirect)
	m = meta(store(member, direct, map[string]any{"field": "alias"}))
	for _, k := range []string{"via", "acting_as", "hub_id", "contract_id", "grant_id"} {
		if _, has := m[k]; has {
			t.Errorf("a member's event must not carry %q: %v", k, m)
		}
	}
}

// While a delegated request is in progress EVERYTHING its authority rests on is held (instance, hub, contract, the person's hub membership
// and account, and their grant): a suspension, a revocation or a contract change WAITS for the request instead of landing in the middle of
// it. A hold-based test: the handler parks inside the middleware while each change is attempted; every one must be blocked, and every
// one must go through once the request is released.
func TestDelegatedServingHoldsEverythingTheAuthorizationRestsOn(t *testing.T) {
	w := newWorld(t)
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read")
	w.grantKeys(g, "conversation.read")

	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseOnce := func() { once.Do(func() { close(release) }) }
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))
	mw := tenancyadapters.AuthorizationMiddleware(w.app, authz)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/probe", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		id, _ := uuid.Parse(r.Header.Get("X-Test-User"))
		mw(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			rw.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(rw, r.WithContext(contextWithPrincipal(r, id)))
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// LIFO: registered after srv.Close, so the parked request is released BEFORE the server waits for it (no hang on a failed assertion)
	t.Cleanup(releaseOnce)

	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/tenants/"+w.tenant["A"].String()+"/probe", nil)
		req.Header.Set("X-Test-User", agent.String())
		req.Header.Set(tenancydomain.ActingAsHeader, "hub:"+w.hub.String())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- -1
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never got past the middleware")
	}
	type change struct {
		what, sql string
		args      []any
	}
	changes := []change{
		{"suspending the instance", `UPDATE tenants SET status = 'suspended' WHERE id = $1`, []any{w.tenant["A"]}},
		{"pausing the hub", `UPDATE service_hubs SET status = 'suspended' WHERE id = $1`, []any{w.hub}},
		{"suspending the contract", `UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE tenant_id = $1`, []any{w.tenant["A"]}},
		{"shrinking the contract's ceiling", `UPDATE hub_tenant_service_contracts SET delegable_permissions = '{}' WHERE tenant_id = $1`, []any{w.tenant["A"]}},
		{"removing the person from the hub", `DELETE FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, []any{w.hub, agent}},
		{"changing the person's role in the hub", `UPDATE hub_memberships SET role_id = $3 WHERE hub_id = $1 AND user_id = $2`, []any{w.hub, agent, w.roleHubAgent}},
		{"deactivating the account", `UPDATE users SET status = 'inactive' WHERE id = $1`, []any{agent}},
		{"revoking the grant", `UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, []any{g}},
		{"narrowing the grant", `UPDATE effective_access_grants SET permissions = '{}' WHERE id = $1`, []any{g}},
	}
	for _, c := range changes {
		ctx, cancel := context.WithTimeout(w.ctx, 600*time.Millisecond)
		_, err := w.owner.Exec(ctx, c.sql, c.args...)
		cancel()
		if err == nil {
			t.Errorf("%s did not wait for the delegated request in progress", c.what)
		}
	}
	releaseOnce()
	if code := <-done; code != http.StatusNoContent {
		t.Fatalf("the request: %d", code)
	}
	for _, c := range changes { // now nothing holds them
		w.exec(c.sql, c.args...)
	}
}

// "May X do P here?" is answered only about the person asking. Without the guard it would be an oracle on other people's roles and grants.
func TestPermissionQuestionsAreOnlyAnsweredAboutTheCaller(t *testing.T) {
	w := newWorld(t)
	member := w.user("member")
	w.directMember(member, "A") // tenant_agent: holds conversation.claim
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read")
	w.grantKeys(g, "conversation.read")
	asker := w.user("asker")
	// the asker is not the member: the member's permissions must not be disclosed
	w.inSession(asker, func(ctx context.Context, q platformdb.Querier) {
		if w.hasPerm(ctx, q, member, "A", "conversation.claim") {
			t.Error("a member's role permissions were disclosed to somebody else")
		}
		if w.hasPerm(ctx, q, agent, "A", "conversation.read") {
			t.Error("an agent's grant was disclosed to somebody else")
		}
	})
	// the member asking about themselves still gets the truth (the test is not vacuous)
	w.inSession(member, func(ctx context.Context, q platformdb.Querier) {
		if !w.hasPerm(ctx, q, member, "A", "conversation.claim") {
			t.Error("a member must be able to ask about themselves")
		}
	})
}
