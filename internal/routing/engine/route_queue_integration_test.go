package engine_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	"github.com/omnira/omnira/internal/routing/engine"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// A person sends a conversation from a MANUAL queue to a queue that distributes: the move writes the queue and queues the routing job; the engine then treats it as a
// new episode of the new queue and places it with one of that queue's attendants. Nothing of the first queue's state leaks into the second.
func TestAConversationSentToAnotherQueueIsDistributedThereByTheEngine(t *testing.T) {
	e := newEnv(t, nil)
	tenant := e.tenant(t)
	manual := e.queue(t, tenant, "Triagem", "manual")
	vendas := e.queue(t, tenant, "Vendas", "round_robin")
	seller, _ := e.attendant(t, tenant, &vendas, 5)
	person, _ := e.attendant(t, tenant, &manual, 5) // the person who decides to send it away
	conv := e.conversation(t, tenant, &manual)

	// in the manual queue the engine leaves it alone
	if d, err := e.decide(t, tenant, conv, ""); err != nil || d.Kind == engine.AssignAgent {
		t.Fatalf("a manual queue must not be auto-assigned: %+v %v", d, err)
	}
	episodeBefore := e.count(t, `SELECT episode FROM routing_engine_state WHERE tenant_id=$1 AND conversation_id=$2`, tenant, conv)

	err := platformdb.WithTenantSession(e.ctx, e.app, person, false, func(c context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, person, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		move, found, err := routingadapters.NewPostgresConversationAssigner(e.app).MoveToQueue(tenancydomain.WithTenantContext(c, tc), conv, vendas, person)
		if err != nil || !found || !move.Changed {
			t.Fatalf("move: %+v found=%v err=%v", move, found, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='job.routing.assign.v1' AND aggregate_id=$2`, tenant, conv.String()); n != 1 {
		t.Fatalf("routing jobs after the move: %d, want 1", n)
	}

	d, err := e.decide(t, tenant, conv, "")
	if err != nil || d.Kind != engine.AssignAgent || d.AssignedUser != seller {
		t.Fatalf("the engine must place it with the seller of the new queue: %+v %v", d, err)
	}
	if got := e.owner(t, conv); got == nil || *got != seller {
		t.Fatalf("owner = %v, want the seller", got)
	}
	if episodeAfter := e.count(t, `SELECT episode FROM routing_engine_state WHERE tenant_id=$1 AND conversation_id=$2`, tenant, conv); episodeAfter <= episodeBefore {
		t.Errorf("episode %d -> %d: moving to another queue is a new episode", episodeBefore, episodeAfter)
	}
	// and the audit trail of where it was is intact
	if n := e.count(t, `SELECT count(*) FROM assignment_events WHERE tenant_id=$1 AND conversation_id=$2 AND to_user_id=$3`, tenant, conv, seller); n != 1 {
		t.Errorf("assignment history of the seller: %d", n)
	}
	_ = uuid.Nil
}
