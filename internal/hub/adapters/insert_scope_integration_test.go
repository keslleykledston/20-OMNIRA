package adapters_test

// Pre-gate review (Codex) of the database layer, migration 121: with arbitrary SQL as the application role, a signed person must not be able to write an audit
// row for another company or in another person's name, nor enqueue an outbox event for a company they have nothing to do with, nor re-point the acting context
// of a transaction at another Hub/company. Legitimate writers (a member, a Hub agent serving the company, the Hub manager, a system session) keep working.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/provisioning"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

func (w *world) tryAudit(q platformdb.Querier, ctx context.Context, tenant, actor any) error {
	return w.try(ctx, q, `INSERT INTO audit_events(tenant_id, actor_id, action, resource_type, resource_id, outcome) VALUES ($1, $2, 'test.insert_scope', 'test', $3, 'success')`, tenant, actor, uuid.New())
}

func (w *world) tryOutbox(q platformdb.Querier, ctx context.Context, tenant uuid.UUID) error {
	return w.try(ctx, q, `INSERT INTO outbox_events(tenant_id, event_type, aggregate_type, aggregate_id, payload) VALUES ($1, 'test.insert_scope', 'test', $2, '{}')`, tenant, uuid.New())
}

func denied(err error) bool { return err != nil && strings.Contains(err.Error(), "row-level security") }

func TestAuditAndOutboxInsertsAreScopedToThePersonsCompanies(t *testing.T) {
	w := newWorld(t)
	member := w.user("member-of-a")
	w.directMember(member, "A")
	other := w.user("somebody-else")
	w.directMember(other, "B")
	agent := w.hubAgent("hub-agent")
	w.grant(agent, "A") // serves A only

	w.inSession(member, func(ctx context.Context, q platformdb.Querier) {
		if err := w.tryAudit(q, ctx, w.tenant["A"], member); err != nil {
			t.Errorf("a member writing their own audit row in their own company: %v", err)
		}
		if err := w.tryOutbox(q, ctx, w.tenant["A"]); err != nil {
			t.Errorf("a member enqueueing an event of their own company: %v", err)
		}
	})
	w.inSession(member, func(ctx context.Context, q platformdb.Querier) {
		if err := w.tryAudit(q, ctx, w.tenant["B"], member); !denied(err) {
			t.Errorf("an audit row for ANOTHER company was accepted: %v", err)
		}
	})
	w.inSession(member, func(ctx context.Context, q platformdb.Querier) {
		if err := w.tryAudit(q, ctx, w.tenant["A"], other); !denied(err) {
			t.Errorf("an audit row in ANOTHER PERSON's name was accepted: %v", err)
		}
	})
	w.inSession(member, func(ctx context.Context, q platformdb.Querier) {
		if err := w.tryOutbox(q, ctx, w.tenant["B"]); !denied(err) {
			t.Errorf("an outbox event for ANOTHER company was accepted: %v", err)
		}
	})
	// a Hub agent writes for the company they serve and for no other
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if err := w.tryAudit(q, ctx, w.tenant["A"], agent); err != nil {
			t.Errorf("a Hub agent writing an audit row for the company they serve: %v", err)
		}
		if err := w.tryOutbox(q, ctx, w.tenant["A"]); err != nil {
			t.Errorf("a Hub agent enqueueing an event of the company they serve: %v", err)
		}
	})
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if err := w.tryAudit(q, ctx, w.tenant["B"], agent); !denied(err) {
			t.Errorf("a Hub agent wrote an audit row for a company they do not serve: %v", err)
		}
	})
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if err := w.tryOutbox(q, ctx, w.tenant["C"]); !denied(err) {
			t.Errorf("a Hub agent enqueued an event of a company they do not serve: %v", err)
		}
	})
	// no company at all is allowed for the person's own events, and the actor still has to be the person
	w.inSession(member, func(ctx context.Context, q platformdb.Querier) {
		if err := w.tryAudit(q, ctx, nil, member); err != nil {
			t.Errorf("a company-less audit row of the person: %v", err)
		}
		if err := w.tryAudit(q, ctx, nil, nil); err != nil {
			t.Errorf("an actor-less audit row: %v", err)
		}
	})
	w.inSession(member, func(ctx context.Context, q platformdb.Querier) {
		if err := w.tryAudit(q, ctx, nil, other); !denied(err) {
			t.Errorf("a company-less audit row in another person's name was accepted: %v", err)
		}
	})
	// a system session (workers, the platform operator) writes for any company in any name
	if err := platformdb.WithTenantSession(w.ctx, w.app, uuid.Nil, true, func(c context.Context) error {
		q := platformdb.QuerierFromContext(c, w.app)
		if err := w.tryAudit(q, c, w.tenant["B"], other); err != nil {
			t.Errorf("a system session writing an audit row: %v", err)
		}
		if err := w.tryOutbox(q, c, w.tenant["B"]); err != nil {
			t.Errorf("a system session enqueueing an event: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTheActingContextOfATransactionCannotBeRepointed(t *testing.T) {
	w := newWorld(t)
	agent := w.hubAgent("agent")
	for _, k := range []string{"A", "B"} {
		w.ceiling(k, provisioning.ServingPresets["atendimento"]...)
		g := w.grantReply(agent, k)
		w.grantKeys(g, provisioning.ServingPresets["atendimento"]...)
	}
	w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
		if !w.enter(ctx, q, agent, "A") {
			t.Fatal("cannot enter A")
		}
		if !w.enter(ctx, q, agent, "A") {
			t.Error("asking again for the SAME Hub and company must stay true (idempotent)")
		}
		if w.enter(ctx, q, agent, "B") {
			t.Error("the acting context of the transaction was re-pointed at another company")
		}
		var tenant *uuid.UUID
		w.must(q.QueryRow(ctx, `SELECT acting_tenant()`).Scan(&tenant))
		if tenant == nil || *tenant != w.tenant["A"] {
			t.Errorf("after the refused second request the acting company is %v; it must still be company A", tenant)
		}
	})
}
