package adapters_test

// Migration 119 (cost at scale): the policies of hub_inbox_items and of conversations OUTSIDE the delegated context (the Hub's own tab) stopped calling
// has_active_hub_access(...) once per row and ask hub_reach() once per statement. The rewrite must change the COST, never the ANSWER. This test keeps the
// original function as the ORACLE: for every shape of state that changes the answer it asks the function, with the literal tenant/queue/hub of each row,
// and requires the policies to show exactly the rows it says (and, for the inbox row, only when the parent conversation is also visible). Postgres real,
// papel omnira_app, FORCE RLS.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

func TestHubReachPoliciesAnswerLikeTheOriginalFunction(t *testing.T) {
	w := newWorld(t)
	agent := w.hubAgent("agent")
	gA := w.grant(agent, "A")
	gB := w.grant(agent, "B") // a second company, so the answer is compared across companies
	q1, q2 := w.queue1["A"].String(), w.queue2["A"].String()
	qB := w.queue1["B"].String()

	// a SECOND hub serving company A with another scope: the Hub's own tab adds the queues of every hub the person belongs to
	hub2, contract2 := uuid.New(), uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'Outro Hub')`, hub2)
	w.exec(`INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_from) VALUES ($1, $2, $3, now() - interval '1 day')`, contract2, hub2, w.tenant["A"])
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, hub2, agent, w.roleHubAgent)
	g2 := uuid.New()
	w.exec(`INSERT INTO effective_access_grants (id, hub_id, user_id, tenant_id, service_contract_id, valid_from) VALUES ($1, $2, $3, $4, $5, now() - interval '1 day')`,
		g2, hub2, agent, w.tenant["A"], contract2)

	type rowSeen struct{ conv, item bool }
	keys := []string{"A", "A2", "A0", "B", "C"}
	// what the policies show, and what the original function says, per conversation key
	compare := func(t *testing.T) {
		t.Helper()
		got := map[string]rowSeen{}
		want := map[string]rowSeen{}
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
			for _, k := range keys {
				var s rowSeen
				var n int
				w.must(q.QueryRow(ctx, `SELECT count(*) FROM conversations WHERE id = $1`, w.conv[k]).Scan(&n))
				s.conv = n > 0
				w.must(q.QueryRow(ctx, `SELECT count(*) FROM hub_inbox_items WHERE conversation_id = $1`, w.conv[k]).Scan(&n))
				s.item = n > 0
				got[k] = s

				tenant, queue := w.convTenantQueue(k)
				var convOK, itemAccess bool
				w.must(q.QueryRow(ctx, `SELECT has_active_hub_access(current_user_id(), $1, $2)`, tenant, queue).Scan(&convOK))
				w.must(q.QueryRow(ctx, `SELECT has_active_hub_access(current_user_id(), $1, NULL, $2, false)`, tenant, w.hub).Scan(&itemAccess))
				want[k] = rowSeen{conv: convOK, item: itemAccess && convOK}
			}
		})
		for _, k := range keys {
			if got[k] != want[k] {
				t.Errorf("conversation %s: policies show conversation=%v item=%v; the original function says conversation=%v item=%v", k, got[k].conv, got[k].item, want[k].conv, want[k].item)
			}
		}
		var shown []string
		for _, k := range keys {
			if got[k].conv {
				shown = append(shown, k)
			}
		}
		t.Logf("visible conversations: %s", strings.Join(shown, ","))
	}

	scopeA := func(raw string) func() {
		return func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = $2::jsonb WHERE id = $1`, w.contract["A"], raw)
		}
	}
	reset := func() {
		w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb, status = 'active', valid_from = now() - interval '1 day', valid_until = NULL`)
		w.exec(`UPDATE effective_access_grants SET status = 'active', valid_from = now() - interval '1 day', valid_until = NULL`)
		w.exec(`UPDATE service_hubs SET status = 'active'`)
		w.exec(`UPDATE tenants SET status = 'active'`)
		w.exec(`UPDATE users SET status = 'active' WHERE id = $1`, agent)
	}
	cases := []struct {
		name  string
		apply func()
	}{
		{"no scope at all", scopeA(`{}`)},
		{"scope with other keys only", scopeA(`{"x": 1}`)},
		{"one queue", scopeA(`{"queue_ids": ["` + q1 + `"]}`)},
		{"two queues", scopeA(`{"queue_ids": ["` + q1 + `","` + q2 + `"]}`)},
		{"a queue of another company never widens", scopeA(`{"queue_ids": ["` + qB + `"]}`)},
		{"empty list", scopeA(`{"queue_ids": []}`)},
		{"JSON null list", scopeA(`{"queue_ids": null}`)},
		{"not an array", scopeA(`{"queue_ids": "` + q1 + `"}`)},
		{"list of numbers", scopeA(`{"queue_ids": [1, 2]}`)},
		{"nested list", scopeA(`{"queue_ids": [["` + q1 + `"]]}`)},
		{"upper-case id never matches", scopeA(`{"queue_ids": ["` + strings.ToUpper(q1) + `"]}`)},
		{"grant expired", func() {
			w.exec(`UPDATE effective_access_grants SET valid_until = now() - interval '1 minute' WHERE id = $1`, gA)
		}},
		{"grant not yet valid", func() {
			w.exec(`UPDATE effective_access_grants SET valid_from = now() + interval '1 hour' WHERE id = $1`, gA)
		}},
		{"grant revoked", func() { w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, gA) }},
		{"contract ended", func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET valid_until = now() - interval '1 minute' WHERE id = $1`, w.contract["A"])
		}},
		{"contract suspended", func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE id = $1`, w.contract["A"])
		}},
		{"hub suspended", func() { w.exec(`UPDATE service_hubs SET status = 'suspended' WHERE id = $1`, w.hub) }},
		{"company suspended", func() { w.exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, w.tenant["A"]) }},
		{"both companies suspended", func() {
			w.exec(`UPDATE tenants SET status = 'suspended' WHERE id IN ($1, $2)`, w.tenant["A"], w.tenant["B"])
		}},
		{"account inactive", func() { w.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, agent) }},
		{"second hub revoked, first scoped to one queue", func() {
			scopeA(`{"queue_ids": ["` + q1 + `"]}`)()
			w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g2)
		}},
		{"second hub unrestricted widens the first scoped to one queue", scopeA(`{"queue_ids": ["` + q1 + `"]}`)},
		{"second hub scoped to the other queue, first to one", func() {
			scopeA(`{"queue_ids": ["` + q1 + `"]}`)()
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = $2::jsonb WHERE id = $1`, contract2, `{"queue_ids": ["`+q2+`"]}`)
		}},
		{"second hub ended, first scoped to nothing", func() {
			scopeA(`{"queue_ids": []}`)()
			w.exec(`UPDATE hub_tenant_service_contracts SET valid_until = now() - interval '1 minute' WHERE id = $1`, contract2)
		}},
		{"company B grant revoked", func() { w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, gB) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reset()
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb WHERE id = $1`, contract2)
			t.Cleanup(reset)
			c.apply()
			compare(t)
		})
	}

	// the baseline is not vacuous: an unrestricted agent sees conversations of A and B, none of C, and the policies are what hide the rest
	t.Run("the baseline sees A and B and never C", func(t *testing.T) {
		reset()
		var a, b, c int
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM hub_inbox_items WHERE tenant_id = $1`, w.tenant["A"]).Scan(&a))
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM hub_inbox_items WHERE tenant_id = $1`, w.tenant["B"]).Scan(&b))
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM hub_inbox_items WHERE tenant_id = $1`, w.tenant["C"]).Scan(&c))
		})
		if a != 3 || b != 1 || c != 0 {
			t.Errorf("items seen: A=%d (want 3) B=%d (want 1) C=%d (want 0)", a, b, c)
		}
	})
}

// convTenantQueue returns the company and the queue of a fixture conversation, read by the owner.
func (w *world) convTenantQueue(key string) (tenant uuid.UUID, queue *uuid.UUID) {
	w.must(w.owner.QueryRow(w.ctx, `SELECT tenant_id, queue_id FROM conversations WHERE id = $1`, w.conv[key]).Scan(&tenant, &queue))
	return
}
