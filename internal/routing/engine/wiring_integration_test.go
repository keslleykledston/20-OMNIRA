package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	routingapp "github.com/omnira/omnira/internal/routing/application"
	"github.com/omnira/omnira/internal/routing/engine"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	routingworker "github.com/omnira/omnira/internal/worker/routing"
)

// The whole path a customer message takes in production, minus NATS: ingest routes the conversation (RouteNew, engine on) -> the durable job lands in
// the outbox -> the routing worker's handler runs it -> the central engine decides -> the customer is told the group is closed. It is the proof
// that the pieces are WIRED together, not just correct one by one.
func TestEndToEndFromIngestToTheClosedNoticeOnAManualSector(t *testing.T) {
	e := newEnv(t, nil)
	n := &recNotifier{pool: e.app}
	repo := engine.NewRepository(e.app, nil)
	repo.Engine().WithNotifier(n, t.Logf)
	handler, err := routingworker.NewHandler(routingworker.NewPostgresConversationRunner(e.app), routingapp.NewService(repo))
	if err != nil {
		t.Fatal(err)
	}
	a := e.tenant(t)
	fin := e.queue(t, a, "Financeiro", "manual") // the standard sectors are manual
	e.exec(t, `UPDATE queues SET is_default=true, closed_notice='Estamos fechados. Voltamos {{proxima_abertura}}.' WHERE id=$1`, fin)
	cal := e.calendar(t, a, "Comercial fechado", "24x7", "[]")
	e.exec(t, `UPDATE queues SET calendar_id=$2 WHERE id=$1`, fin, cal)
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at, reason) VALUES ($1,$2,now()-interval '1 hour',now()+interval '2 hours','teste')`, a, cal)
	user := uuid.New()
	e.exec(t, `INSERT INTO users (id, external_subject, email, display_name, status) VALUES ($1,$2,$3,'a','active')`, user, user.String(), user.String()+"@example.com")
	t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user) })
	var role uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL LIMIT 1`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO memberships (tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,'active')`, a, user, role)

	conv := e.conversation(t, a, nil) // arrives with no queue, exactly like a new inbound conversation
	route := func(engineOn bool) {
		t.Helper()
		store := inboxadapters.NewPostgresInboundStore(e.app).WithEngineRouting(engineOn)
		if err := platformdb.WithTenantSession(e.ctx, e.app, user, false, func(ctx context.Context) error {
			tc, err := tenancydomain.NewTenantContext(a, user, tenancydomain.AccessSourceDirect)
			if err != nil {
				return err
			}
			return store.RouteNew(tenancydomain.WithTenantContext(ctx, tc), conv)
		}); err != nil {
			t.Fatal(err)
		}
	}
	route(true)
	var raw []byte
	if err := e.seed.QueryRow(e.ctx, `SELECT json_build_object('aggregate_id', aggregate_id, 'payload', payload)::text::bytea FROM outbox_events WHERE event_type='job.routing.assign.v1' AND aggregate_id=$1`, conv.String()).Scan(&raw); err != nil {
		t.Fatalf("ingest must leave the durable routing job for a manual queue when the engine is on: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("job envelope: %s", raw)
	}
	// the worker runs the job; the handler acknowledges "nobody assigned" as a business outcome (ErrNoEligibleAgent), which the consumer ACKs
	if err := handler.Handle(e.ctx, raw); err != nil && !strings.Contains(err.Error(), "no eligible agent") {
		t.Fatalf("handler: %v", err)
	}
	if len(n.sent) != 1 || n.sent[0].conv != conv || !strings.HasPrefix(n.sent[0].text, "Estamos fechados. Voltamos ") || strings.Contains(n.sent[0].text, "{{") {
		t.Fatalf("the customer is told, once, when the group opens: %+v", n.sent)
	}
	if e.count(t, `SELECT count(*) FROM routing_engine_state WHERE conversation_id=$1 AND state='waiting_open'`, conv) != 1 ||
		e.count(t, `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='open_wakeup' AND state='pending'`, conv) != 1 {
		t.Fatal("the opening is waiting on a durable timer")
	}
	// the same job delivered again (JetStream redelivery) changes nothing and tells nobody twice (the sender's idempotency key does that; the key is stable)
	_ = handler.Handle(e.ctx, raw)
	if len(n.sent) != 2 || n.sent[0].key != n.sent[1].key {
		t.Fatalf("a redelivery repeats the SAME idempotency key: %+v", n.sent)
	}
	if e.owner(t, conv) != nil {
		t.Fatal("nobody is assigned inside a manual queue")
	}
}

// Load shape of a busy morning at the opening: many conversations of a closed manual sector and of an automatic queue are decided at the same
// time by several workers while the timers run. No deadlock, no duplicate notice, no assignment beyond capacity, every closed conversation told once.
func TestConcurrentWorkersTimersAndNoticesDoNotDeadlockNorDuplicate(t *testing.T) {
	e := newEnv(t, nil)
	n := &recNotifier{pool: e.app}
	repo := engine.NewRepository(e.app, nil)
	repo.Engine().WithNotifier(n, func(string, ...any) {})
	handler, err := routingworker.NewHandler(routingworker.NewPostgresConversationRunner(e.app), routingapp.NewService(repo))
	if err != nil {
		t.Fatal(err)
	}
	a := e.tenant(t)
	closedQ := e.queue(t, a, "Financeiro", "manual")
	cal := e.calendar(t, a, "fechado", "24x7", "[]")
	e.exec(t, `UPDATE queues SET calendar_id=$2, closed_notice='Fechado. Voltamos {{proxima_abertura}}.' WHERE id=$1`, closedQ, cal)
	e.exec(t, `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at, reason) VALUES ($1,$2,now()-interval '1 hour',now()+interval '2 hours','teste')`, a, cal)
	rr := e.queue(t, a, "Suporte", "round_robin")
	var users []uuid.UUID
	for i := 0; i < 3; i++ {
		u, _ := e.attendant(t, a, &rr, 5)
		users = append(users, u)
	}
	const perQueue = 40
	var jobs [][]byte
	mk := func(q uuid.UUID) {
		c := e.conversation(t, a, &q)
		raw, _ := json.Marshal(map[string]string{"aggregate_id": c.String()})
		jobs = append(jobs, raw)
	}
	for i := 0; i < perQueue; i++ {
		mk(closedQ)
		mk(rr)
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(jobs)*2)
	sem := make(chan struct{}, 12)
	for _, raw := range jobs {
		wg.Add(1)
		go func(raw []byte) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := handler.Handle(e.ctx, raw); err != nil && !strings.Contains(err.Error(), "no eligible agent") {
				errs <- err
			}
		}(raw)
	}
	stop := make(chan struct{})
	var tw sync.WaitGroup
	tw.Add(1)
	go func() { // the timers run alongside
		defer tw.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := e.eng.RunTimersOnce(e.ctx); err != nil {
					errs <- err
				}
			}
		}
	}()
	wg.Wait()
	close(stop)
	tw.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("worker error under load (a deadlock shows up here as 40P01): %v", err)
	}
	if len(n.sent) != perQueue {
		t.Fatalf("every conversation of the closed sector is told exactly once: %d notices for %d conversations", len(n.sent), perQueue)
	}
	keys := map[string]bool{}
	for _, s := range n.sent {
		keys[s.key] = true
	}
	if len(keys) != perQueue {
		t.Fatalf("one idempotency key per conversation and episode: %d distinct", len(keys))
	}
	for _, u := range users {
		if got := e.count(t, `SELECT count(*) FROM conversations WHERE tenant_id=$1 AND assigned_to_user_id=$2 AND status='open'`, a, u); got > 5 {
			t.Fatalf("agent %s holds %d conversations, capacity is 5", u, got)
		}
	}
	if got := e.count(t, `SELECT count(*) FROM conversations WHERE tenant_id=$1 AND queue_id=$2 AND assigned_to_user_id IS NOT NULL`, a, rr); got != 15 {
		t.Fatalf("the automatic queue fills its three agents to capacity and no further: %d assigned", got)
	}
	if got := e.count(t, `SELECT count(*) FROM routing_engine_state WHERE tenant_id=$1 AND state='waiting_open'`, a); got != perQueue {
		t.Fatalf("each closed conversation is waiting for the opening: %d", got)
	}
}
