package delivery_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
	"github.com/omnira/omnira/internal/worker/delivery"
)

// reconcileEnv is a minimal fixture for exercising PostgresReconciliationStore
// directly against real Postgres: one tenant, one contact, and helpers to
// seed exactly the messages/outbox_events rows each test needs. Every
// timestamp is seeded explicitly (INSERT ... published_at = now() - $interval)
// rather than relying on real sleeps, so eligibility windows are exact and
// tests stay fast.
type reconcileEnv struct {
	t      *testing.T
	seed   *pgxpool.Pool
	tenant uuid.UUID
}

func newReconcileEnv(t *testing.T) *reconcileEnv {
	t.Helper()
	seedURL, _ := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	e := &reconcileEnv{t: t, seed: seed, tenant: uuid.New()}
	e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, e.tenant, e.tenant.String())
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, e.tenant)
		seed.Close()
	})
	return e
}

func (e *reconcileEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// seedMessage creates a minimal outbound message (contact + conversation +
// message) with the given status and returns its id.
func (e *reconcileEnv) seedMessage(status string) uuid.UUID {
	e.t.Helper()
	contact := uuid.New()
	conv := uuid.New()
	msg := uuid.New()
	phone := fmt.Sprintf("+5511%09d", (time.Now().UnixNano()/1000)%1000000000)
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'reconcile-fixture',$3)`, contact, e.tenant, phone)
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conv, e.tenant, contact)
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status)
	        VALUES($1,$2,$3,'outbound','text','reconcile fixture body',$4)`, msg, e.tenant, conv, status)
	return msg
}

// seedSendEvent inserts one job.channel.send_text.v1 outbox_events row for
// messageID. If publishedAgo is non-nil, published_at is set to that far in
// the past; if nil, published_at stays NULL (an unpublished intent).
func (e *reconcileEnv) seedSendEvent(messageID uuid.UUID, publishedAgo *time.Duration) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	if publishedAgo == nil {
		e.exec(`INSERT INTO outbox_events(id,tenant_id,event_type,aggregate_type,aggregate_id,payload)
		        VALUES($1,$2,'job.channel.send_text.v1','message',$3,'{}'::jsonb)`,
			id, e.tenant, messageID.String())
		return id
	}
	e.exec(`INSERT INTO outbox_events(id,tenant_id,event_type,aggregate_type,aggregate_id,payload,published_at)
	        VALUES($1,$2,'job.channel.send_text.v1','message',$3,'{}'::jsonb, now() - $4::interval)`,
		id, e.tenant, messageID.String(), fmt.Sprintf("%d seconds", int(publishedAgo.Seconds())))
	return id
}

func (e *reconcileEnv) messageStatus(id uuid.UUID) string {
	e.t.Helper()
	var status string
	if err := e.seed.QueryRow(context.Background(), `SELECT status FROM messages WHERE id=$1`, id).Scan(&status); err != nil {
		e.t.Fatal(err)
	}
	return status
}

func (e *reconcileEnv) unpublishedCount(messageID uuid.UUID) int {
	e.t.Helper()
	var n int
	if err := e.seed.QueryRow(context.Background(), `
		SELECT count(*) FROM outbox_events
		WHERE aggregate_type='message' AND aggregate_id=$1
		  AND event_type='job.channel.send_text.v1' AND published_at IS NULL`,
		messageID.String()).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *reconcileEnv) totalEventCount(messageID uuid.UUID) int {
	e.t.Helper()
	var n int
	if err := e.seed.QueryRow(context.Background(), `
		SELECT count(*) FROM outbox_events
		WHERE aggregate_type='message' AND aggregate_id=$1 AND event_type='job.channel.send_text.v1'`,
		messageID.String()).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

const (
	testMaxAge = 1 * time.Hour
	testGrace  = 0 * time.Second // isolate the MaxAge boundary itself in tests; production uses jobsstream.ReconciliationGrace
)

// --- Section 12: status exclusions -----------------------------------------

func TestReconcile_OldQueued_IsEligible(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	old := 2 * time.Hour
	e.seedSendEvent(msg, &old)

	store := delivery.NewPostgresReconciliationStore(e.seed)
	n, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("created=%d, want 1", n)
	}
	if got := e.totalEventCount(msg); got != 2 {
		t.Fatalf("total events=%d, want 2 (original + reconciled)", got)
	}
	if got := e.unpublishedCount(msg); got != 1 {
		t.Fatalf("unpublished events=%d, want exactly 1 (the new one)", got)
	}
}

func TestReconcile_YoungQueued_NotEligible(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	young := 10 * time.Minute
	e.seedSendEvent(msg, &young)

	store := delivery.NewPostgresReconciliationStore(e.seed)
	n, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("created=%d, want 0 (still within MaxAge+grace)", n)
	}
}

func testStatusNeverEligible(t *testing.T, status string) {
	e := newReconcileEnv(t)
	msg := e.seedMessage(status)
	old := 2 * time.Hour
	e.seedSendEvent(msg, &old)

	store := delivery.NewPostgresReconciliationStore(e.seed)
	n, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("status=%s: created=%d, want 0 — never auto-reconciled", status, n)
	}
}

func TestReconcile_Sent_NeverEligible(t *testing.T)      { testStatusNeverEligible(t, "sent") }
func TestReconcile_Failed_NeverEligible(t *testing.T)    { testStatusNeverEligible(t, "failed") }
func TestReconcile_Uncertain_NeverEligible(t *testing.T) { testStatusNeverEligible(t, "uncertain") }

// --- Section 13: unpublished-intent guard -----------------------------------

func TestReconcile_ExistingUnpublishedIntent_BlocksReconciliation(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	old := 2 * time.Hour
	e.seedSendEvent(msg, &old) // stranded original
	e.seedSendEvent(msg, nil)  // an unpublished intent already exists (e.g. publisher retrying)

	store := delivery.NewPostgresReconciliationStore(e.seed)
	n, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("created=%d, want 0 — an unpublished intent already exists, ordinary publisher owns it", n)
	}
	if got := e.totalEventCount(msg); got != 2 {
		t.Fatalf("total events=%d, want 2 (unchanged)", got)
	}
}

// --- Section 18: multiple historical events, eligibility based on the LATEST ---

func TestReconcile_EligibilityUsesLatestPublishedIntent(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	veryOld := 5 * time.Hour
	recentReconciled := 10 * time.Minute // this one is still young
	e.seedSendEvent(msg, &veryOld)
	e.seedSendEvent(msg, &recentReconciled)

	store := delivery.NewPostgresReconciliationStore(e.seed)
	n, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("created=%d, want 0 — the LATEST published intent (10m ago) is still within MaxAge, the 5h-old one must not be used", n)
	}
}

func TestReconcile_SecondCycle_EligibleOnceLatestAlsoExpires(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	veryOld := 5 * time.Hour
	alsoOldNow := 2 * time.Hour // simulates time having passed since the first reconciliation
	e.seedSendEvent(msg, &veryOld)
	e.seedSendEvent(msg, &alsoOldNow)

	store := delivery.NewPostgresReconciliationStore(e.seed)
	n, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("created=%d, want 1 — the latest intent has now also expired", n)
	}
	if got := e.totalEventCount(msg); got != 3 {
		t.Fatalf("total events=%d, want 3", got)
	}
}

// --- Section 16: MaxAge<=0 disables reconciliation --------------------------

func TestReconcile_MaxAgeZero_NeverReconciles(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	ancient := 24 * time.Hour
	e.seedSendEvent(msg, &ancient)

	store := delivery.NewPostgresReconciliationStore(e.seed)
	n, err := store.ReconcileStrandedQueuedSends(context.Background(), 0, testGrace, 200)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("created=%d, want 0 — MaxAge<=0 must disable reconciliation regardless of how old the intent is", n)
	}

	r := delivery.NewReconciler(store, 0, testGrace)
	if r.ShouldRun() {
		t.Fatal("Reconciler.ShouldRun() = true, want false when MaxAge<=0")
	}
}

// --- Section 20/21: reserved provider id and duplicate-delivery lineage ----

func TestReconcile_DoesNotTouchReservedProviderMessageID(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	e.exec(`UPDATE messages SET reserved_provider_message_id=$2 WHERE id=$1`, msg, "already-reserved-"+msg.String())
	old := 2 * time.Hour
	e.seedSendEvent(msg, &old)

	store := delivery.NewPostgresReconciliationStore(e.seed)
	if _, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200); err != nil {
		t.Fatal(err)
	}
	var reserved string
	if err := e.seed.QueryRow(context.Background(), `SELECT reserved_provider_message_id FROM messages WHERE id=$1`, msg).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != "already-reserved-"+msg.String() {
		t.Fatalf("reserved_provider_message_id changed to %q, want it untouched", reserved)
	}
}

func TestReconcile_NewEventReferencesLatestPreviousAsCausation(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	old := 2 * time.Hour
	prevEventID := e.seedSendEvent(msg, &old)

	store := delivery.NewPostgresReconciliationStore(e.seed)
	if _, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200); err != nil {
		t.Fatal(err)
	}
	var causation string
	if err := e.seed.QueryRow(context.Background(), `
		SELECT causation_id FROM outbox_events
		WHERE aggregate_type='message' AND aggregate_id=$1 AND event_type='job.channel.send_text.v1' AND published_at IS NULL`,
		msg.String()).Scan(&causation); err != nil {
		t.Fatal(err)
	}
	if causation != prevEventID.String() {
		t.Fatalf("causation_id=%q, want the previous event id %q", causation, prevEventID)
	}
}

func TestReconcile_OriginalEventNeverModified(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	old := 2 * time.Hour
	origID := e.seedSendEvent(msg, &old)
	var beforePublishedAt time.Time
	if err := e.seed.QueryRow(context.Background(), `SELECT published_at FROM outbox_events WHERE id=$1`, origID).Scan(&beforePublishedAt); err != nil {
		t.Fatal(err)
	}

	store := delivery.NewPostgresReconciliationStore(e.seed)
	if _, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200); err != nil {
		t.Fatal(err)
	}

	var afterPublishedAt time.Time
	var attempts int
	if err := e.seed.QueryRow(context.Background(), `SELECT published_at, attempts FROM outbox_events WHERE id=$1`, origID).Scan(&afterPublishedAt, &attempts); err != nil {
		t.Fatal(err)
	}
	if !afterPublishedAt.Equal(beforePublishedAt) {
		t.Fatalf("original published_at changed: before=%v after=%v", beforePublishedAt, afterPublishedAt)
	}
	if attempts != 0 {
		t.Fatalf("original attempts=%d, want 0 (never touched)", attempts)
	}
}

// --- Section 14: concurrent reconcilers produce exactly one effective event ---

func TestReconcile_ConcurrentReconcilers_OneEffectiveEvent(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	old := 2 * time.Hour
	e.seedSendEvent(msg, &old)

	seedURL, _ := testhelpers.RequireIntegrationDatabase(t)
	poolA, err := pgxpool.New(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer poolA.Close()
	poolB, err := pgxpool.New(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer poolB.Close()
	storeA := delivery.NewPostgresReconciliationStore(poolA)
	storeB := delivery.NewPostgresReconciliationStore(poolB)

	results := make(chan int, 2)
	errs := make(chan error, 2)
	run := func(s *delivery.PostgresReconciliationStore) {
		n, err := s.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200)
		results <- n
		errs <- err
	}
	go run(storeA)
	go run(storeB)

	total := 0
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		total += <-results
	}
	if total != 1 {
		t.Fatalf("total created across two concurrent reconcilers=%d, want exactly 1", total)
	}
	if got := e.totalEventCount(msg); got != 2 {
		t.Fatalf("total events=%d, want 2 (original + exactly one reconciliation)", got)
	}
}

// --- Section 22: two-tenant system-context scan -----------------------------

func TestReconcile_ScansAcrossTenants(t *testing.T) {
	e1 := newReconcileEnv(t)
	e2 := newReconcileEnv(t)
	old := 2 * time.Hour
	msg1 := e1.seedMessage("queued")
	e1.seedSendEvent(msg1, &old)
	msg2 := e2.seedMessage("queued")
	e2.seedSendEvent(msg2, &old)

	store := delivery.NewPostgresReconciliationStore(e1.seed)
	n, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("created=%d, want at least 2 (one per tenant) — system context must see across tenants", n)
	}
	if e1.unpublishedCount(msg1) != 1 {
		t.Fatal("tenant 1's message was not reconciled")
	}
	if e2.unpublishedCount(msg2) != 1 {
		t.Fatal("tenant 2's message was not reconciled")
	}
}

// --- Section 15: bounded batch -----------------------------------------------

func TestReconcile_RespectsBatchSize(t *testing.T) {
	e := newReconcileEnv(t)
	old := 2 * time.Hour
	for i := 0; i < 5; i++ {
		msg := e.seedMessage("queued")
		e.seedSendEvent(msg, &old)
	}
	store := delivery.NewPostgresReconciliationStore(e.seed)
	n, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 3)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("created=%d, want exactly batchSize=3", n)
	}
}

// Codex M / ADR-0038: reconciliation does not recreate delivery intents for a suspended company.
func TestReconcile_SuspendedCompany_IsNotReenqueued(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	old := 2 * time.Hour
	e.seedSendEvent(msg, &old)
	e.exec(`UPDATE tenants SET status='suspended' WHERE id=$1`, e.tenant)

	store := delivery.NewPostgresReconciliationStore(e.seed)
	n, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200)
	if err != nil || n != 0 || e.totalEventCount(msg) != 1 {
		t.Fatalf("a suspended company's message was re-enqueued: n=%d err=%v events=%d", n, err, e.totalEventCount(msg))
	}
	e.exec(`UPDATE tenants SET status='active' WHERE id=$1`, e.tenant)
	if n, err := store.ReconcileStrandedQueuedSends(context.Background(), testMaxAge, testGrace, 200); err != nil || n != 1 {
		t.Fatalf("after the reactivation: n=%d err=%v, want 1", n, err)
	}
}

// Codex M / ADR-0038: a suspension in flight is not raced by the reconciliation (it skips that company, picks it up later).
func TestReconcile_ASuspensionInFlightIsNotRaced(t *testing.T) {
	e := newReconcileEnv(t)
	msg := e.seedMessage("queued")
	old := 2 * time.Hour
	e.seedSendEvent(msg, &old)
	ctx := context.Background()
	tx, err := e.seed.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE tenants SET status='suspended' WHERE id=$1`, e.tenant); err != nil {
		t.Fatal(err)
	}
	store := delivery.NewPostgresReconciliationStore(e.seed)
	if n, err := store.ReconcileStrandedQueuedSends(ctx, testMaxAge, testGrace, 200); err != nil || n != 0 || e.totalEventCount(msg) != 1 {
		t.Fatalf("reconciliation wrote behind a suspension in flight: n=%d err=%v events=%d", n, err, e.totalEventCount(msg))
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := store.ReconcileStrandedQueuedSends(ctx, testMaxAge, testGrace, 200); err != nil || n != 1 {
		t.Fatalf("once the suspension was rolled back: n=%d err=%v, want 1", n, err)
	}
}
