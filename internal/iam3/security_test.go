package iam3_test

import (
	"context"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pins the exact permission set of each system role. Runtime authorization is an exact
// permission_key match on role_permissions (no wildcard, prefix or inheritance), so holding
// membership.manage never implies channel.manage. Revocation, cross-tenant isolation and
// privilege-escalation behavior are covered against real fixtures in
// internal/tenancy/adapters/team_http_test.go.
var systemRolePermissions = map[string][]string{
	"tenant_admin": {
		"agent.manage", "agent.read", "audit.read", "channel.manage", "conversation.claim", "conversation.manage",
		"dashboard.read", "membership.manage", "membership.read", "ticket.create", "ticket.read", "ticket.reconcile", "ticket.update", "tenant.manage", "tenant.read",
	},
	"tenant_supervisor": {
		"agent.manage", "agent.read", "audit.read", "conversation.claim", "conversation.manage", "dashboard.read", "membership.read", "ticket.create", "ticket.read", "ticket.reconcile", "ticket.update", "tenant.read",
	},
	// PRODUCT.6-F: ticket.create does NOT imply ticket.read — an agent may
	// create a ticket from a conversation they are already authorized to
	// operate, but tenant-wide ticket visibility remains a separate grant
	// (see migration 000043's own rationale, unchanged by this one).
	// PRODUCT.6-O2B1: ticket.update follows the exact same rationale as
	// ticket.create (migration 000049) — mutate the lifecycle of an
	// already-linked ticket from a conversation the actor is authorized to
	// operate, still never implying tenant-wide ticket.read.
	// PRODUCT.7A1: ticket.reconcile (migration 000051) is never granted to
	// tenant_agent — it is a support/admin operational-visibility
	// capability, not an operating-agent one.
	"tenant_agent": {"conversation.claim", "tenant.read", "ticket.create", "ticket.update"},
	"hub_admin":    {"hub.manage", "hub.read"},
}

func seedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("OMNIRA_DATABASE_URL")
	if url == "" {
		t.Skip("OMNIRA_DATABASE_URL not set; skipping IAM3 role matrix test")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestSystemRolePermissionMatrix(t *testing.T) {
	pool := seedPool(t)
	ctx := context.Background()

	for role, want := range systemRolePermissions {
		rows, err := pool.Query(ctx, `
			SELECT rp.permission_key
			FROM roles r JOIN role_permissions rp ON rp.role_id = r.id
			WHERE r.tenant_id IS NULL AND r.key = $1`, role)
		if err != nil {
			t.Fatalf("%s: query: %v", role, err)
		}
		var got []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatalf("%s: scan: %v", role, err)
			}
			got = append(got, k)
		}
		rows.Close()
		sort.Strings(got)
		wantSorted := append([]string(nil), want...)
		sort.Strings(wantSorted)
		if !reflect.DeepEqual(got, wantSorted) {
			t.Errorf("%s permissions = %v, want %v", role, got, wantSorted)
		}
	}
}

func TestManageDoesNotImplyOtherManage(t *testing.T) {
	// A role holding membership.manage must not gain channel.manage unless granted explicitly:
	// supervisor has neither *.manage on membership/channel/tenant; agent has none.
	for _, role := range []string{"tenant_supervisor", "tenant_agent"} {
		for _, p := range systemRolePermissions[role] {
			switch p {
			case "membership.manage", "channel.manage", "tenant.manage":
				t.Errorf("%s must not hold %s", role, p)
			}
		}
	}
}
