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

	// an inbox row of the SECOND hub for company B, where the person holds no grant through that hub: it must never be visible, whatever the first hub allows
	w.exec(`INSERT INTO hub_inbox_items (hub_id, tenant_id, conversation_id, queue_id) VALUES ($1, $2, $3, $4)`, hub2, w.tenant["B"], w.conv["B"], w.queue1["B"])

	type rowSeen struct{ conv, item bool }
	keys := []string{"A", "A2", "A0", "B", "C"}
	// what the policies show, and what the original function says, per conversation key
	compareFor := func(t *testing.T, agent uuid.UUID) {
		t.Helper()
		got := map[string]rowSeen{}
		want := map[string]rowSeen{}
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
			for _, k := range keys {
				var s rowSeen
				var n int
				w.must(q.QueryRow(ctx, `SELECT count(*) FROM conversations WHERE id = $1`, w.conv[k]).Scan(&n))
				s.conv = n > 0
				w.must(q.QueryRow(ctx, `SELECT count(*) FROM hub_inbox_items WHERE conversation_id = $1 AND hub_id = $2`, w.conv[k], w.hub).Scan(&n))
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
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
			var n int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM hub_inbox_items WHERE hub_id = $1`, hub2).Scan(&n))
			if n != 0 {
				t.Errorf("an inbox row of a hub the person holds no grant through is visible (%d)", n)
			}
		})
		// the SET hub_reach() returns is the oracle's answer for every (hub, company) pair, not only what the policies make of it
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
			for _, hub := range []uuid.UUID{w.hub, hub2} {
				for _, k := range []string{"A", "B", "C"} {
					var inReach, oracle bool
					w.must(q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hub_reach() r WHERE r.hub_id = $1 AND r.tenant_id = $2)`, hub, w.tenant[k]).Scan(&inReach))
					w.must(q.QueryRow(ctx, `SELECT has_active_hub_access(current_user_id(), $1, NULL, $2, false)`, w.tenant[k], hub).Scan(&oracle))
					if inReach != oracle {
						t.Errorf("hub_reach() lists (hub %s, company %s) = %v; the original function says %v", hub.String()[:8], k, inReach, oracle)
					}
				}
			}
		})
		var shown []string
		for _, k := range keys {
			if got[k].conv {
				shown = append(shown, k)
			}
		}
		t.Logf("visible conversations: %s", strings.Join(shown, ","))
	}

	compare := func(t *testing.T) { t.Helper(); compareFor(t, agent) }
	scopeA := func(raw string) func() {
		return func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = $2::jsonb WHERE id = $1`, w.contract["A"], raw)
		}
	}
	reset := func() {
		w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb, status = 'active', valid_from = now() - interval '1 day', valid_until = NULL`)
		w.exec(`UPDATE effective_access_grants SET status = 'active', valid_from = now() - interval '1 day', valid_until = NULL`)
		// the second hub starts OFF: its unrestricted scope would hide every scope case of the first one; the cases about two hubs switch it on
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g2)
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
		{"a queue of another company, with no grant there, shows nothing of it", func() {
			scopeA(`{"queue_ids": ["` + qB + `"]}`)()
			w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, gB)
		}},
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
		{"second hub unrestricted widens the first scoped to one queue", func() {
			scopeA(`{"queue_ids": ["` + q1 + `"]}`)()
			w.exec(`UPDATE effective_access_grants SET status = 'active' WHERE id = $1`, g2)
		}},
		{"second hub scoped to the other queue, first to one", func() {
			scopeA(`{"queue_ids": ["` + q1 + `"]}`)()
			w.exec(`UPDATE effective_access_grants SET status = 'active' WHERE id = $1`, g2)
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = $2::jsonb WHERE id = $1`, contract2, `{"queue_ids": ["`+q2+`"]}`)
		}},
		{"second hub ended, first scoped to nothing", func() {
			scopeA(`{"queue_ids": []}`)()
			w.exec(`UPDATE effective_access_grants SET status = 'active' WHERE id = $1`, g2)
			w.exec(`UPDATE hub_tenant_service_contracts SET valid_until = now() - interval '1 minute' WHERE id = $1`, contract2)
		}},
		{"grant valid from this very instant", func() {
			w.exec(`UPDATE effective_access_grants SET valid_from = now() WHERE id = $1`, gA)
		}},
		{"grant and contract ending in the future", func() {
			w.exec(`UPDATE effective_access_grants SET valid_until = now() + interval '1 hour' WHERE id = $1`, gA)
			w.exec(`UPDATE hub_tenant_service_contracts SET valid_until = now() + interval '1 hour' WHERE id = $1`, w.contract["A"])
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

	// another person: a hub member with NO grant, and a person with a grant for another company only, see none of A's rows
	t.Run("a hub member without a grant, and a person whose only grant is elsewhere", func(t *testing.T) {
		reset()
		nogrant := w.hubAgent("nogrant")
		compareFor(t, nogrant)
		onlyB := w.hubAgent("only-b")
		w.grant(onlyB, "B")
		compareFor(t, onlyB)
		var a int
		w.inSession(onlyB, func(ctx context.Context, q platformdb.Querier) {
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM hub_inbox_items WHERE tenant_id = $1`, w.tenant["A"]).Scan(&a))
		})
		if a != 0 {
			t.Errorf("a person with a grant on company B only sees %d inbox rows of company A", a)
		}
	})
	t.Run("a forged identity without the signed session context sees nothing when the database enforces it", func(t *testing.T) {
		reset()
		if !w.enforced() {
			t.Skip("the database is not in mode enforce; the legacy settings are trusted in off and measure")
		}
		tx, err := w.app.Begin(w.ctx)
		w.must(err)
		defer tx.Rollback(w.ctx)
		_, err = tx.Exec(w.ctx, `SELECT set_config('app.current_user_id', $1, true)`, agent.String())
		w.must(err)
		var items, convs int
		w.must(tx.QueryRow(w.ctx, `SELECT count(*) FROM hub_inbox_items`).Scan(&items))
		w.must(tx.QueryRow(w.ctx, `SELECT count(*) FROM conversations`).Scan(&convs))
		if items != 0 || convs != 0 {
			t.Errorf("a forged identity reads %d inbox rows and %d conversations", items, convs)
		}
	})

	t.Run("a hub membership removed ends the reach in both forms", func(t *testing.T) {
		reset()
		leaver := w.hubAgent("leaver")
		w.grant(leaver, "A")
		compareFor(t, leaver)
		w.exec(`DELETE FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, w.hub, leaver)
		compareFor(t, leaver)
		var n int
		w.inSession(leaver, func(ctx context.Context, q platformdb.Querier) {
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM hub_inbox_items`).Scan(&n))
		})
		if n != 0 {
			t.Errorf("a person who left the hub still reads %d inbox rows", n)
		}
	})

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
