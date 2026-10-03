package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/tenancy/application"
)

// GET /api/v1/tenants lists the tenants the signed-in user can switch to. It is
// the data behind the tenant selector, so the rules that matter are who is NOT
// in the list: other users' tenants, revoked memberships, inactive tenants.

func newMyTenantsHandler(app *pgxpool.Pool) *TenantAPIHandler {
	return NewTenantAPIHandler(
		application.NewTenantService(NewPostgresTenantRepository(app)),
		application.NewMembershipService(NewPostgresMembershipRepository(app), NewPostgresRoleRepository(app)),
	)
}

func listMyTenants(t *testing.T, app *pgxpool.Pool, user uuid.UUID) (int, []TenantResponse, string) {
	t.Helper()
	var rec *httptest.ResponseRecorder
	// Same shape as UserSessionMiddleware: a user RLS session and the Principal, no tenant.
	if err := platformdb.WithTenantSession(context.Background(), app, user, false, func(ctx context.Context) error {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil).WithContext(authn.WithPrincipal(ctx, &authn.Principal{UserID: user}))
		rec = httptest.NewRecorder()
		newMyTenantsHandler(app).ListMyTenants(rec, req)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var out []TenantResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
	}
	return rec.Code, out, rec.Body.String()
}

func addMembership(t *testing.T, pool *pgxpool.Pool, tenantID, userID uuid.UUID, roleKey, status string) {
	t.Helper()
	var roleID uuid.UUID
	if err := pool.QueryRow(context.Background(), `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL LIMIT 1`, roleKey).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,$5)`,
		uuid.New(), tenantID, userID, roleID, status); err != nil {
		t.Fatal(err)
	}
}

func nameTenant(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, legal string, trade *string, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE tenants SET legal_name=$2, trade_name=$3, status=$4 WHERE id=$1`, id, legal, trade, status); err != nil {
		t.Fatal(err)
	}
}

func TestListMyTenantsReturnsOnlyTheUsersUsableTenantsInStableOrder(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	suffix := uuid.New().String()[:6]
	zeta, alfa, revokedT, inactiveT, strangerT := seedTeamTenant(t, seed), seedTeamTenant(t, seed), seedTeamTenant(t, seed), seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	trade := "Alfa Telecom " + suffix
	nameTenant(t, seed, zeta.tenantID, "Zeta Ltda "+suffix, nil, "active")
	nameTenant(t, seed, alfa.tenantID, "Beta Ltda "+suffix, &trade, "active") // trade name wins over legal name
	nameTenant(t, seed, revokedT.tenantID, "Revogado "+suffix, nil, "active")
	nameTenant(t, seed, inactiveT.tenantID, "Inativo "+suffix, nil, "inactive")
	nameTenant(t, seed, strangerT.tenantID, "Outro "+suffix, nil, "active")

	user := seedTeamMember(t, seed, zeta.tenantID, "tenant_admin", "active")
	addMembership(t, seed, alfa.tenantID, user, "tenant_agent", "active")
	addMembership(t, seed, revokedT.tenantID, user, "tenant_admin", "revoked")
	addMembership(t, seed, inactiveT.tenantID, user, "tenant_admin", "active")
	seedTeamMember(t, seed, strangerT.tenantID, "tenant_admin", "active") // someone else's tenant

	code, got, body := listMyTenants(t, app, user)
	if code != http.StatusOK {
		t.Fatalf("status %d %s", code, body)
	}
	if len(got) != 2 {
		t.Fatalf("want exactly the 2 usable tenants, got %d: %s", len(got), body)
	}
	// Ordered by the displayed name: "Alfa Telecom" (trade name) before "Zeta".
	if got[0].ID != alfa.tenantID || got[1].ID != zeta.tenantID {
		t.Fatalf("order must follow the displayed name, got %s then %s", got[0].LegalName, got[1].LegalName)
	}
	for _, leaked := range []string{"Revogado", "Inativo", "Outro "} {
		if strings.Contains(body, leaked) {
			t.Fatalf("response must not contain %q: %s", leaked, body)
		}
	}
}

func TestListMyTenantsNeverShowsAnotherUsersTenants(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a, b := seedTeamTenant(t, seed), seedTeamTenant(t, seed)
	userA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	userB := seedTeamMember(t, seed, b.tenantID, "tenant_admin", "active")

	_, gotA, _ := listMyTenants(t, app, userA)
	_, gotB, _ := listMyTenants(t, app, userB)
	if len(gotA) != 1 || gotA[0].ID != a.tenantID {
		t.Fatalf("user A must see only tenant A: %+v", gotA)
	}
	if len(gotB) != 1 || gotB[0].ID != b.tenantID {
		t.Fatalf("user B must see only tenant B: %+v", gotB)
	}
}

func TestListMyTenantsIsAnEmptyListWhenTheUserBelongsToNone(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	revoked := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "revoked")

	code, got, body := listMyTenants(t, app, revoked)
	if code != http.StatusOK || len(got) != 0 {
		t.Fatalf("a user with no usable tenant must get 200 and an empty list, got %d %s", code, body)
	}
	if strings.TrimSpace(body) != "[]" {
		t.Fatalf("the empty list must be [] (never null): %q", body)
	}
}

func TestListMyTenantsRequiresAPrincipal(t *testing.T) {
	_, app := teamSeedPool(t), teamAppPool(t)
	rec := httptest.NewRecorder()
	newMyTenantsHandler(app).ListMyTenants(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no principal = %d, want 401", rec.Code)
	}
}
