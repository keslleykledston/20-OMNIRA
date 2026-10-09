package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func (e *env) session2(tenant uuid.UUID) application.TenantSession {
	return func(ctx context.Context, tenantID uuid.UUID, fn func(context.Context) error) error {
		return platformdb.WithSystemTenantSession(ctx, e.app, tenantID, fn)
	}
}

func fastConfig() application.JobRunnerConfig {
	return application.JobRunnerConfig{BatchSize: 20, Lease: time.Minute, MaxAttempts: 4, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
}

type pipelineFunc func(ctx context.Context, job ports.Job) error

func (f pipelineFunc) Process(ctx context.Context, job ports.Job) error { return f(ctx, job) }

func jobsFor(e *env, tenant uuid.UUID) int {
	return e.count(`SELECT count(*) FROM intelligence_jobs WHERE tenant_id=$1`, tenant)
}

func jobState(e *env, id uuid.UUID) (state string, attempts int, class *string) {
	_ = e.seed.QueryRow(e.ctx, `SELECT state, attempts, last_error_class FROM intelligence_jobs WHERE id=$1`, id).Scan(&state, &attempts, &class)
	return
}

func TestPersistedMessagesEmitAMinimalOutboxEventInTheSameTransaction(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	g := e.group(a.id)
	author := e.participant(a.id, g, "111@lid", "João")
	secret := "SEGREDO-NUNCA-NO-EVENTO"
	in := e.message(a.id, a.conversation, secret)
	_ = e.groupMessage(a.id, g, author, secret, nil)
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES(gen_random_uuid(),$1,$2,'outbound','text',$3,'sent')`, a.id, a.conversation, secret)
	t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM outbox_events WHERE tenant_id=$1`, a.id) })

	if n := e.count(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='job.inbox.message_persisted.v1'`, a.id); n != 2 {
		t.Fatalf("events = %d, want 2 (one inbound conversation message, one group message; the outbound is not an event)", n)
	}
	if n := e.count(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND payload::text LIKE '%' || $2 || '%'`, a.id, secret); n != 0 {
		t.Fatal("the event must carry only references, never the message content")
	}
	var kind, aggregate string
	_ = e.seed.QueryRow(e.ctx, `SELECT payload->>'kind', aggregate_id FROM outbox_events WHERE tenant_id=$1 AND payload->>'kind'='conversation'`, a.id).Scan(&kind, &aggregate)
	if aggregate != in.String() {
		t.Fatalf("aggregate_id = %s, want the message id %s", aggregate, in)
	}
	var keys int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM outbox_events o, jsonb_object_keys(o.payload) k WHERE o.tenant_id=$1 AND o.payload->>'kind'='conversation'`, a.id).Scan(&keys)
	if keys != 6 {
		t.Fatalf("payload keys = %d, want exactly event_id, tenant_id, message_id, kind, container_id, occurred_at", keys)
	}
}

func TestEnsureFromEventIsIdempotentAndTakesTheTenantFromStoredState(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	msgA := e.message(a.id, a.conversation, "x")
	store := NewPostgresJobStore(e.app)
	ref := cnv(msgA)
	for i := 0; i < 5; i++ { // the same event, delivered again and again
		created, err := store.EnsureFromEvent(e.ctx, ref, application.PipelineVersion)
		if err != nil || created != (i == 0) {
			t.Fatalf("delivery %d: created=%v err=%v", i, created, err)
		}
	}
	if jobsFor(e, a.id) != 1 {
		t.Fatalf("jobs = %d, want exactly 1 however many times the event arrives", jobsFor(e, a.id))
	}
	if e.count(`SELECT count(*) FROM intelligence_jobs WHERE message_id=$1 AND tenant_id=$2`, msgA, a.id) != 1 || jobsFor(e, b.id) != 0 {
		t.Fatal("the job belongs to the message's tenant, whatever the event said")
	}
	// a new pipeline version is a new job for the same message
	if created, err := store.EnsureFromEvent(e.ctx, ref, "v2"); err != nil || !created {
		t.Fatalf("v2: %v %v", created, err)
	}
	if _, err := store.EnsureFromEvent(e.ctx, cnv(uuid.New()), application.PipelineVersion); !errors.Is(err, domain.ErrReferenceNotFound) {
		t.Fatalf("an event for a message that does not exist: %v", err)
	}
}

func TestRunnerProcessesAJobThroughTheRoutingPipelineOnce(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	flags := application.DefaultFlags()
	flags.TopicAutoRoutingEnabled = true
	routing := application.NewRoutingService(NewPostgresRoutingRepository(e.app), NewPostgresTopicRepository(e.app), flags, domain.DefaultRoutingConfig(), nil)
	store := NewPostgresJobStore(e.app)
	runner := application.NewJobRunner(store, application.RoutingPipeline{Routing: routing}, e.session2(a.id), fastConfig(), nil)
	msg := e.message(a.id, a.conversation, "pedido 6001 não chegou")
	if _, err := store.EnsureFromEvent(e.ctx, cnv(msg), application.PipelineVersion); err != nil {
		t.Fatal(err)
	}
	if n, err := runner.ProcessOnce(e.ctx); err != nil || n != 1 {
		t.Fatalf("claimed %d err %v", n, err)
	}
	if n := e.count(`SELECT count(*) FROM intelligence_jobs WHERE tenant_id=$1 AND state='completed' AND attempts=1`, a.id); n != 1 {
		t.Fatal("the job must complete on the first attempt")
	}
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1`, a.id) != 1 || e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2`, a.id, msg) != 1 {
		t.Fatal("the message must have been routed into one new topic")
	}
	if n, _ := runner.ProcessOnce(e.ctx); n != 0 {
		t.Fatalf("a completed job must not be claimed again, claimed %d", n)
	}
}

func TestACrashBeforeProcessingIsRecoveredWhenTheLeaseExpires(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	msg := e.message(a.id, a.conversation, "x")
	store := NewPostgresJobStore(e.app)
	if _, err := store.EnsureFromEvent(e.ctx, cnv(msg), application.PipelineVersion); err != nil {
		t.Fatal(err)
	}
	// worker 1 claims the job and "crashes": it never completes it
	first, err := store.Claim(e.ctx, 5, time.Minute)
	if err != nil || len(first) != 1 || first[0].Attempt != 1 {
		t.Fatalf("first claim: %+v %v", first, err)
	}
	// while the lease is valid nobody else may take it
	if again, _ := store.Claim(e.ctx, 5, time.Minute); len(again) != 0 {
		t.Fatalf("a leased job was claimed twice: %+v", again)
	}
	// the lease runs out
	e.exec(`UPDATE intelligence_jobs SET locked_until = now() - interval '1 second' WHERE id=$1`, first[0].ID)
	var runs int32
	runner := application.NewJobRunner(store, pipelineFunc(func(ctx context.Context, j ports.Job) error { atomic.AddInt32(&runs, 1); return nil }), e.session2(a.id), fastConfig(), nil)
	if n, err := runner.ProcessOnce(e.ctx); err != nil || n != 1 || runs != 1 {
		t.Fatalf("recovery: claimed %d runs %d err %v", n, runs, err)
	}
	if state, attempts, _ := jobState(e, first[0].ID); state != "completed" || attempts != 2 {
		t.Fatalf("state %s attempts %d, want completed on attempt 2", state, attempts)
	}
	// the crashed worker comes back from the dead and tries to finish the job it no longer owns: refused
	if ok, err := store.Complete(e.ctx, first[0]); err != nil || ok {
		t.Fatalf("a stale claim must not move the job: ok=%v err=%v", ok, err)
	}
	if ok, _ := store.Dead(e.ctx, first[0], "stale"); ok {
		t.Fatal("a stale claim must not kill the job either")
	}
}

func TestACrashAfterProcessingNeverDuplicatesTheWork(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	flags := application.DefaultFlags()
	flags.TopicAutoRoutingEnabled = true
	routing := application.NewRoutingService(NewPostgresRoutingRepository(e.app), NewPostgresTopicRepository(e.app), flags, domain.DefaultRoutingConfig(), nil)
	store := NewPostgresJobStore(e.app)
	msg := e.message(a.id, a.conversation, "pedido 6002 não chegou")
	if _, err := store.EnsureFromEvent(e.ctx, cnv(msg), application.PipelineVersion); err != nil {
		t.Fatal(err)
	}
	// worker 1 does the work (routing) and dies before Complete
	claimed, _ := store.Claim(e.ctx, 5, time.Minute)
	if len(claimed) != 1 {
		t.Fatal("claim")
	}
	if err := e.session2(a.id)(e.ctx, a.id, func(ctx context.Context) error {
		return application.RoutingPipeline{Routing: routing}.Process(ctx, claimed[0])
	}); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE intelligence_jobs SET locked_until = now() - interval '1 second' WHERE id=$1`, claimed[0].ID)
	// worker 2 takes over and runs the same job again
	runner := application.NewJobRunner(store, application.RoutingPipeline{Routing: routing}, e.session2(a.id), fastConfig(), nil)
	if n, err := runner.ProcessOnce(e.ctx); err != nil || n != 1 {
		t.Fatalf("takeover: %d %v", n, err)
	}
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1`, a.id) != 1 ||
		e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1`, a.id) != 1 ||
		e.count(`SELECT count(*) FROM routing_decisions WHERE tenant_id=$1 AND applied`, a.id) != 1 {
		t.Fatal("running the same job twice must not create a second topic, link or decision")
	}
	if state, attempts, _ := jobState(e, claimed[0].ID); state != "completed" || attempts != 2 {
		t.Fatalf("state %s attempts %d", state, attempts)
	}
}

func TestConcurrentWorkersProcessEveryJobExactlyOnce(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresJobStore(e.app)
	var ids []uuid.UUID
	for i := 0; i < 12; i++ {
		m := e.message(a.id, a.conversation, fmt.Sprintf("m%d", i))
		ids = append(ids, m)
		if _, err := store.EnsureFromEvent(e.ctx, cnv(m), application.PipelineVersion); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	pipe := pipelineFunc(func(ctx context.Context, j ports.Job) error {
		mu.Lock()
		seen[j.ID]++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return nil
	})
	var wg sync.WaitGroup
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := fastConfig()
			cfg.BatchSize = 3
			r := application.NewJobRunner(store, pipe, e.session2(a.id), cfg, nil)
			for i := 0; i < 6; i++ {
				_, _ = r.ProcessOnce(e.ctx)
			}
		}()
	}
	wg.Wait()
	if len(seen) != len(ids) {
		t.Fatalf("processed %d distinct jobs, want %d", len(seen), len(ids))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("job %s was processed %d times", id, n)
		}
	}
	if e.count(`SELECT count(*) FROM intelligence_jobs WHERE tenant_id=$1 AND state='completed'`, a.id) != len(ids) {
		t.Fatal("every job must end completed")
	}
}

func TestFailuresRetryThenSucceedAndPoisonEndsDead(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresJobStore(e.app)
	mk := func(body string) uuid.UUID {
		m := e.message(a.id, a.conversation, body)
		if _, err := store.EnsureFromEvent(e.ctx, cnv(m), application.PipelineVersion); err != nil {
			t.Fatal(err)
		}
		var id uuid.UUID
		_ = e.seed.QueryRow(e.ctx, `SELECT id FROM intelligence_jobs WHERE message_id=$1`, m).Scan(&id)
		return id
	}
	flaky, poison, permanent := mk("flaky"), mk("poison"), mk("permanent")
	var flakyRuns int32
	pipe := pipelineFunc(func(ctx context.Context, j ports.Job) error {
		switch j.ID {
		case flaky:
			if atomic.AddInt32(&flakyRuns, 1) <= 2 {
				return errors.New("provider timeout")
			}
			return nil
		case poison:
			return errors.New("always broken")
		default:
			return application.ErrPermanent
		}
	})
	runner := application.NewJobRunner(store, pipe, e.session2(a.id), fastConfig(), nil)
	for i := 0; i < 8; i++ {
		_, _ = runner.ProcessOnce(e.ctx)
		time.Sleep(3 * time.Millisecond)
	}
	if st, at, _ := jobState(e, flaky); st != "completed" || at != 3 {
		t.Errorf("flaky: %s after %d attempts, want completed on the 3rd", st, at)
	}
	if st, at, class := jobState(e, poison); st != "dead" || at != 4 || class == nil || *class != "retries_exhausted" {
		t.Errorf("poison: %s after %d attempts class %v, want dead after MaxAttempts", st, at, class)
	}
	if st, at, class := jobState(e, permanent); st != "dead" || at != 1 || class == nil || *class != "permanent" {
		t.Errorf("permanent: %s after %d attempts class %v, want dead on the first", st, at, class)
	}
	if n, _ := runner.ProcessOnce(e.ctx); n != 0 {
		t.Errorf("dead and completed jobs must not be claimed, claimed %d", n)
	}
}

func TestAPanicOnOneJobIsIsolatedAndTheRunnerKeepsGoing(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresJobStore(e.app)
	bad, good := e.message(a.id, a.conversation, "bad"), e.message(a.id, a.conversation, "good")
	for _, m := range []uuid.UUID{bad, good} {
		_, _ = store.EnsureFromEvent(e.ctx, cnv(m), application.PipelineVersion)
	}
	var goodRan int32
	pipe := pipelineFunc(func(ctx context.Context, j ports.Job) error {
		if j.Ref.ID == bad {
			panic("boom")
		}
		atomic.AddInt32(&goodRan, 1)
		return nil
	})
	if _, err := application.NewJobRunner(store, pipe, e.session2(a.id), fastConfig(), nil).ProcessOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	if goodRan != 1 || e.count(`SELECT count(*) FROM intelligence_jobs WHERE tenant_id=$1 AND state='dead'`, a.id) != 1 || e.count(`SELECT count(*) FROM intelligence_jobs WHERE tenant_id=$1 AND state='completed'`, a.id) != 1 {
		t.Fatal("the panicking job must be dead and the other one completed")
	}
}

func TestJobsRunInsideTheirOwnTenantAndOperatorsCannotTouchThem(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	agentA := e.member(a.id, "tenant_agent")
	store := NewPostgresJobStore(e.app)
	ma, mb := e.message(a.id, a.conversation, "a"), e.message(b.id, b.conversation, "b")
	for _, m := range []uuid.UUID{ma, mb} {
		_, _ = store.EnsureFromEvent(e.ctx, cnv(m), application.PipelineVersion)
	}
	var mu sync.Mutex
	wrong := 0
	pipe := pipelineFunc(func(ctx context.Context, j ports.Job) error {
		tc, err := tenancydomain.FromContext(ctx)
		if err != nil || tc.TenantID != j.TenantID {
			mu.Lock()
			wrong++
			mu.Unlock()
		}
		return nil
	})
	session := func(ctx context.Context, tenantID uuid.UUID, fn func(context.Context) error) error {
		return platformdb.WithSystemTenantSession(ctx, e.app, tenantID, fn)
	}
	if _, err := application.NewJobRunner(store, pipe, session, fastConfig(), nil).ProcessOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	if wrong != 0 {
		t.Fatalf("%d job(s) ran under another tenant's context", wrong)
	}
	// an operator session of tenant A sees A's job state, not B's, and cannot move any job
	e.session(a.id, agentA, func(ctx context.Context) {
		var n int
		_ = NewPostgresTopicRepository(e.app).q(ctx).QueryRow(ctx, `SELECT count(*) FROM intelligence_jobs`).Scan(&n)
		if n != 1 {
			t.Errorf("an operator of A sees %d jobs, want only A's 1", n)
		}
		tag, _ := NewPostgresTopicRepository(e.app).q(ctx).Exec(ctx, `UPDATE intelligence_jobs SET state='pending'`)
		if tag.RowsAffected() != 0 {
			t.Error("an operator session must not change jobs")
		}
	})
	if !strings.Contains("completed", "complete") || e.count(`SELECT count(*) FROM intelligence_jobs WHERE state='completed' AND tenant_id IN ($1,$2)`, a.id, b.id) != 2 {
		t.Fatal("both tenants' jobs must have completed under their own tenant")
	}
}

// The dangerous race: worker 1 stalls past its lease, worker 2 takes the job and is still working when worker 1 wakes up
// and tries to finish, fail or kill it. Only the holder of the CURRENT claim may move a running job.
func TestAStalledWorkerCannotMoveAJobAnotherWorkerNowHolds(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	store := NewPostgresJobStore(e.app)
	msg := e.message(a.id, a.conversation, "x")
	if _, err := store.EnsureFromEvent(e.ctx, cnv(msg), application.PipelineVersion); err != nil {
		t.Fatal(err)
	}
	w1, _ := store.Claim(e.ctx, 1, time.Minute)
	e.exec(`UPDATE intelligence_jobs SET locked_until = now() - interval '1 second' WHERE id=$1`, w1[0].ID)
	w2, _ := store.Claim(e.ctx, 1, time.Minute) // worker 2 holds attempt 2 and is still running
	if len(w2) != 1 || w2[0].Attempt != 2 {
		t.Fatalf("takeover: %+v", w2)
	}
	if ok, err := store.Complete(e.ctx, w1[0]); err != nil || ok {
		t.Fatalf("a stalled worker completed a job it no longer holds: ok=%v err=%v", ok, err)
	}
	if ok, _ := store.Retry(e.ctx, w1[0], "transient", time.Second); ok {
		t.Fatal("a stalled worker rescheduled a job it no longer holds")
	}
	if ok, _ := store.Dead(e.ctx, w1[0], "stale"); ok {
		t.Fatal("a stalled worker killed a job it no longer holds")
	}
	if state, attempts, _ := jobState(e, w2[0].ID); state != "running" || attempts != 2 {
		t.Fatalf("the job must still be running under worker 2: %s attempt %d", state, attempts)
	}
	if ok, err := store.Complete(e.ctx, w2[0]); err != nil || !ok {
		t.Fatalf("the current holder must be able to complete: ok=%v err=%v", ok, err)
	}
}

// Codex H2 / ADR-0038: a suspended company's AI jobs are not claimed (no pipeline, no cost, no tickets or summaries while it
// is suspended); the pending job is untouched and is taken after the reactivation.
func TestASuspendedCompanysJobsAreNotClaimedUntilReactivated(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	msg := e.message(a.id, a.conversation, "x")
	store := NewPostgresJobStore(e.app)
	if _, err := store.EnsureFromEvent(e.ctx, cnv(msg), application.PipelineVersion); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE tenants SET status='suspended' WHERE id=$1`, a.id)
	claimed, err := store.Claim(e.ctx, 1000, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range claimed {
		if j.TenantID == a.id {
			t.Fatalf("a suspended company's job was claimed: %+v", j)
		}
	}
	var state string
	_ = e.seed.QueryRow(e.ctx, `SELECT state FROM intelligence_jobs WHERE tenant_id=$1`, a.id).Scan(&state)
	if state != "pending" {
		t.Fatalf("the job must stay pending, got %q", state)
	}
	e.exec(`UPDATE tenants SET status='active' WHERE id=$1`, a.id)
	claimed, err = store.Claim(e.ctx, 1000, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range claimed {
		found = found || j.TenantID == a.id
	}
	if !found {
		t.Fatal("after the reactivation the job must be claimed")
	}
}

func TestAnAIJobClaimDoesNotRaceASuspensionInFlight(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	msg := e.message(a.id, a.conversation, "x")
	store := NewPostgresJobStore(e.app)
	if _, err := store.EnsureFromEvent(e.ctx, cnv(msg), application.PipelineVersion); err != nil {
		t.Fatal(err)
	}
	tx, err := e.seed.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(e.ctx) }()
	if _, err := tx.Exec(e.ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, `UPDATE tenants SET status='suspended' WHERE id=$1`, a.id); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(e.ctx, 1000, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range claimed {
		if j.TenantID == a.id {
			t.Fatal("the claim raced a suspension in flight")
		}
	}
	if err := tx.Rollback(e.ctx); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.Claim(e.ctx, 1000, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range claimed {
		found = found || j.TenantID == a.id
	}
	if !found {
		t.Fatal("once the suspension was rolled back the job must be claimed")
	}
}
