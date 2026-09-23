package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

func teamSeedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("OMNIRA_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_DATABASE_URL not set; skipping team management tests")
	}
	return openTeamPool(t, dbURL)
}

func teamAppPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("OMNIRA_APP_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_APP_DATABASE_URL not set; skipping RLS-enforced team tests")
	}
	return openTeamPool(t, dbURL)
}

func openTeamPool(t *testing.T, dbURL string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type teamFixture struct {
	tenantID uuid.UUID
}

// seedTeamTenant cria um tenant e devolve seu id; membros entram com
// seedTeamMember. O tenant cascateia para memberships, então limpá-lo basta.
func seedTeamTenant(t *testing.T, pool *pgxpool.Pool) teamFixture {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO tenants (id, legal_name, isolation_profile, status)
		 VALUES ($1,$2,'shared_strong_isolation','active')`,
		id, "team-"+id.String()[:8]); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, id)
	})
	return teamFixture{tenantID: id}
}

func seedTeamMember(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, roleKey, status string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	userID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, external_subject, email, display_name, status) VALUES ($1,$2,$3,$4,'active')`,
		userID, userID.String(), userID.String()+"@example.com", "Member "+userID.String()[:8]); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})
	var roleID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL LIMIT 1`, roleKey).Scan(&roleID); err != nil {
		t.Fatalf("seed role %s: %v", roleKey, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,$5)`,
		uuid.New(), tenantID, userID, roleID, status); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	return userID
}

func membershipIDOf(t *testing.T, pool *pgxpool.Pool, tenantID, userID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM memberships WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&id); err != nil {
		t.Fatalf("find membership: %v", err)
	}
	return id
}

// asActor runs fn inside a real RLS session for actor, in tenantID's
// TenantContext — the same shape the middleware builds per request.
func asActor(t *testing.T, pool *pgxpool.Pool, tenantID, actor uuid.UUID, fn func(ctx context.Context) error) error {
	t.Helper()
	return platformdb.WithTenantSession(context.Background(), pool, actor, false, func(sessionCtx context.Context) error {
		tc, err := domain.NewTenantContext(tenantID, actor, domain.AccessSourceDirect)
		if err != nil {
			return err
		}
		return fn(domain.WithTenantContext(sessionCtx, tc))
	})
}

func newTeamHandler(app *pgxpool.Pool) *TeamHandler {
	return NewTeamHandler(app, auditadapters.NewPostgresAuditEventRepository(app), false)
}

func doRequest(t *testing.T, ctx context.Context, method string, fn http.HandlerFunc, pathValues map[string]string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(ctx, method, "/", bytes.NewReader(body))
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

// --- List: tenant scoping and permission ---

func TestListTeamIsScopedToTenantAndRequiresPermission(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	b := seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	seedTeamMember(t, seed, b.tenantID, "tenant_admin", "active")
	agentA := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")

	h := newTeamHandler(app)

	// admin A sees exactly A's members, never B's.
	if err := asActor(t, app, a.tenantID, adminA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodGet, h.ListTeam, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Items []TeamMember `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			return err
		}
		if len(body.Items) != 3 {
			t.Fatalf("expected 3 members of tenant A, got %d", len(body.Items))
		}
		return nil
	}); err != nil {
		t.Fatalf("admin A session: %v", err)
	}

	// tenant_agent lacks membership.read: denied.
	if err := asActor(t, app, a.tenantID, agentA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodGet, h.ListTeam, nil, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("agent list = %d, want 403", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("agent session: %v", err)
	}
}

// --- Role change: authorized ---

func TestUpdateMembershipRoleChangeByAdmin(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	target := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	membershipID := membershipIDOf(t, seed, a.tenantID, target)

	h := newTeamHandler(app)
	body, _ := json.Marshal(UpdateMembershipRequest{RoleKey: strPtr("tenant_supervisor")})

	if err := asActor(t, app, a.tenantID, adminA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, h.UpdateMembership,
			map[string]string{"membership_id": membershipID.String()}, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}

	var roleKey string
	if err := seed.QueryRow(context.Background(),
		`SELECT r.key FROM memberships m JOIN roles r ON r.id=m.role_id WHERE m.id=$1`, membershipID).Scan(&roleKey); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if roleKey != "tenant_supervisor" {
		t.Fatalf("role = %s, want tenant_supervisor", roleKey)
	}
}

// --- Cross-tenant: known UUID of another tenant is useless ---

func TestUpdateMembershipOfForeignTenantIs404(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	b := seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	targetB := seedTeamMember(t, seed, b.tenantID, "tenant_agent", "active")
	membershipB := membershipIDOf(t, seed, b.tenantID, targetB)

	h := newTeamHandler(app)
	body, _ := json.Marshal(UpdateMembershipRequest{RoleKey: strPtr("tenant_supervisor")})

	if err := asActor(t, app, a.tenantID, adminA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, h.UpdateMembership,
			map[string]string{"membership_id": membershipB.String()}, body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("cross-tenant update = %d, want 404", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}

	var roleKey string
	if err := seed.QueryRow(context.Background(),
		`SELECT r.key FROM memberships m JOIN roles r ON r.id=m.role_id WHERE m.id=$1`, membershipB).Scan(&roleKey); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if roleKey != "tenant_agent" {
		t.Fatalf("tenant B membership was mutated: role=%s", roleKey)
	}
}

// --- Agent cannot manage ---

func TestAgentCannotUpdateMembership(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	agentA := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	target := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	membershipID := membershipIDOf(t, seed, a.tenantID, target)

	h := newTeamHandler(app)
	body, _ := json.Marshal(UpdateMembershipRequest{RoleKey: strPtr("tenant_admin")})

	if err := asActor(t, app, a.tenantID, agentA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, h.UpdateMembership,
			map[string]string{"membership_id": membershipID.String()}, body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("agent update = %d, want 403", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

// --- Supervisor: read-only per the real permission matrix ---

func TestSupervisorCanReadButNotManage(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	supervisor := seedTeamMember(t, seed, a.tenantID, "tenant_supervisor", "active")
	target := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	membershipID := membershipIDOf(t, seed, a.tenantID, target)

	h := newTeamHandler(app)

	if err := asActor(t, app, a.tenantID, supervisor, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodGet, h.ListTeam, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("supervisor list = %d, want 200", rec.Code)
		}
		body, _ := json.Marshal(UpdateMembershipRequest{RoleKey: strPtr("tenant_admin")})
		updateRec := doRequest(t, ctx, http.MethodPatch, h.UpdateMembership,
			map[string]string{"membership_id": membershipID.String()}, body)
		if updateRec.Code != http.StatusForbidden {
			t.Fatalf("supervisor update = %d, want 403", updateRec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

// --- Cannot assign a global role from tenant administration ---

func TestCannotAssignGlobalRole(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	target := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	membershipID := membershipIDOf(t, seed, a.tenantID, target)

	h := newTeamHandler(app)
	body, _ := json.Marshal(UpdateMembershipRequest{RoleKey: strPtr("system_admin")})

	if err := asActor(t, app, a.tenantID, adminA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, h.UpdateMembership,
			map[string]string{"membership_id": membershipID.String()}, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("assign system_admin = %d, want 422", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}

	// ListAssignableRoles must not offer it either.
	if err := asActor(t, app, a.tenantID, adminA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodGet, h.ListAssignableRoles, nil, nil)
		var body struct {
			Items []RoleOption `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			return err
		}
		for _, r := range body.Items {
			if r.Key == "system_admin" || r.Key == "hub_admin" {
				t.Fatalf("global role %s offered to tenant administration", r.Key)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

// --- Role permission matrix (read-only) ---

func TestListRolesReturnsFixedPermissionMatrixReadOnly(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	supervisor := seedTeamMember(t, seed, a.tenantID, "tenant_supervisor", "active")
	agent := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	h := newTeamHandler(app)

	want := map[string][]string{
		"tenant_admin": {
			"agent.manage", "agent.read", "audit.read", "channel.manage", "conversation.claim", "conversation.manage",
			"dashboard.read", "membership.manage", "membership.read", "tenant.manage", "tenant.read", "ticket.read",
		},
		"tenant_supervisor": {"agent.manage", "agent.read", "audit.read", "conversation.claim", "conversation.manage", "dashboard.read", "membership.read", "tenant.read", "ticket.read"},
		"tenant_agent":      {"conversation.claim", "tenant.read"},
	}

	// A member with membership.read (supervisor) sees exactly the three fixed roles and their sets.
	if err := asActor(t, app, a.tenantID, supervisor, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodGet, h.ListAssignableRoles, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Items []RoleOption `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			return err
		}
		if len(body.Items) != len(want) {
			t.Fatalf("got %d roles, want %d", len(body.Items), len(want))
		}
		for _, r := range body.Items {
			exp, ok := want[r.Key]
			if !ok {
				t.Fatalf("unexpected role %s", r.Key)
			}
			if !reflect.DeepEqual(r.Permissions, exp) {
				t.Errorf("%s permissions = %v, want %v", r.Key, r.Permissions, exp)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}

	// An agent lacks membership.read: no matrix for them.
	if err := asActor(t, app, a.tenantID, agent, func(ctx context.Context) error {
		if rec := doRequest(t, ctx, http.MethodGet, h.ListAssignableRoles, nil, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("agent status = %d, want 403", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

// --- Agents list requires conversation.manage ---

func TestListAgentsRequiresConversationManage(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	supervisor := seedTeamMember(t, seed, a.tenantID, "tenant_supervisor", "active")
	agent := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")
	h := NewAgentsHandler(app)

	if err := asActor(t, app, a.tenantID, agent, func(ctx context.Context) error {
		if rec := doRequest(t, ctx, http.MethodGet, h.ListAgents, nil, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("agent status = %d, want 403", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
	if err := asActor(t, app, a.tenantID, supervisor, func(ctx context.Context) error {
		if rec := doRequest(t, ctx, http.MethodGet, h.ListAgents, nil, nil); rec.Code != http.StatusOK {
			t.Fatalf("supervisor status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

// --- Last admin invariant ---

func TestLastActiveAdminCannotBeDemoted(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	onlyAdmin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	membershipID := membershipIDOf(t, seed, a.tenantID, onlyAdmin)

	h := newTeamHandler(app)
	body, _ := json.Marshal(UpdateMembershipRequest{RoleKey: strPtr("tenant_agent")})

	if err := asActor(t, app, a.tenantID, onlyAdmin, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, h.UpdateMembership,
			map[string]string{"membership_id": membershipID.String()}, body)
		if rec.Code != http.StatusConflict {
			t.Fatalf("demote last admin = %d, want 409", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}

	var roleKey string
	if err := seed.QueryRow(context.Background(),
		`SELECT r.key FROM memberships m JOIN roles r ON r.id=m.role_id WHERE m.id=$1`, membershipID).Scan(&roleKey); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if roleKey != "tenant_admin" {
		t.Fatal("last admin was demoted despite the invariant")
	}
}

func TestLastActiveAdminCannotBeRevoked(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	onlyAdmin := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	membershipID := membershipIDOf(t, seed, a.tenantID, onlyAdmin)

	h := newTeamHandler(app)
	body, _ := json.Marshal(UpdateMembershipRequest{Status: strPtr("revoked")})

	if err := asActor(t, app, a.tenantID, onlyAdmin, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, h.UpdateMembership,
			map[string]string{"membership_id": membershipID.String()}, body)
		if rec.Code != http.StatusConflict {
			t.Fatalf("revoke last admin = %d, want 409", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

// With a second admin, one of them can be revoked and the tenant keeps an
// active administrator.
func TestSecondAdminCanBeRevoked(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	adminOne := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	adminTwo := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	membershipTwo := membershipIDOf(t, seed, a.tenantID, adminTwo)

	h := newTeamHandler(app)
	body, _ := json.Marshal(UpdateMembershipRequest{Status: strPtr("revoked")})

	if err := asActor(t, app, a.tenantID, adminOne, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodPatch, h.UpdateMembership,
			map[string]string{"membership_id": membershipTwo.String()}, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("revoke second admin = %d %s, want 200", rec.Code, rec.Body.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

// --- Revocation takes effect immediately ---

func TestRevokedMembershipLosesAccessImmediately(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	revoked := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "revoked")

	h := newTeamHandler(app)
	if err := asActor(t, app, a.tenantID, revoked, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodGet, h.ListTeam, nil, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("revoked membership list = %d, want 403", rec.Code)
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

// --- Effective permissions ---

func TestMyAccessReflectsRolePermissions(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	agentA := seedTeamMember(t, seed, a.tenantID, "tenant_agent", "active")

	h := newTeamHandler(app)

	if err := asActor(t, app, a.tenantID, adminA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodGet, h.MyAccess, nil, nil)
		var body struct {
			RoleKey     string   `json:"role_key"`
			Permissions []string `json:"permissions"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			return err
		}
		if body.RoleKey != "tenant_admin" {
			t.Fatalf("role_key = %s", body.RoleKey)
		}
		if !contains(body.Permissions, "membership.manage") {
			t.Fatalf("admin permissions missing membership.manage: %v", body.Permissions)
		}
		return nil
	}); err != nil {
		t.Fatalf("admin session: %v", err)
	}

	if err := asActor(t, app, a.tenantID, agentA, func(ctx context.Context) error {
		rec := doRequest(t, ctx, http.MethodGet, h.MyAccess, nil, nil)
		var body struct {
			Permissions []string `json:"permissions"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			return err
		}
		if contains(body.Permissions, "membership.manage") {
			t.Fatalf("agent must not have membership.manage: %v", body.Permissions)
		}
		return nil
	}); err != nil {
		t.Fatalf("agent session: %v", err)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func strPtr(s string) *string { return &s }
