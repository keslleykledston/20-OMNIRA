package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/outbox/ports"
	"github.com/omnira/omnira/internal/testhelpers"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// findUnpublished wraps the repository call in the same system-actor session
// (SET LOCAL app.is_system_admin=true) production's publisher establishes
// via Publisher.SetSessionRunner before calling FindUnpublished — outbox_events
// has RLS, and a background worker has no human membership to satisfy it.
func (f *poisonFixture) findUnpublished(t *testing.T, repo ports.OutboxEventRepository, limit int) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	err := platformdb.WithTenantSession(context.Background(), f.app, uuid.Nil, true, func(sctx context.Context) error {
		events, err := repo.FindUnpublished(sctx, limit)
		if err != nil {
			return err
		}
		for _, e := range events {
			ids = append(ids, e.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("FindUnpublished failed: %v", err)
	}
	return ids
}

// poisonFixture seeds one tenant and gives helpers to insert valid/poison
// outbox_events rows directly (bypassing OutboxService.Store, which always
// writes correctly-shaped data — these tests need to simulate a row that
// got malformed some other way, e.g. a bug in a different writer, manual
// intervention, or legacy pre-constraint data).
type poisonFixture struct {
	seed, app *pgxpool.Pool
	tenantID  uuid.UUID
}

func newPoisonFixture(t *testing.T) *poisonFixture {
	t.Helper()
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	f := &poisonFixture{seed: seed, app: app, tenantID: uuid.New()}
	if _, err := seed.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,'Outbox poison','active')`, f.tenantID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, f.tenantID)
		seed.Close()
		app.Close()
	})
	return f
}

// insertValid writes a row exactly as OutboxService.Store would.
func (f *poisonFixture) insertValid(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.seed.Exec(context.Background(), `
		INSERT INTO outbox_events(id,tenant_id,event_type,aggregate_type,aggregate_id,correlation_id,payload)
		VALUES ($1,$2,'job.routing.assign.v1','conversation',$3,$4,'{}'::jsonb)`,
		id, f.tenantID, uuid.New().String(), uuid.New().String())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// insertPoisonPayload writes a row with syntactically valid JSONB that is
// not a JSON object — json.Unmarshal into map[string]interface{} fails,
// exactly the same containment path a malformed aggregate_id would take,
// without needing to fight the new CHECK constraint (which correctly
// rejects a malformed aggregate_id/correlation_id/causation_id at the SQL
// level now — this test simulates the class of poison the constraint
// cannot cover: payload shape, or any future/legacy column it doesn't
// know about).
func (f *poisonFixture) insertPoisonPayload(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.seed.Exec(context.Background(), `
		INSERT INTO outbox_events(id,tenant_id,event_type,aggregate_type,aggregate_id,correlation_id,payload)
		VALUES ($1,$2,'job.routing.assign.v1','conversation',$3,$4,'[1,2,3]'::jsonb)`,
		id, f.tenantID, uuid.New().String(), uuid.New().String())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *poisonFixture) quarantinedAt(t *testing.T, id uuid.UUID) *time.Time {
	t.Helper()
	var v *time.Time
	if err := f.seed.QueryRow(context.Background(), `SELECT quarantined_at FROM outbox_events WHERE id=$1`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *poisonFixture) quarantineReason(t *testing.T, id uuid.UUID) *string {
	t.Helper()
	var v *string
	if err := f.seed.QueryRow(context.Background(), `SELECT quarantine_reason FROM outbox_events WHERE id=$1`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func containsID(events []uuid.UUID, id uuid.UUID) bool {
	for _, e := range events {
		if e == id {
			return true
		}
	}
	return false
}

func TestFindUnpublished_ValidRow_IsReturned(t *testing.T) {
	f := newPoisonFixture(t)
	repo := NewPostgresOutboxRepository(f.app)
	valid := f.insertValid(t)

	ids := f.findUnpublished(t, repo, 100)
	if !containsID(ids, valid) {
		t.Fatalf("expected valid event %s to be returned, got %v", valid, ids)
	}
}

func TestFindUnpublished_PoisonFirst_ValidStillPublishes(t *testing.T) {
	f := newPoisonFixture(t)
	repo := NewPostgresOutboxRepository(f.app)
	poison := f.insertPoisonPayload(t) // created first -> earlier created_at -> ordered first
	time.Sleep(10 * time.Millisecond)
	valid := f.insertValid(t)

	ids := f.findUnpublished(t, repo, 100)
	if containsID(ids, poison) {
		t.Fatalf("poison row must never be returned as publishable, got %v", ids)
	}
	if !containsID(ids, valid) {
		t.Fatalf("expected the valid event after the poison row to still be returned, got %v", ids)
	}
	if q := f.quarantinedAt(t, poison); q == nil {
		t.Fatal("expected the poison row to be quarantined (quarantined_at set)")
	}
	if r := f.quarantineReason(t, poison); r == nil || *r == "" {
		t.Fatal("expected a non-empty quarantine_reason recorded for diagnosis")
	}
}

func TestFindUnpublished_ValidPoisonValid_BothValidsPublish(t *testing.T) {
	f := newPoisonFixture(t)
	repo := NewPostgresOutboxRepository(f.app)
	validA := f.insertValid(t)
	time.Sleep(10 * time.Millisecond)
	poison := f.insertPoisonPayload(t)
	time.Sleep(10 * time.Millisecond)
	validB := f.insertValid(t)

	ids := f.findUnpublished(t, repo, 100)
	if !containsID(ids, validA) || !containsID(ids, validB) {
		t.Fatalf("expected both valid events (before and after the poison row) to be returned, got %v", ids)
	}
	if containsID(ids, poison) {
		t.Fatalf("poison row must not be returned, got %v", ids)
	}
}

func TestFindUnpublished_QuarantinedRow_NeverReappearsOnRetry(t *testing.T) {
	f := newPoisonFixture(t)
	repo := NewPostgresOutboxRepository(f.app)
	poison := f.insertPoisonPayload(t)

	f.findUnpublished(t, repo, 100)
	firstQuarantinedAt := f.quarantinedAt(t, poison)
	if firstQuarantinedAt == nil {
		t.Fatal("expected the poison row to be quarantined after the first call")
	}

	// Simulate a publisher restart / next tick: same row must not be
	// re-processed, re-quarantined with a new timestamp, or hot-looped.
	ids := f.findUnpublished(t, repo, 100)
	if containsID(ids, poison) {
		t.Fatal("quarantined row must never reappear in FindUnpublished again")
	}
	secondQuarantinedAt := f.quarantinedAt(t, poison)
	if secondQuarantinedAt == nil || !secondQuarantinedAt.Equal(*firstQuarantinedAt) {
		t.Fatalf("expected quarantined_at to stay stable across calls (no re-quarantine), got %v then %v", firstQuarantinedAt, secondQuarantinedAt)
	}
}

func TestFindUnpublished_RespectsLimit_EvenWithPoisonRows(t *testing.T) {
	f := newPoisonFixture(t)
	repo := NewPostgresOutboxRepository(f.app)
	f.insertPoisonPayload(t)
	for i := 0; i < 5; i++ {
		f.insertValid(t)
	}

	ids := f.findUnpublished(t, repo, 3)
	if len(ids) > 3 {
		t.Fatalf("expected at most 3 events (bounded batch), got %d", len(ids))
	}
}
