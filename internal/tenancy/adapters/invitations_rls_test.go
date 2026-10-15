package adapters

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// 000035 wrote `m.tenant_id = tenant_id` inside the policy's subquery, where `tenant_id` resolves to m.tenant_id: whoever could manage
// invitations in ONE instance passed the INSERT policy for any other instance. 000135 compares with the invitation's own tenant.
func TestInvitationInsertPolicyIsBoundToTheInvitationsOwnInstance(t *testing.T) {
	seed, app := teamSeedPool(t), teamAppPool(t)
	a := seedTeamTenant(t, seed)
	b := seedTeamTenant(t, seed)
	adminA := seedTeamMember(t, seed, a.tenantID, "tenant_admin", "active")
	agentB := seedTeamMember(t, seed, b.tenantID, "tenant_agent", "active")
	var roleID uuid.UUID
	if err := seed.QueryRow(context.Background(), `SELECT id FROM roles WHERE key = 'tenant_agent' AND tenant_id IS NULL`).Scan(&roleID); err != nil {
		t.Fatalf("role: %v", err)
	}

	invite := func(actor, actingTenant, target uuid.UUID) error {
		return asActor(t, app, actingTenant, actor, func(ctx context.Context) error {
			_, err := platformdb.QuerierFromContext(ctx, app).Exec(ctx, `
				INSERT INTO membership_invitations (tenant_id, email, role_id, token_hash, created_by, expires_at)
				VALUES ($1, $2, $3, $4, $5, now() + interval '1 day')`,
				target, uuid.NewString()+"@rls.test", roleID, uuid.NewString(), actor)
			return err
		})
	}
	refusedByRLS := func(err error) bool {
		var pg *pgconn.PgError
		return errors.As(err, &pg) && pg.Code == "42501"
	}

	if err := invite(adminA, a.tenantID, a.tenantID); err != nil {
		t.Fatalf("an admin of A must be able to invite to A: %v", err)
	}
	if err := invite(adminA, a.tenantID, b.tenantID); !refusedByRLS(err) {
		t.Fatalf("an admin of A inviting to B = %v, want the RLS refusal (42501)", err)
	}
	if err := invite(agentB, b.tenantID, b.tenantID); !refusedByRLS(err) {
		t.Fatalf("an agent of B (no membership.manage) inviting to B = %v, want the RLS refusal (42501)", err)
	}
	if _, err := seed.Exec(context.Background(), `UPDATE memberships SET status = 'revoked' WHERE tenant_id = $1 AND user_id = $2`, a.tenantID, adminA); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := invite(adminA, a.tenantID, a.tenantID); !refusedByRLS(err) {
		t.Fatalf("a revoked admin of A inviting to A = %v, want the RLS refusal (42501)", err)
	}
}
