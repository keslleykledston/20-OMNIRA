// Package flowstest seeds real tenants/users/memberships in the DISPOSABLE integration database
// (testhelpers.RequireIntegrationDatabase guards the database name) for the flows tests.
package flowstest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

// Env has two isolated tenants, each with an admin member, and two pools: Seed (owner, bypasses RLS: setup/assertions
// only) and App (omnira_app: what the product runs as, subject to RLS).
type Env struct {
	Seed, App        *pgxpool.Pool
	TenantA, TenantB uuid.UUID
	UserA, UserB     uuid.UUID
}

func New(t *testing.T) *Env {
	t.Helper()
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}
	e := &Env{Seed: seed, App: app, TenantA: uuid.New(), TenantB: uuid.New(), UserA: uuid.New(), UserB: uuid.New()}
	var roleID uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL LIMIT 1`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	for _, u := range []uuid.UUID{e.UserA, e.UserB} {
		if _, err := seed.Exec(ctx, `INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid"); err != nil {
			t.Fatal(err)
		}
	}
	for i, tn := range []uuid.UUID{e.TenantA, e.TenantB} {
		if _, err := seed.Exec(ctx, `INSERT INTO tenants(id, legal_name, status) VALUES($1,$2,'active')`, tn, "flows-test-"+tn.String()[:8]); err != nil {
			t.Fatal(err)
		}
		user := []uuid.UUID{e.UserA, e.UserB}[i]
		if _, err := seed.Exec(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tn, user, roleID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		// tenants cascade to every flow table; users are global
		_, _ = seed.Exec(c, `DELETE FROM tenants WHERE id IN ($1,$2)`, e.TenantA, e.TenantB)
		_, _ = seed.Exec(c, `DELETE FROM users WHERE id IN ($1,$2)`, e.UserA, e.UserB)
		seed.Close()
		app.Close()
	})
	return e
}

// AsUser runs fn in a tenant session of a human member (RLS by membership) with a matching TenantContext.
func (e *Env) AsUser(t *testing.T, tenant, user uuid.UUID, fn func(ctx context.Context)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := platformdb.WithTenantSession(ctx, e.App, user, false, func(sc context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		fn(tenancydomain.WithTenantContext(sc, tc))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// AsSystem runs fn the way the worker does: a system session scoped to one tenant derived from trusted state.
func (e *Env) AsSystem(t *testing.T, tenant uuid.UUID, fn func(ctx context.Context)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := platformdb.WithSystemTenantSession(ctx, e.App, tenant, func(sc context.Context) error {
		fn(sc)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
