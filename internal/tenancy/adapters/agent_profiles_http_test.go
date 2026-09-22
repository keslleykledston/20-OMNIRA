package adapters

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
)

// Real Postgres/RLS regression: known foreign UUIDs must neither read nor
// mutate an operational profile or its queue membership.
func TestAgentProfilesTenantIsolationAndOperationalMutations(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	supervisorA := seedTeamMember(t, seed, a.tenantID, "tenant_supervisor", "active")
	agentA := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	agentB := seedTeamMember(t, seed, b.tenantID, "tenant_agent", "active")
	membershipA, membershipB := membershipIDOf(t, seed, a.tenantID, agentA), membershipIDOf(t, seed, b.tenantID, agentB)
	profileA, profileB, queueA, queueB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, item := range []struct{ profile, tenant, membership uuid.UUID }{{profileA, a.tenantID, membershipA}, {profileB, b.tenantID, membershipB}} {
		if _, err := seed.Exec(context.Background(), `INSERT INTO agent_profiles(id,tenant_id,membership_id,status) VALUES($1,$2,$3,'active')`, item.profile, item.tenant, item.membership); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		id, tenant uuid.UUID
		name       string
	}{{queueA, a.tenantID, "A"}, {queueB, b.tenantID, "B"}} {
		if _, err := seed.Exec(context.Background(), `INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,$3,'manual')`, item.id, item.tenant, item.name); err != nil {
			t.Fatal(err)
		}
	}
	h := NewAgentProfilesHandler(app, auditadapters.NewPostgresAuditEventRepository(app))
	call := func(actor, tenant uuid.UUID, method string, fn http.HandlerFunc, values map[string]string, body string) int {
		var code int
		err := asActor(t, app, tenant, actor, func(ctx context.Context) error {
			rec := doRequest(t, ctx, method, fn, values, []byte(body))
			code = rec.Code
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	if got := call(adminA, a.tenantID, http.MethodGet, h.List, nil, ""); got != http.StatusOK {
		t.Fatalf("admin list=%d", got)
	}
	if got := call(supervisorA, a.tenantID, http.MethodGet, h.Get, map[string]string{"agent_profile_id": profileA.String()}, ""); got != http.StatusOK {
		t.Fatalf("supervisor get=%d", got)
	}
	if got := call(agentA, a.tenantID, http.MethodGet, h.List, nil, ""); got != http.StatusForbidden {
		t.Fatalf("agent list=%d", got)
	}
	if got := call(adminA, a.tenantID, http.MethodGet, h.Get, map[string]string{"agent_profile_id": profileB.String()}, ""); got != http.StatusNotFound {
		t.Fatalf("foreign known profile read=%d", got)
	}
	if got := call(adminA, a.tenantID, http.MethodPost, h.AddQueue, map[string]string{"agent_profile_id": profileA.String()}, `{"queue_id":"`+queueA.String()+`","available":true,"capacity":2}`); got != http.StatusCreated {
		t.Fatalf("add queue=%d", got)
	}
	var memberA uuid.UUID
	if err := seed.QueryRow(context.Background(), `SELECT id FROM queue_members WHERE tenant_id=$1 AND queue_id=$2 AND user_id=$3`, a.tenantID, queueA, agentA).Scan(&memberA); err != nil {
		t.Fatal(err)
	}
	if got := call(adminA, a.tenantID, http.MethodPatch, h.UpdateQueue, map[string]string{"agent_profile_id": profileB.String(), "queue_member_id": memberA.String()}, `{"available":false,"capacity":1}`); got != http.StatusNotFound {
		t.Fatalf("foreign profile update=%d", got)
	}
	if got := call(adminA, a.tenantID, http.MethodPatch, h.UpdateQueue, map[string]string{"agent_profile_id": profileA.String(), "queue_member_id": memberA.String()}, `{"available":false,"capacity":3}`); got != http.StatusNoContent {
		t.Fatalf("update queue=%d", got)
	}
	if got := call(adminA, a.tenantID, http.MethodDelete, h.RemoveQueue, map[string]string{"agent_profile_id": profileB.String(), "queue_member_id": memberA.String()}, ""); got != http.StatusNotFound {
		t.Fatalf("foreign remove=%d", got)
	}
	if got := call(supervisorA, a.tenantID, http.MethodDelete, h.RemoveQueue, map[string]string{"agent_profile_id": profileA.String(), "queue_member_id": memberA.String()}, ""); got != http.StatusNoContent {
		t.Fatalf("supervisor remove=%d", got)
	}
	if got := call(adminA, a.tenantID, http.MethodPost, h.AddQueue, map[string]string{"agent_profile_id": profileA.String()}, `{"queue_id":"`+queueB.String()+`"}`); got != http.StatusNotFound {
		t.Fatalf("foreign queue assign=%d", got)
	}
	var rls, force, super, bypass bool
	if err := app.QueryRow(context.Background(), `SELECT relrowsecurity,relforcerowsecurity FROM pg_class WHERE oid='agent_profiles'::regclass`).Scan(&rls, &force); err != nil || !rls || !force {
		t.Fatalf("agent_profiles RLS/FORCE=%v/%v err=%v", rls, force, err)
	}
	if err := app.QueryRow(context.Background(), `SELECT rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&super, &bypass); err != nil || super || bypass {
		t.Fatalf("runtime role super/bypass=%v/%v err=%v", super, bypass, err)
	}
}
