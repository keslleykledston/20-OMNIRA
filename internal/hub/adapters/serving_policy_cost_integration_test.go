package adapters_test

// ADR-0040 phase 04d (cost), migration 114: the policies of the delegated context were rewritten to ask the relationship ONCE per statement (acting_scope)
// instead of once per row (has_active_hub_access). The rewrite must change the COST, never the ANSWER. This test compares, over every shape of state that
// changes the answer, what the delegated context sees with what the original predicate (the legacy Hub policies, which are untouched outside the delegated
// context) says for the same person, on a real PostgreSQL with the application's role:
//   - conversations: the queue scope in all its shapes (absent, empty list, one or two queues, JSON null, not an array, upper-case id, other keys);
//   - messages: visible when the grant and contract are live AND the conversation is visible (the queue scope does not hide a message of an allowed queue);
//   - the state of the relationship: grant expired / not yet valid / revoked, contract ended, hub suspended, company suspended, account inactive, hub
//     membership removed. When the context cannot even be entered, nothing is visible in either form;
//   - the legacy policies still serve the Hub's own tab outside the delegated context, and a delegated session never gets a row from them.

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/provisioning"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

func (w *world) seenConversations(q platformdb.Querier, ctx context.Context) (convs []string, messages int) {
	rows, err := q.Query(ctx, `SELECT id::text FROM conversations WHERE tenant_id = $1`, w.tenant["A"])
	w.must(err)
	for rows.Next() {
		var id string
		w.must(rows.Scan(&id))
		convs = append(convs, id)
	}
	w.must(rows.Err())
	rows.Close()
	sort.Strings(convs)
	w.must(q.QueryRow(ctx, `SELECT count(*) FROM messages WHERE tenant_id = $1`, w.tenant["A"]).Scan(&messages))
	return convs, messages
}

func TestTheCheapPoliciesOfTheDelegatedContextAnswerLikeTheOriginalPredicate(t *testing.T) {
	w := newWorld(t)
	w.ceiling("A", provisioning.ServingPresets["atendimento"]...)
	agent := w.hubAgent("agent")
	g := w.grantReply(agent, "A")
	w.grantKeys(g, provisioning.ServingPresets["atendimento"]...)
	q1, q2 := w.queue1["A"].String(), w.queue2["A"].String()

	// legacy: the same person OUTSIDE the delegated context (the Hub's own tab path, policies untouched by 114)
	legacy := func() (convs []string, messages int) {
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) { convs, messages = w.seenConversations(q, ctx) })
		return
	}
	// delegated: the new policies; ok=false when the context cannot be entered
	delegated := func() (convs []string, messages int, ok bool) {
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
			if !w.enter(ctx, q, agent, "A") {
				return
			}
			ok = true
			convs, messages = w.seenConversations(q, ctx)
		})
		return
	}
	scope := func(raw string) func() {
		return func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = $2::jsonb WHERE id = $1`, w.contract["A"], raw)
		}
	}
	reset := func() {
		w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}'::jsonb, status = 'active', valid_from = now() - interval '1 day', valid_until = NULL WHERE id = $1`, w.contract["A"])
		w.exec(`UPDATE effective_access_grants SET status = 'active', valid_from = now() - interval '1 day', valid_until = NULL WHERE id = $1`, g)
		w.exec(`UPDATE service_hubs SET status = 'active' WHERE id = $1`, w.hub)
		w.exec(`UPDATE tenants SET status = 'active' WHERE id = $1`, w.tenant["A"])
		w.exec(`UPDATE users SET status = 'active' WHERE id = $1`, agent)
	}
	cases := []struct {
		name  string
		apply func()
		// the number of conversations the person must see (of A, A2 in queue 2, A0 without queue) when the context can be entered; -1 = cannot enter
		want int
	}{
		{"no scope at all ({})", scope(`{}`), 3},
		{"scope with other keys only", scope(`{"x": 1}`), 3},
		{"one queue", scope(`{"queue_ids": ["` + q1 + `"]}`), 1},
		{"two queues", scope(`{"queue_ids": ["` + q1 + `","` + q2 + `"]}`), 2},
		{"empty list", scope(`{"queue_ids": []}`), 0},
		{"JSON null list", scope(`{"queue_ids": null}`), 0},
		{"not an array", scope(`{"queue_ids": "` + q1 + `"}`), 0},
		{"list of numbers", scope(`{"queue_ids": [1, 2]}`), 0},
		{"upper-case id never matches", scope(`{"queue_ids": ["` + strings.ToUpper(q1) + `"]}`), 0},
		{"grant expired", func() {
			w.exec(`UPDATE effective_access_grants SET valid_until = now() - interval '1 minute' WHERE id = $1`, g)
		}, -1},
		{"grant not yet valid", func() {
			w.exec(`UPDATE effective_access_grants SET valid_from = now() + interval '1 hour' WHERE id = $1`, g)
		}, -1},
		{"grant revoked", func() { w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g) }, -1},
		{"contract ended", func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET valid_until = now() - interval '1 minute' WHERE id = $1`, w.contract["A"])
		}, -1},
		{"contract suspended", func() {
			w.exec(`UPDATE hub_tenant_service_contracts SET status = 'suspended' WHERE id = $1`, w.contract["A"])
		}, -1},
		{"hub suspended", func() { w.exec(`UPDATE service_hubs SET status = 'suspended' WHERE id = $1`, w.hub) }, -1},
		{"company suspended", func() { w.exec(`UPDATE tenants SET status = 'suspended' WHERE id = $1`, w.tenant["A"]) }, -1},
		{"account inactive", func() { w.exec(`UPDATE users SET status = 'inactive' WHERE id = $1`, agent) }, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reset()
			t.Cleanup(reset)
			c.apply()
			lc, lm := legacy()
			dc, dm, ok := delegated()
			if c.want < 0 {
				if ok {
					t.Fatalf("the delegated context could be entered; want it refused")
				}
				if len(lc) != 0 || lm != 0 {
					t.Errorf("the original predicate still shows %d conversations / %d messages", len(lc), lm)
				}
				return
			}
			if !ok {
				t.Fatalf("the delegated context could not be entered")
			}
			if strings.Join(dc, ",") != strings.Join(lc, ",") || dm != lm {
				t.Errorf("the delegated policies show %d conversations / %d messages; the original predicate shows %d / %d", len(dc), dm, len(lc), lm)
			}
			if len(dc) != c.want {
				t.Errorf("conversations seen = %d, want %d", len(dc), c.want)
			}
		})
	}

	t.Run("outside the delegated context the legacy policies still serve the Hub's own tab, and inside it they never contribute", func(t *testing.T) {
		reset()
		// a SECOND hub that serves the same instance with a wider scope: the legacy path (any hub) adds its queues; the delegated context must not
		hub2 := uuid.New()
		w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'Outro Hub')`, hub2)
		contract2 := uuid.New()
		w.exec(`INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_from) VALUES ($1, $2, $3, now() - interval '1 day')`, contract2, hub2, w.tenant["A"])
		w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, hub2, agent, w.roleHubAgent)
		w.exec(`INSERT INTO effective_access_grants (hub_id, user_id, tenant_id, service_contract_id, valid_from, can_reply) VALUES ($1, $2, $3, $4, now() - interval '1 day', true)`, hub2, agent, w.tenant["A"], contract2)
		t.Cleanup(func() { w.exec(`DELETE FROM hub_memberships WHERE hub_id = $1`, hub2) })
		scope(`{"queue_ids": ["` + q1 + `"]}`)()
		lc, _ := legacy()
		dc, _, ok := delegated()
		if !ok {
			t.Fatal("cannot enter")
		}
		if len(lc) != 3 {
			t.Errorf("the Hub's own path (any hub) sees %d conversations, want 3 (the other hub is unrestricted)", len(lc))
		}
		if len(dc) != 1 {
			t.Errorf("the delegated context for the first hub sees %d conversations, want only its own queue's 1", len(dc))
		}
	})

	// last: removing the membership also removes the grants that hang from it
	t.Run("a hub membership removed ends it in both forms", func(t *testing.T) {
		reset()
		w.exec(`DELETE FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, w.hub, agent)
		lc, lm := legacy()
		_, _, ok := delegated()
		if ok || len(lc) != 0 || lm != 0 {
			t.Errorf("entered=%v, original predicate shows %d/%d", ok, len(lc), lm)
		}
	})
}

func TestTheDelegatedTransactionDoesNotJITCompile(t *testing.T) {
	w := newWorld(t)
	agent := w.hubAgent("agent")
	w.ceiling("A", provisioning.ServingPresets["atendimento"]...)
	w.grantKeys(w.grantReply(agent, "A"), provisioning.ServingPresets["atendimento"]...)
	// the policies of the delegated context look costly on paper, which made PostgreSQL JIT-compile an inbox scan (1.2 s measured): off for that transaction
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		var before string
		w.must(q.QueryRow(ctx, `SELECT current_setting('jit')`).Scan(&before))
		if !w.enter(ctx, q, agent, "A") {
			t.Fatal("cannot enter")
		}
		var after string
		w.must(q.QueryRow(ctx, `SELECT current_setting('jit')`).Scan(&after))
		if after != "off" {
			t.Errorf("jit inside the delegated transaction = %q, want off", after)
		}
		if before == "off" {
			t.Skip("jit is already off for this server; nothing to prove")
		}
	})
	// and it is scoped to that transaction: the next one is untouched
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		var v string
		w.must(q.QueryRow(ctx, `SELECT current_setting('jit')`).Scan(&v))
		if v == "off" {
			t.Error("the setting leaked out of the delegated transaction")
		}
	})
}
