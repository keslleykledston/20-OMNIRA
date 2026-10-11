package adapters_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// ADR-0042: while a session acts for an instance through the Hub, the person's OWN membership of that company (a Hub agent can also be a tenant administrator somewhere) confers
// nothing: the delegated context is the only thing that speaks for it. The membership predicates must answer false inside the acting context and true outside it, in the legacy modes
// and in enforce, whatever else the person is.
func TestMembershipPredicatesAreFalseWhileActing(t *testing.T) {
	w := newWorld(t)
	agent := w.hubAgent("agent")
	w.grantKeys(w.grant(agent, "A"), "conversation.read")
	w.ceiling("A", "conversation.read")
	// the same person is also a tenant administrator of A on their own
	w.exec(`INSERT INTO memberships (tenant_id, user_id, role_id) VALUES ($1, $2, $3)`, w.tenant["A"], agent, w.role("tenant_admin"))

	ask := func(ctx context.Context, q platformdb.Querier, user uuid.UUID) (member, admin bool) {
		w.must(q.QueryRow(ctx, `SELECT has_active_membership($1, $2), has_active_admin_membership($1, $2)`, w.tenant["A"], user).Scan(&member, &admin))
		return member, admin
	}
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if member, admin := ask(ctx, q, agent); !member || !admin {
			t.Fatalf("outside the delegated context the person is a member and an administrator (member=%v admin=%v)", member, admin)
		}
	})
	w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
		if member, admin := ask(ctx, q, agent); member || admin {
			t.Errorf("inside the delegated context the person's own membership must not speak (member=%v admin=%v)", member, admin)
		}
		var perm bool
		w.must(q.QueryRow(ctx, `SELECT actor_has_permission($1, $2, 'membership.read')`, w.tenant["A"], agent).Scan(&perm))
		if perm {
			t.Error("inside the delegated context an administrator's own permission must not apply (only the grant's keys do)")
		}
	})
}
