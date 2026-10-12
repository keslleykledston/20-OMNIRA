package adapters_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ADR-0043 (S2): a channel line may name the queue (sector) its new conversations go to. Without one the tenant's default queue
// still decides, exactly as before; a line can never point at another tenant's queue; a queue a line points at cannot be deleted.
func TestRouteNewUsesTheQueueOfTheLineTheConversationCameThrough(t *testing.T) {
	e := newKindEnv(t)
	store := inboxadapters.NewPostgresInboundStore(e.app)
	tenant, other := e.tenant(), e.tenant()
	user := e.member(tenant, "tenant_agent")
	def, sector, foreign := uuid.New(), uuid.New(), uuid.New()
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Triagem','manual',true)`, def, tenant)
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Financeiro','round_robin',false)`, sector, tenant)
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Financeiro','manual',true)`, foreign, other)

	line := func(t *testing.T, tn uuid.UUID, number string, queue any) uuid.UUID {
		t.Helper()
		id := uuid.New()
		e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities,default_queue_id)
			VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]'::jsonb,$4)`, id, tn, number+uuid.NewString(), queue)
		return id
	}
	convOn := func(connection uuid.UUID, phone string) uuid.UUID {
		conv := e.conversation(tenant, "Cliente", "other", phone)
		e.exec(`UPDATE conversations SET channel_connection_id=$2 WHERE id=$1`, conv, connection)
		return conv
	}
	route := func(conv uuid.UUID) {
		t.Helper()
		if err := platformdb.WithTenantSession(e.ctx, e.app, user, false, func(ctx context.Context) error {
			tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
			if err != nil {
				return err
			}
			return store.RouteNew(tenancydomain.WithTenantContext(ctx, tc), conv)
		}); err != nil {
			t.Fatal(err)
		}
	}
	queueOf := func(conv uuid.UUID) *uuid.UUID {
		var q *uuid.UUID
		if err := e.seed.QueryRow(e.ctx, `SELECT queue_id FROM conversations WHERE id=$1`, conv).Scan(&q); err != nil {
			t.Fatal(err)
		}
		return q
	}
	jobs := func(conv uuid.UUID) int {
		var n int
		if err := e.seed.QueryRow(e.ctx, `SELECT count(*) FROM outbox_events WHERE event_type='job.routing.assign.v1' AND aggregate_id=$1`, conv.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	withSector := line(t, tenant, "5592900000001", sector)
	withoutSector := line(t, tenant, "5592900000002", nil)

	a := convOn(withSector, "+5592955550001")
	b := convOn(withoutSector, "+5592955550002")
	c := e.conversation(tenant, "Sem linha", "other", "+5592955550003") // no channel_connection_id at all: the default
	for _, conv := range []uuid.UUID{a, b, c} {
		route(conv)
	}
	if q := queueOf(a); q == nil || *q != sector {
		t.Errorf("a conversation from the line with a sector must go to that sector, got %v", q)
	}
	if jobs(a) != 1 {
		t.Errorf("the sector is round_robin: expected one assign job, got %d", jobs(a))
	}
	for name, conv := range map[string]uuid.UUID{"line without sector": b, "no line": c} {
		if q := queueOf(conv); q == nil || *q != def {
			t.Errorf("%s must keep going to the default queue, got %v", name, q)
		}
		if jobs(conv) != 0 {
			t.Errorf("%s: the default queue is manual, no assign job expected", name)
		}
	}

	// routing happens once: a second RouteNew never moves a conversation that already has a queue
	e.exec(`UPDATE channel_connections SET default_queue_id=$2 WHERE id=$1`, withSector, def)
	route(a)
	if q := queueOf(a); q == nil || *q != sector {
		t.Errorf("an already routed conversation must not move, got %v", q)
	}

	// the database refuses a line pointing at another tenant's queue, and a delete of a queue a line points at
	if _, err := e.seed.Exec(e.ctx, `UPDATE channel_connections SET default_queue_id=$2 WHERE id=$1`, withoutSector, foreign); err == nil {
		t.Error("a line must not accept another tenant's queue")
	}
	e.exec(`UPDATE channel_connections SET default_queue_id=$2 WHERE id=$1`, withoutSector, sector)
	if _, err := e.seed.Exec(e.ctx, `DELETE FROM queues WHERE id=$1`, sector); err == nil {
		t.Error("a queue that a line points at must not be deletable")
	}
}
