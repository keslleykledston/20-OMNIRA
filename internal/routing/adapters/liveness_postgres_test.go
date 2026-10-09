package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
)

// livenessFixture seeds one tenant with a contact, a round_robin queue and a
// manual queue, ready for conversations to be inserted per test case.
type livenessFixture struct {
	seed, app                *pgxpool.Pool
	tenantID                 uuid.UUID
	contactID                uuid.UUID
	roundRobinQueue, manualQ uuid.UUID
}

func newLivenessFixture(t *testing.T) *livenessFixture {
	t.Helper()
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}
	f := &livenessFixture{
		seed: seed, app: app,
		tenantID:        uuid.New(),
		contactID:       uuid.New(),
		roundRobinQueue: uuid.New(),
		manualQ:         uuid.New(),
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = seed.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, f.tenantID, "B0 liveness")
	must(err)
	_, err = seed.Exec(ctx, `INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'Contato','+5511999990000')`, f.contactID, f.tenantID)
	must(err)
	_, err = seed.Exec(ctx, `INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,'RR','round_robin')`, f.roundRobinQueue, f.tenantID)
	must(err)
	_, err = seed.Exec(ctx, `INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,'Manual','manual')`, f.manualQ, f.tenantID)
	must(err)
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, f.tenantID)
		seed.Close()
		app.Close()
	})
	return f
}

// conversation seeds one conversation and returns its id. retryAt nil means
// SQL NULL (never attempted / not round-robin).
func (f *livenessFixture) conversation(t *testing.T, queueID *uuid.UUID, assignedTo *uuid.UUID, status string, retryAt *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.seed.Exec(context.Background(),
		`INSERT INTO conversations(id,tenant_id,contact_id,queue_id,assigned_to_user_id,status,routing_retry_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7)`,
		id, f.tenantID, f.contactID, queueID, assignedTo, status, retryAt)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *livenessFixture) pendingJobCount(t *testing.T, conversationID uuid.UUID) int {
	t.Helper()
	var n int
	err := f.seed.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='job.routing.assign.v1' AND aggregate_id=$2`,
		f.tenantID, conversationID.String()).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *livenessFixture) retryAt(t *testing.T, conversationID uuid.UUID) *time.Time {
	t.Helper()
	var v *time.Time
	err := f.seed.QueryRow(context.Background(), `SELECT routing_retry_at FROM conversations WHERE id=$1`, conversationID).Scan(&v)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func past() *time.Time {
	t := time.Now().Add(-time.Minute)
	return &t
}

func future() *time.Time {
	t := time.Now().Add(time.Hour)
	return &t
}

func TestRetrigger_DueUnassignedRoundRobin_IsRetriggered(t *testing.T) {
	f := newLivenessFixture(t)
	repo := NewPostgresLivenessRepository(f.app)
	conv := f.conversation(t, &f.roundRobinQueue, nil, "open", past())

	// TEST.3: scoped to this fixture's own tenant — Retrigger(nil, nil, ...)
	// sweeps every tenant in the shared real-Postgres database, so an exact
	// count assertion is only deterministic when scoped. See
	// TestRetrigger_TenantIsolation below, which already proves scoping
	// works; every exact-count test in this file follows that pattern now.
	n, err := repo.Retrigger(context.Background(), &f.tenantID, nil, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 retriggered, got %d", n)
	}
	if got := f.pendingJobCount(t, conv); got != 1 {
		t.Fatalf("expected 1 fresh job.routing.assign.v1, got %d", got)
	}
	if retryAt := f.retryAt(t, conv); retryAt == nil || !retryAt.After(time.Now()) {
		t.Fatalf("expected routing_retry_at bumped into the future, got %v", retryAt)
	}
}

func TestRetrigger_StillWithinBackoffWindow_IsNotRetriggered(t *testing.T) {
	f := newLivenessFixture(t)
	repo := NewPostgresLivenessRepository(f.app)
	conv := f.conversation(t, &f.roundRobinQueue, nil, "open", future())

	n, err := repo.Retrigger(context.Background(), &f.tenantID, nil, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 retriggered (still in-flight), got %d", n)
	}
	if got := f.pendingJobCount(t, conv); got != 0 {
		t.Fatalf("expected no job enqueued for a conversation still within its retry window, got %d", got)
	}
}

func TestRetrigger_AlreadyAssigned_IsNeverRetriggered(t *testing.T) {
	f := newLivenessFixture(t)
	repo := NewPostgresLivenessRepository(f.app)
	someone := uuid.New()
	if _, err := f.seed.Exec(context.Background(), `INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, someone, someone, someone.String()+"@invalid"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, someone) })
	conv := f.conversation(t, &f.roundRobinQueue, &someone, "open", past())

	n, err := repo.Retrigger(context.Background(), &f.tenantID, nil, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 retriggered for an already-assigned conversation, got %d", n)
	}
	if got := f.pendingJobCount(t, conv); got != 0 {
		t.Fatalf("expected no job for an already-assigned conversation, got %d", got)
	}
}

func TestRetrigger_ClosedConversation_IsNeverRetriggered(t *testing.T) {
	f := newLivenessFixture(t)
	repo := NewPostgresLivenessRepository(f.app)
	conv := f.conversation(t, &f.roundRobinQueue, nil, "closed", past())

	n, err := repo.Retrigger(context.Background(), &f.tenantID, nil, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 retriggered for a closed conversation, got %d", n)
	}
	if got := f.pendingJobCount(t, conv); got != 0 {
		t.Fatalf("expected no job for a closed conversation, got %d", got)
	}
}

func TestRetrigger_ManualQueue_IsNeverRetriggered(t *testing.T) {
	f := newLivenessFixture(t)
	repo := NewPostgresLivenessRepository(f.app)
	conv := f.conversation(t, &f.manualQ, nil, "open", past())

	n, err := repo.Retrigger(context.Background(), &f.tenantID, nil, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 retriggered for a manual-mode queue, got %d", n)
	}
	if got := f.pendingJobCount(t, conv); got != 0 {
		t.Fatalf("expected no job for a manual-mode queue conversation, got %d", got)
	}
}

func TestRetrigger_BatchIsBounded(t *testing.T) {
	f := newLivenessFixture(t)
	repo := NewPostgresLivenessRepository(f.app)
	for i := 0; i < 5; i++ {
		f.conversation(t, &f.roundRobinQueue, nil, "open", past())
	}
	n, err := repo.Retrigger(context.Background(), &f.tenantID, nil, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected exactly the bounded limit (2), got %d", n)
	}
}

func TestRetrigger_ProgressGuarantee_RepeatedCallsMakeForwardProgress(t *testing.T) {
	f := newLivenessFixture(t)
	repo := NewPostgresLivenessRepository(f.app)
	convs := map[uuid.UUID]bool{}
	for i := 0; i < 3; i++ {
		convs[f.conversation(t, &f.roundRobinQueue, nil, "open", past())] = true
	}
	seen := map[uuid.UUID]bool{}
	for i := 0; i < 3; i++ {
		n, err := repo.Retrigger(context.Background(), &f.tenantID, nil, 1, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("call %d: expected 1 retriggered, got %d", i, n)
		}
		for id := range convs {
			if !seen[id] {
				if got := f.pendingJobCount(t, id); got == 1 {
					seen[id] = true
					break
				}
			}
		}
	}
	if len(seen) != 3 {
		t.Fatalf("expected all 3 distinct conversations to be retriggered exactly once across 3 calls, got %d distinct: %v", len(seen), seen)
	}
	// A 4th call must find nothing left due — proves no starvation and no re-selection within the backoff.
	n, err := repo.Retrigger(context.Background(), &f.tenantID, nil, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected nothing left due after all 3 were bumped forward, got %d", n)
	}
}

func TestRetrigger_TenantIsolation(t *testing.T) {
	fa := newLivenessFixture(t)
	fb := newLivenessFixture(t)
	convA := fa.conversation(t, &fa.roundRobinQueue, nil, "open", past())
	convB := fb.conversation(t, &fb.roundRobinQueue, nil, "open", past())

	tenantA := fa.tenantID
	n, err := fa.repo().Retrigger(context.Background(), &tenantA, nil, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 retriggered scoped to tenant A, got %d", n)
	}
	if got := fa.pendingJobCount(t, convA); got != 1 {
		t.Fatalf("expected tenant A's conversation to be retriggered, got %d jobs", got)
	}
	if got := fb.pendingJobCount(t, convB); got != 0 {
		t.Fatalf("expected tenant B's conversation to be untouched by a tenant-A-scoped call, got %d jobs", got)
	}
}

func (f *livenessFixture) repo() *PostgresLivenessRepository {
	return NewPostgresLivenessRepository(f.app)
}

func TestRetrigger_QueueScoped_OnlyMatchingQueueRetriggered(t *testing.T) {
	f := newLivenessFixture(t)
	repo := f.repo()
	otherQueue := uuid.New()
	_, err := f.seed.Exec(context.Background(), `INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,'RR2','round_robin')`, otherQueue, f.tenantID)
	if err != nil {
		t.Fatal(err)
	}
	inScope := f.conversation(t, &f.roundRobinQueue, nil, "open", past())
	outOfScope := f.conversation(t, &otherQueue, nil, "open", past())

	scopedQueue := f.roundRobinQueue
	n, err := repo.Retrigger(context.Background(), &f.tenantID, &scopedQueue, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 retriggered scoped to one queue, got %d", n)
	}
	if got := f.pendingJobCount(t, inScope); got != 1 {
		t.Fatalf("expected the in-scope-queue conversation to be retriggered, got %d", got)
	}
	if got := f.pendingJobCount(t, outOfScope); got != 0 {
		t.Fatalf("expected the other queue's conversation to be untouched, got %d", got)
	}
}

func TestActiveQueuesForAgent_ReturnsOnlyEligibleRoundRobinQueues(t *testing.T) {
	f := newLivenessFixture(t)
	ctx := context.Background()
	userID := uuid.New()
	var roleID uuid.UUID
	if err := f.seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL LIMIT 1`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.seed.Exec(ctx, `INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, userID, userID, userID.String()+"@invalid"); err != nil {
		t.Fatal(err)
	}
	var membershipID uuid.UUID
	if err := f.seed.QueryRow(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active') RETURNING id`, f.tenantID, userID, roleID).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	var agentProfileID uuid.UUID
	if err := f.seed.QueryRow(ctx, `INSERT INTO agent_profiles(tenant_id,membership_id,status) VALUES($1,$2,'active') RETURNING id`, f.tenantID, membershipID).Scan(&agentProfileID); err != nil {
		t.Fatal(err)
	}
	// Eligible: round_robin queue, active+available membership.
	if _, err := f.seed.Exec(ctx, `INSERT INTO queue_members(tenant_id,queue_id,user_id,active,available,capacity) VALUES($1,$2,$3,true,true,1)`, f.tenantID, f.roundRobinQueue, userID); err != nil {
		t.Fatal(err)
	}
	// Not eligible: manual-mode queue.
	if _, err := f.seed.Exec(ctx, `INSERT INTO queue_members(tenant_id,queue_id,user_id,active,available,capacity) VALUES($1,$2,$3,true,true,1)`, f.tenantID, f.manualQ, userID); err != nil {
		t.Fatal(err)
	}

	repo := f.repo()
	queueIDs, err := repo.ActiveQueuesForAgent(ctx, f.tenantID, agentProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queueIDs) != 1 || queueIDs[0] != f.roundRobinQueue {
		t.Fatalf("expected exactly [%s], got %v", f.roundRobinQueue, queueIDs)
	}
}

func TestActiveQueuesForAgent_UnavailableMember_IsExcluded(t *testing.T) {
	f := newLivenessFixture(t)
	ctx := context.Background()
	userID := uuid.New()
	var roleID uuid.UUID
	if err := f.seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL LIMIT 1`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.seed.Exec(ctx, `INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, userID, userID, userID.String()+"@invalid"); err != nil {
		t.Fatal(err)
	}
	var membershipID uuid.UUID
	if err := f.seed.QueryRow(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active') RETURNING id`, f.tenantID, userID, roleID).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	var agentProfileID uuid.UUID
	if err := f.seed.QueryRow(ctx, `INSERT INTO agent_profiles(tenant_id,membership_id,status) VALUES($1,$2,'active') RETURNING id`, f.tenantID, membershipID).Scan(&agentProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.seed.Exec(ctx, `INSERT INTO queue_members(tenant_id,queue_id,user_id,active,available,capacity) VALUES($1,$2,$3,true,false,1)`, f.tenantID, f.roundRobinQueue, userID); err != nil {
		t.Fatal(err)
	}

	repo := f.repo()
	queueIDs, err := repo.ActiveQueuesForAgent(ctx, f.tenantID, agentProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queueIDs) != 0 {
		t.Fatalf("expected no eligible queues for an unavailable member, got %v", queueIDs)
	}
}

// Codex H1 / ADR-0038: the liveness sweep does not re-trigger the assignment of a suspended company's conversations, so
// nothing is assigned while it is suspended; after the reactivation the same conversation is picked up.
func TestRetrigger_SuspendedCompany_IsNeverRetriggered(t *testing.T) {
	f := newLivenessFixture(t)
	repo := NewPostgresLivenessRepository(f.app)
	conv := f.conversation(t, &f.roundRobinQueue, nil, "open", past())

	if _, err := f.seed.Exec(context.Background(), `UPDATE tenants SET status='suspended' WHERE id=$1`, f.tenantID); err != nil {
		t.Fatal(err)
	}
	n, err := repo.Retrigger(context.Background(), &f.tenantID, nil, 10, time.Minute)
	if err != nil || n != 0 || f.pendingJobCount(t, conv) != 0 {
		t.Fatalf("a suspended company's conversation was re-triggered: n=%d err=%v jobs=%d", n, err, f.pendingJobCount(t, conv))
	}
	if _, err := f.seed.Exec(context.Background(), `UPDATE tenants SET status='active' WHERE id=$1`, f.tenantID); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.Retrigger(context.Background(), &f.tenantID, nil, 10, time.Minute); err != nil || n != 1 {
		t.Fatalf("after the reactivation: n=%d err=%v, want 1", n, err)
	}
}
