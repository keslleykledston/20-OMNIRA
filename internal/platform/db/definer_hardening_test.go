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
