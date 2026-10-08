package db_test

// Guards the SECURITY DEFINER search_path hardening (migration 000096): a definer function that does not pin
// pg_temp last can be fooled by a same-named TEMP table. This is a catalogue test, so a NEW definer function
// added without the pin fails the build instead of silently reintroducing the hole.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/testhelpers"
)

func TestSecurityDefinerFunctionsPinPgTempLast(t *testing.T) {
	ownerURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()

	rows, err := owner.Query(ctx, `SELECT p.proname, coalesce(array_to_string(p.proconfig, ','), '')
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'public' AND p.prosecdef ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name, cfg string
		if err := rows.Scan(&name, &cfg); err != nil {
			t.Fatal(err)
		}
		seen++
		if !strings.Contains(cfg, "search_path=pg_catalog, public, pg_temp") {
			t.Errorf("SECURITY DEFINER function %s does not pin pg_catalog, public, pg_temp (config=%q): a TEMP table can shadow its tables", name, cfg)
		}
	}
	if seen < 10 {
		t.Fatalf("expected the known definer functions, found only %d", seen)
	}

	// the hardened functions still evaluate under the application role (behaviour preserved)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	err = platformdb.WithTenantSession(ctx, app, uuid.New(), false, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, app)
		var ok bool
		for _, sql := range []string{
			`SELECT has_active_membership(gen_random_uuid(), current_user_id())`,
			`SELECT has_active_admin_membership(gen_random_uuid(), current_user_id())`,
			`SELECT can_read_invitation(gen_random_uuid(), 'nobody@example.test')`,
			`SELECT can_read_tenant_peer(gen_random_uuid())`,
		} {
			if err := q.QueryRow(c, sql).Scan(&ok); err != nil {
				return err
			}
			if ok {
				t.Errorf("%s returned true for a user with no relation to anything", sql)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("a hardened definer function failed to evaluate: %v", err)
	}
}

// has_active_membership / has_active_admin_membership answer only for the session user (or a system session):
// they used to be a cross-user oracle for "does X belong to tenant T / administer it".
func TestMembershipHelpersAnswerOnlyForTheSessionUser(t *testing.T) {
	ownerURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	tenant, alice, bob := uuid.New(), uuid.New(), uuid.New()
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(owner.Exec(ctx, `INSERT INTO tenants (id, legal_name, status) VALUES ($1, $2, 'active')`, tenant, "membership-oracle-"+tenant.String()))
	must(owner.Exec(ctx, `INSERT INTO users (id, external_subject) VALUES ($1, $3), ($2, $4)`, alice, bob, "alice-"+alice.String(), "bob-"+bob.String()))
	for _, u := range []uuid.UUID{alice, bob} {
		must(owner.Exec(ctx, `INSERT INTO memberships (tenant_id, user_id, role_id)
			SELECT $1, $2, id FROM roles WHERE tenant_id IS NULL AND key = 'tenant_admin' LIMIT 1`, tenant, u))
	}
	ask := func(system bool, as uuid.UUID, sql string, args ...any) bool {
		t.Helper()
		var v bool
		if err := platformdb.WithTenantSession(ctx, app, as, system, func(c context.Context) error {
			return platformdb.QuerierFromContext(c, app).QueryRow(c, sql, args...).Scan(&v)
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, fn := range []string{"has_active_membership", "has_active_admin_membership"} {
		q := `SELECT ` + fn + `($1, $2)`
		if !ask(false, alice, q, tenant, alice) {
			t.Errorf("%s: alice must be able to ask about herself", fn)
		}
		if ask(false, alice, q, tenant, bob) {
			t.Errorf("%s: alice learned that bob belongs to / administers the tenant", fn)
		}
		if !ask(true, uuid.New(), q, tenant, bob) {
			t.Errorf("%s: a system session must still be able to ask about any user (workers, provisioning)", fn)
		}
	}
	// the policies built on the helper behave exactly as before for the user themselves
	var seen int
	if err := platformdb.WithTenantSession(ctx, app, alice, false, func(c context.Context) error {
		return platformdb.QuerierFromContext(c, app).QueryRow(c, `SELECT count(*) FROM tenants WHERE id = $1`, tenant).Scan(&seen)
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Errorf("a member lost access to their own tenant row: %d", seen)
	}
}
