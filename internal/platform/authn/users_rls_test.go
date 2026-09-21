package authn

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// These tests pin the security model of the users table: the row is created by
// the trusted server-side provisioning flow and by nothing else.

func usersSeedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("OMNIRA_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_DATABASE_URL not set; skipping users RLS tests")
	}
	return openUsersPool(t, dbURL)
}

// The runtime role is the one RLS applies to; the owner is a superuser and
// would bypass every policy below.
func usersAppPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("OMNIRA_APP_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_APP_DATABASE_URL not set; skipping RLS-enforced users tests")
	}
	return openUsersPool(t, dbURL)
}

func openUsersPool(t *testing.T, dbURL string) *pgxpool.Pool {
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

func seedUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, external_subject, email, status) VALUES ($1,$2,$3,'active')`,
		id, id.String(), id.String()+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id)
	})
	return id
}

// seedTenantAdmin gives the user an active tenant_admin membership — the
// highest role a tenant can grant.
func seedTenantAdmin(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tenantID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants (id, legal_name, isolation_profile, status)
		 VALUES ($1,$2,'shared_strong_isolation','active')`,
		tenantID, "users-rls-"+tenantID.String()[:8]); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	var roleID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL LIMIT 1`).Scan(&roleID); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,'active')`,
		uuid.New(), tenantID, userID, roleID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenantID)
	})
}

// cleaner is the owner pool: users has no DELETE policy, so the runtime role
// cannot remove even a row it just inserted.
func insertUserAs(t *testing.T, pool, cleaner *pgxpool.Pool, actor uuid.UUID, systemAdmin bool) error {
	t.Helper()
	newID := uuid.New()
	t.Cleanup(func() {
		_, _ = cleaner.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, newID)
	})
	return platformdb.WithTenantSession(context.Background(), pool, actor, systemAdmin, func(ctx context.Context) error {
		_, err := platformdb.QuerierFromContext(ctx, pool).Exec(ctx,
			`INSERT INTO users (id, external_subject, email, status) VALUES ($1,$2,$3,'active')`,
			newID, newID.String(), newID.String()+"@example.com")
		return err
	})
}

// A: the trusted provisioning context may create the row.
func TestUsersInsertAllowedInSystemContext(t *testing.T) {
	seed, app := usersSeedPool(t), usersAppPool(t)
	if err := insertUserAs(t, app, seed, uuid.Nil, true); err != nil {
		t.Fatalf("system context must be able to provision a user: %v", err)
	}
}

// B: an ordinary authenticated session must not create users.
func TestUsersInsertDeniedInNormalContext(t *testing.T) {
	seed, app := usersSeedPool(t), usersAppPool(t)
	actor := seedUser(t, seed)

	if err := insertUserAs(t, app, seed, actor, false); err == nil {
		t.Fatal("an ordinary session was allowed to insert a user")
	}
}

// C: tenant_admin is a membership role and must not confer the global ability
// to mint identities.
func TestUsersInsertDeniedForTenantAdmin(t *testing.T) {
	seed, app := usersSeedPool(t), usersAppPool(t)
	actor := seedUser(t, seed)
	seedTenantAdmin(t, seed, actor)

	if err := insertUserAs(t, app, seed, actor, false); err == nil {
		t.Fatal("tenant_admin was allowed to insert a user")
	}
}

// D and E: a user reads itself, and not other users.
func TestUsersSelectIsLimitedToSelf(t *testing.T) {
	seed, app := usersSeedPool(t), usersAppPool(t)
	actor, other := seedUser(t, seed), seedUser(t, seed)

	if err := platformdb.WithTenantSession(context.Background(), app, actor, false, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, app)
		var self bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, actor).Scan(&self); err != nil {
			return err
		}
		if !self {
			t.Fatal("user cannot read its own row")
		}
		var foreign bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, other).Scan(&foreign); err != nil {
			return err
		}
		if foreign {
			t.Fatal("user read another user's row")
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

// F: self-update still works and stays confined to the caller's own row.
func TestUsersUpdateIsLimitedToSelf(t *testing.T) {
	seed, app := usersSeedPool(t), usersAppPool(t)
	actor, other := seedUser(t, seed), seedUser(t, seed)

	if err := platformdb.WithTenantSession(context.Background(), app, actor, false, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, app)
		tag, err := q.Exec(ctx, `UPDATE users SET display_name='self' WHERE id=$1`, actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			t.Fatal("user could not update its own row")
		}
		tag, err = q.Exec(ctx, `UPDATE users SET display_name='hijacked' WHERE id=$1`, other)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 0 {
			t.Fatal("user updated another user's row")
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}

// G: an ordinary session must not be a system admin. The flag is a literal in
// the Go call sites and never derives from the request, so a normal session
// evaluates is_system_admin() as false.
func TestOrdinarySessionIsNotSystemAdmin(t *testing.T) {
	seed, app := usersSeedPool(t), usersAppPool(t)
	actor := seedUser(t, seed)

	if err := platformdb.WithTenantSession(context.Background(), app, actor, false, func(ctx context.Context) error {
		var isAdmin bool
		if err := platformdb.QuerierFromContext(ctx, app).
			QueryRow(ctx, `SELECT is_system_admin()`).Scan(&isAdmin); err != nil {
			return err
		}
		if isAdmin {
			t.Fatal("an ordinary session evaluated is_system_admin() as true")
		}
		return nil
	}); err != nil {
		t.Fatalf("session: %v", err)
	}
}
