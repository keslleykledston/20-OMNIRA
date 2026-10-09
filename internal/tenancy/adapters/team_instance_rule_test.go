package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ADR-0039 (Codex): the single-instance rule also covers REACTIVATING a membership, and the two "ask then write" checks of the
// team screen (is this person already elsewhere? is this the last administrator?) are atomic, not merely correct in sequence.

func roleID(t *testing.T, seed *pgxpool.Pool, key string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := seed.QueryRow(context.Background(), `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func addTeamMembership(t *testing.T, seed *pgxpool.Pool, tenant, user uuid.UUID, role, status string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := seed.Exec(context.Background(), `INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,$5)`, id, tenant, user, roleID(t, seed, role), status); err != nil {
		t.Fatal(err)
	}
	return id
}

func patchStatus(t *testing.T, app *pgxpool.Pool, tenant, actor, membership uuid.UUID, status string) int {
	t.Helper()
	body, _ := json.Marshal(UpdateMembershipRequest{Status: strPtr(status)})
	var code int
	if err := asActor(t, app, tenant, actor, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, newTeamHandler(app).UpdateMembership, map[string]string{"membership_id": membership.String()}, body)
		code = rec.Code
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
	return code
}

func activeMemberships(t *testing.T, seed *pgxpool.Pool, user uuid.UUID) int {
	t.Helper()
	var n int
	if err := seed.QueryRow(context.Background(), `SELECT count(*) FROM memberships WHERE user_id=$1 AND status='active'`, user).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTeamReactivationOfSomeoneWhoWorksElsewhereIsRefused(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	person := seedTeamMember(t, seed, b.tenantID, "tenant_agent", "active") // works in B
	inA := addTeamMembership(t, seed, a.tenantID, person, "tenant_agent", "inactive")

	if code := patchStatus(t, app, a.tenantID, adminA, inA, "active"); code != http.StatusConflict {
		t.Fatalf("reactivating someone who works in another instance = %d, want 409", code)
	}
	if n := activeMemberships(t, seed, person); n != 1 {
		t.Fatalf("the refused reactivation still changed something: %d active memberships", n)
	}
	// a role change on an ACTIVE membership is not a reactivation and is untouched by the rule
	other := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	body, _ := json.Marshal(UpdateMembershipRequest{RoleKey: strPtr("tenant_supervisor")})
	if err := asActor(t, app, a.tenantID, adminA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, newTeamHandler(app).UpdateMembership, map[string]string{"membership_id": membershipIDOf(t, seed, a.tenantID, other).String()}, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("a role change on an active member: %d", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// once they stop working in B, the company may take them back
	if _, err := seed.Exec(context.Background(), `UPDATE memberships SET status='inactive' WHERE tenant_id=$1 AND user_id=$2`, b.tenantID, person); err != nil {
		t.Fatal(err)
	}
	if code := patchStatus(t, app, a.tenantID, adminA, inA, "active"); code != http.StatusOK {
		t.Fatalf("reactivating someone who works nowhere else = %d, want 200", code)
	}
}

func TestTeamReactivationOfAHubAgentIsRefused(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	person := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "inactive") // inactive here, works nowhere else...
	hub := uuid.New()
	if _, err := seed.Exec(context.Background(), `INSERT INTO service_hubs (id, name) VALUES ($1, 'H')`, hub); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM service_hubs WHERE id=$1`, hub) })
	if _, err := seed.Exec(context.Background(), `INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1,$2,$3)`, hub, person, roleID(t, seed, "hub_agent")); err != nil {
		t.Fatal(err)
	}
	// ...but being an agent of a hub counts as working in other instances
	if code := patchStatus(t, app, a.tenantID, adminA, membershipIDOf(t, seed, a.tenantID, person), "active"); code != http.StatusConflict {
		t.Fatalf("reactivating a hub agent = %d, want 409", code)
	}
}

func TestTwoReactivationsAtOnceNeverPutAPersonInTwoInstances(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	adminB := seedTeamMember(t, seed, b.tenantID, "tenant_admin", "active")
	person := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "inactive")
	inA := membershipIDOf(t, seed, a.tenantID, person)
	inB := addTeamMembership(t, seed, b.tenantID, person, "tenant_agent", "inactive")

	for i := 0; i < 15; i++ {
		if _, err := seed.Exec(context.Background(), `UPDATE memberships SET status='inactive' WHERE user_id=$1`, person); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		for k, c := range []struct{ tenant, admin, membership uuid.UUID }{{a.tenantID, adminA, inA}, {b.tenantID, adminB, inB}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[k] = patchStatus(t, app, c.tenant, c.admin, c.membership, "active")
			}()
		}
		close(start)
		wg.Wait()
		if n := activeMemberships(t, seed, person); n != 1 {
			t.Fatalf("round %d: %d active memberships after two simultaneous reactivations (codes %v), want exactly 1", i, n, codes)
		}
	}
}

func TestTwoAdminDemotionsAtOnceNeverEmptyTheCompany(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	x := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	y := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	mx, my := membershipIDOf(t, seed, a.tenantID, x), membershipIDOf(t, seed, a.tenantID, y)

	for i := 0; i < 15; i++ {
		if _, err := seed.Exec(context.Background(), `UPDATE memberships SET status='active', role_id=$2 WHERE tenant_id=$1 AND user_id IN ($3,$4)`, a.tenantID, roleID(t, seed, "tenant_admin"), x, y); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		// each administrator demotes the OTHER one at the same instant
		for k, c := range []struct{ actor, target uuid.UUID }{{x, my}, {y, mx}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[k] = patchStatus(t, app, a.tenantID, c.actor, c.target, "inactive")
			}()
		}
		close(start)
		wg.Wait()
		var admins int
		if err := seed.QueryRow(context.Background(), `SELECT count(*) FROM memberships m JOIN roles r ON r.id=m.role_id WHERE m.tenant_id=$1 AND m.status='active' AND r.key='tenant_admin'`, a.tenantID).Scan(&admins); err != nil {
			t.Fatal(err)
		}
		if admins < 1 {
			t.Fatalf("round %d: the company was left with no administrator (codes %v)", i, codes)
		}
	}
}
