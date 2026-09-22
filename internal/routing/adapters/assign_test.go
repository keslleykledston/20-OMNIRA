package adapters_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	routingapplication "github.com/omnira/omnira/internal/routing/application"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

type assignEnv struct {
	t    *testing.T
	seed *pgxpool.Pool
	app  *pgxpool.Pool
	mux  *http.ServeMux

	tenantA, tenantB                  uuid.UUID
	agent1, agent2, supervisor, admin uuid.UUID
	viewer, revoked, outsiderB        uuid.UUID
}

func (e *assignEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func newAssignEnv(t *testing.T) *assignEnv {
	t.Helper()
	seedURL, appURL := os.Getenv("OMNIRA_DATABASE_URL"), os.Getenv("OMNIRA_APP_DATABASE_URL")
	if seedURL == "" || appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	e := &assignEnv{t: t, seed: seed, app: app, tenantA: uuid.New(), tenantB: uuid.New()}
	users := []*uuid.UUID{&e.agent1, &e.agent2, &e.supervisor, &e.admin, &e.viewer, &e.revoked, &e.outsiderB}
	for _, u := range users {
		*u = uuid.New()
		e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, *u, *u, u.String()+"@invalid")
	}
	for _, tn := range []uuid.UUID{e.tenantA, e.tenantB} {
		e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id IN ($1,$2)`, e.tenantA, e.tenantB)
		for _, u := range users {
			_, _ = seed.Exec(bg, `DELETE FROM users WHERE id=$1`, *u)
		}
		_, _ = seed.Exec(bg, `DELETE FROM roles WHERE tenant_id IN ($1,$2)`, e.tenantA, e.tenantB)
		seed.Close()
		app.Close()
	})
	role := func(key string) (id uuid.UUID) {
		if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return
	}
	viewerRole := uuid.New() // tenant-scoped custom role WITHOUT any conversation permission
	e.exec(`INSERT INTO roles(id,tenant_id,key,name) VALUES($1,$2,'viewer_only','Viewer')`, viewerRole, e.tenantA)
	e.exec(`INSERT INTO role_permissions(role_id,permission_key) VALUES($1,'tenant.read')`, viewerRole)
	member := func(tn, u, r uuid.UUID, status string) {
		e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,$4)`, tn, u, r, status)
	}
	member(e.tenantA, e.agent1, role("tenant_agent"), "active")
	member(e.tenantA, e.agent2, role("tenant_agent"), "active")
	member(e.tenantA, e.supervisor, role("tenant_supervisor"), "active")
	member(e.tenantA, e.admin, role("tenant_admin"), "active")
	member(e.tenantA, e.viewer, viewerRole, "active")
	member(e.tenantA, e.revoked, role("tenant_agent"), "revoked")
	member(e.tenantB, e.outsiderB, role("tenant_admin"), "active")
	// IAM4 profiles are an explicit operational enablement, independent of role.
	e.exec(`INSERT INTO agent_profiles(tenant_id,membership_id,status)
		SELECT tenant_id,id,'active' FROM memberships WHERE tenant_id=$1 AND user_id IN ($2,$3,$4,$5)`, e.tenantA, e.agent1, e.agent2, e.supervisor, e.admin)

	authz := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	mw := tenancyadapters.AuthorizationMiddleware(app, authz)
	h := routingadapters.NewAssignHandler(routingapplication.NewAssigner(
		routingadapters.NewPostgresConversationAssigner(app),
		routingadapters.NewAuditRecorder(auditadapters.NewPostgresAuditEventRepository(app)),
	))
	e.mux = http.NewServeMux()
	e.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/assign", mw(http.HandlerFunc(h.Assign)))
	e.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/unassign", mw(http.HandlerFunc(h.Unassign)))
	return e
}

func (e *assignEnv) conversation(tenant uuid.UUID) uuid.UUID {
	e.t.Helper()
	contact, conv := uuid.New(), uuid.New()
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'C',$3)`, contact, tenant, fmt.Sprintf("+5511%09d", contact.ID()%1000000000))
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conv, tenant, contact)
	return conv
}

type result struct {
	code       int
	assignedTo string
	changed    bool
	body       string
}

func (e *assignEnv) call(user, tenant, conv uuid.UUID, action, body string) result {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tenant.String()+"/inbox/conversations/"+conv.String()+"/"+action, strings.NewReader(body))
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	r := result{code: rec.Code, body: strings.TrimSpace(rec.Body.String())}
	var out struct {
		AssignedTo *string `json:"assigned_to_user_id"`
		Changed    bool    `json:"changed"`
	}
	if rec.Code == 200 && json.Unmarshal(rec.Body.Bytes(), &out) == nil {
		r.changed = out.Changed
		if out.AssignedTo != nil {
			r.assignedTo = *out.AssignedTo
		}
	}
	return r
}

func (e *assignEnv) owner(conv uuid.UUID) string {
	var o *uuid.UUID
	if err := e.seed.QueryRow(context.Background(), `SELECT assigned_to_user_id FROM conversations WHERE id=$1`, conv).Scan(&o); err != nil {
		e.t.Fatal(err)
	}
	if o == nil {
		return ""
	}
	return o.String()
}

func (e *assignEnv) count(sql string, args ...any) (n int) {
	if err := e.seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return
}

func want(t *testing.T, got result, code int, msg string) {
	t.Helper()
	if got.code != code {
		t.Fatalf("%s: code=%d body=%q, want %d", msg, got.code, got.body, code)
	}
}

func TestAssignClaimHistoryAuditAndIdempotency(t *testing.T) {
	e := newAssignEnv(t)
	conv := e.conversation(e.tenantA)
	r := e.call(e.agent1, e.tenantA, conv, "assign", "")
	want(t, r, 200, "agent claims free conversation")
	if !r.changed || r.assignedTo != e.agent1.String() || e.owner(conv) != e.agent1.String() {
		t.Fatalf("claim result %+v owner=%s", r, e.owner(conv))
	}
	if n := e.count(`SELECT count(*) FROM assignment_events WHERE conversation_id=$1 AND from_user_id IS NULL AND to_user_id=$2 AND changed_by=$2 AND reason='manual_claim'`, conv, e.agent1); n != 1 {
		t.Fatalf("history rows=%d", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='conversation.assigned' AND resource_type='conversation' AND resource_id=$2 AND actor_id=$3 AND outcome='success'`, e.tenantA, conv.String(), e.agent1); n != 1 {
		t.Fatalf("audit rows=%d", n)
	}
	// Documented behavior: repeating an operation that already holds is a 200 no-op (no history, no audit).
	r = e.call(e.agent1, e.tenantA, conv, "assign", "")
	want(t, r, 200, "repeat claim")
	if r.changed {
		t.Fatal("repeat claim must report changed=false")
	}
	if n := e.count(`SELECT count(*) FROM assignment_events WHERE conversation_id=$1`, conv); n != 1 {
		t.Fatalf("idempotent repeat wrote history: %d", n)
	}
	// Another agent cannot take an owned conversation: 409, ownership untouched.
	want(t, e.call(e.agent2, e.tenantA, conv, "assign", ""), 409, "second agent claim")
	if e.owner(conv) != e.agent1.String() {
		t.Fatal("conflicting claim changed the owner")
	}
}

func TestAssignConcurrentClaimHasExactlyOneWinner(t *testing.T) {
	e := newAssignEnv(t)
	for round := 0; round < 8; round++ {
		conv := e.conversation(e.tenantA)
		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		for i, u := range []uuid.UUID{e.agent1, e.agent2} {
			wg.Add(1)
			go func(i int, u uuid.UUID) {
				defer wg.Done()
				<-start
				codes[i] = e.call(u, e.tenantA, conv, "assign", "").code
			}(i, u)
		}
		close(start)
		wg.Wait()
		if !((codes[0] == 200 && codes[1] == 409) || (codes[0] == 409 && codes[1] == 200)) {
			t.Fatalf("round %d: codes=%v, want exactly one 200 and one 409", round, codes)
		}
		if n := e.count(`SELECT count(*) FROM assignment_events WHERE conversation_id=$1`, conv); n != 1 {
			t.Fatalf("round %d: history rows=%d", round, n)
		}
		if n := e.count(`SELECT count(*) FROM audit_events WHERE resource_id=$1 AND action='conversation.assigned'`, conv.String()); n != 1 {
			t.Fatalf("round %d: audit rows=%d", round, n)
		}
	}
}

func TestUnassignAndRBAC(t *testing.T) {
	e := newAssignEnv(t)
	conv := e.conversation(e.tenantA)
	want(t, e.call(e.agent1, e.tenantA, conv, "assign", ""), 200, "claim")

	// Another agent cannot release someone else's conversation; supervisor can.
	want(t, e.call(e.agent2, e.tenantA, conv, "unassign", ""), 403, "agent releases other's")
	if e.owner(conv) != e.agent1.String() {
		t.Fatal("forbidden unassign changed owner")
	}
	want(t, e.call(e.supervisor, e.tenantA, conv, "unassign", ""), 200, "supervisor unassign")
	if e.owner(conv) != "" {
		t.Fatal("supervisor unassign did not release")
	}
	if n := e.count(`SELECT count(*) FROM assignment_events WHERE conversation_id=$1 AND to_user_id IS NULL AND from_user_id=$2 AND changed_by=$3 AND reason='manual_unassign'`, conv, e.agent1, e.supervisor); n != 1 {
		t.Fatalf("unassign history=%d", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE resource_id=$1 AND action='conversation.unassigned' AND actor_id=$2`, conv.String(), e.supervisor); n != 1 {
		t.Fatalf("unassign audit=%d", n)
	}
	// Unassigning a free conversation is a documented no-op.
	r := e.call(e.supervisor, e.tenantA, conv, "unassign", "")
	want(t, r, 200, "unassign free")
	if r.changed {
		t.Fatal("no-op unassign reported changed")
	}
	// Owner releases their own.
	want(t, e.call(e.agent2, e.tenantA, conv, "assign", ""), 200, "agent2 claim")
	want(t, e.call(e.agent2, e.tenantA, conv, "unassign", ""), 200, "owner release")
	if n := e.count(`SELECT count(*) FROM assignment_events WHERE conversation_id=$1 AND reason='manual_release'`, conv); n != 1 {
		t.Fatalf("release history=%d", n)
	}

	// Supervisor/admin assign to another agent (reassignment keeps history).
	want(t, e.call(e.agent1, e.tenantA, conv, "assign", ""), 200, "agent1 claim")
	body := `{"assignee_user_id":"` + e.agent2.String() + `"}`
	want(t, e.call(e.agent1, e.tenantA, conv, "assign", body), 403, "agent assigns to another")
	r = e.call(e.supervisor, e.tenantA, conv, "assign", body)
	want(t, r, 200, "supervisor reassigns")
	if e.owner(conv) != e.agent2.String() {
		t.Fatal("reassign did not apply")
	}
	if n := e.count(`SELECT count(*) FROM assignment_events WHERE conversation_id=$1 AND from_user_id=$2 AND to_user_id=$3 AND changed_by=$4 AND reason='manual_assign'`, conv, e.agent1, e.agent2, e.supervisor); n != 1 {
		t.Fatalf("reassign history=%d", n)
	}
	want(t, e.call(e.admin, e.tenantA, conv, "assign", `{"assignee_user_id":"`+e.agent1.String()+`"}`), 200, "admin reassigns")

	// Ineligible targets: no claim permission, revoked, other tenant, unknown.
	for name, target := range map[string]uuid.UUID{"viewer": e.viewer, "revoked": e.revoked, "tenantB user": e.outsiderB, "unknown": uuid.New()} {
		want(t, e.call(e.admin, e.tenantA, conv, "assign", `{"assignee_user_id":"`+target.String()+`"}`), 422, "assign to "+name)
	}
	if e.owner(conv) != e.agent1.String() {
		t.Fatal("rejected assignments changed the owner")
	}

	// Role without conversation permissions and revoked membership are denied.
	free := e.conversation(e.tenantA)
	want(t, e.call(e.viewer, e.tenantA, free, "assign", ""), 403, "viewer claim")
	want(t, e.call(e.viewer, e.tenantA, conv, "unassign", ""), 403, "viewer unassign")
	// Revoked membership: the tenant is invisible to the user (middleware answers 404, no enumeration).
	want(t, e.call(e.revoked, e.tenantA, free, "assign", ""), 404, "revoked membership claim")
	want(t, e.call(e.revoked, e.tenantA, conv, "unassign", ""), 404, "revoked membership unassign")
	if e.owner(free) != "" {
		t.Fatal("denied claim assigned the conversation")
	}
}

func TestAssignTenantIsolation(t *testing.T) {
	e := newAssignEnv(t)
	convA, convB := e.conversation(e.tenantA), e.conversation(e.tenantB)
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, convB, e.outsiderB)

	want(t, e.call(e.agent1, e.tenantA, convA, "assign", ""), 200, "tenant A assigns A")
	// Knowing tenant B's conversation UUID is useless from tenant A's path: not found, nothing changes.
	want(t, e.call(e.agent1, e.tenantA, convB, "assign", ""), 404, "tenant A claim on B's uuid via A path")
	want(t, e.call(e.supervisor, e.tenantA, convB, "unassign", ""), 404, "tenant A unassign B's uuid via A path")
	want(t, e.call(e.admin, e.tenantA, convB, "assign", `{"assignee_user_id":"`+e.agent2.String()+`"}`), 404, "tenant A assign B's uuid to A agent")
	// Using tenant B's path without membership: tenant is invisible (404), never served.
	want(t, e.call(e.agent1, e.tenantB, convB, "assign", ""), 404, "tenant A user on B path")
	want(t, e.call(e.supervisor, e.tenantB, convB, "unassign", ""), 404, "tenant A supervisor unassign on B path")
	// Forged tenant_id/user in the body is ignored: identity comes from the principal.
	forged := `{"tenant_id":"` + e.tenantB.String() + `","user_id":"` + e.outsiderB.String() + `"}`
	want(t, e.call(e.agent2, e.tenantA, e.conversation(e.tenantA), "assign", forged), 200, "forged body ignored")
	// Unknown conversation is indistinguishable from another tenant's (no enumeration).
	a := e.call(e.agent1, e.tenantA, uuid.New(), "assign", "")
	b := e.call(e.agent1, e.tenantA, convB, "assign", "")
	if a.code != 404 || b.code != 404 || a.body != b.body {
		t.Fatalf("enumeration oracle: %+v vs %+v", a, b)
	}
	if e.owner(convB) != e.outsiderB.String() {
		t.Fatal("cross-tenant conversation was modified")
	}
	if n := e.count(`SELECT count(*) FROM assignment_events WHERE conversation_id=$1`, convB); n != 0 {
		t.Fatalf("cross-tenant history rows=%d", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE resource_id=$1`, convB.String()); n != 0 {
		t.Fatalf("cross-tenant audit rows=%d", n)
	}
	want(t, e.call(e.outsiderB, e.tenantB, convB, "unassign", ""), 200, "tenant B admin can unassign own")

	// RLS remains a second barrier below the application checks.
	err := platformdb.WithTenantSession(context.Background(), e.app, e.agent1, false, func(sc context.Context) error {
		tag, err := platformdb.QuerierFromContext(sc, e.app).Exec(sc, `UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, convB, e.agent1)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 0 {
			t.Fatalf("RLS let tenant A's user update tenant B's conversation (%d rows)", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOperationalProfileAndMembershipRevocationImmediatelyBlockRouting(t *testing.T) {
	e := newAssignEnv(t)
	queue := uuid.New()
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,'IAM4 routing','manual')`, queue, e.tenantA)
	e.exec(`INSERT INTO queue_members(tenant_id,queue_id,user_id,available,capacity) VALUES($1,$2,$3,true,2)`, e.tenantA, queue, e.agent1)

	conversation := e.conversation(e.tenantA)
	e.exec(`UPDATE conversations SET queue_id=$1 WHERE id=$2`, queue, conversation)
	e.exec(`UPDATE agent_profiles SET status='disabled' WHERE tenant_id=$1 AND membership_id=(SELECT id FROM memberships WHERE tenant_id=$1 AND user_id=$2)`, e.tenantA, e.agent1)
	want(t, e.call(e.admin, e.tenantA, conversation, "assign", `{"assignee_user_id":"`+e.agent1.String()+`"}`), 422, "disabled profile is ineligible")

	e.exec(`UPDATE agent_profiles SET status='active' WHERE tenant_id=$1 AND membership_id=(SELECT id FROM memberships WHERE tenant_id=$1 AND user_id=$2)`, e.tenantA, e.agent1)
	e.exec(`UPDATE memberships SET status='revoked' WHERE tenant_id=$1 AND user_id=$2`, e.tenantA, e.agent1)
	want(t, e.call(e.admin, e.tenantA, conversation, "assign", `{"assignee_user_id":"`+e.agent1.String()+`"}`), 422, "revoked membership is immediately ineligible")
}
