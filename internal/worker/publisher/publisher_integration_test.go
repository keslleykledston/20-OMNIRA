package publisher

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/omnira/omnira/internal/outbox/adapters"
	"github.com/omnira/omnira/internal/outbox/application"
	"github.com/omnira/omnira/internal/outbox/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/testhelpers"
)

func TestPublisherRuntimeRoleEndToEnd(t *testing.T) {
	databaseURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	natsCfg := testhelpers.RequireIntegrationNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	nc, err := nats.Connect(natsCfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	runShort := strings.ReplaceAll(natsCfg.RunID, ":", "")
	unique := strings.ReplaceAll(uuid.NewString(), "-", "")
	streamName := "OMNIRA_TEST_" + runShort + "_" + unique
	// The Publisher derives the actual publish subject from the event's own
	// EventType/AggregateType (see subjectForEvent in publisher.go), not from
	// an independent string here — the stream's subject filter MUST match
	// that derivation exactly, or every publish silently exhausts retries
	// against a stream that never matches. Both eventType and the resulting
	// subject are unique per test run so this never collides with another
	// invocation.
	eventType := domain.EventType("test.publisher." + unique)
	aggregateType := domain.AggregateType("test")
	subject := subjectForEvent(&domain.OutboxEvent{EventType: eventType, AggregateType: aggregateType})
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: streamName, Subjects: []string{subject}, Storage: jetstream.MemoryStorage})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = js.DeleteStream(ctx, streamName) }()

	tenantID := uuid.New()
	conn, err := seed.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.is_system_admin', 'true', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,'Publisher test','active')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenantID) }()
	event, err := domain.NewOutboxEvent(tenantID, eventType, aggregateType, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	repo := adapters.NewPostgresOutboxRepository(app)
	if err := platformdb.WithSystemTenantSession(ctx, app, tenantID, func(scoped context.Context) error {
		return repo.Store(scoped, event)
	}); err != nil {
		t.Fatal(err)
	}
	pub := NewPublisher(application.NewOutboxService(repo), js, 10, 1)
	pub.SetSessionRunner(func(ctx context.Context, fn func(context.Context) error) error {
		return platformdb.WithTenantSession(ctx, app, uuid.Nil, true, fn)
	})
	if published, err := pub.PublishUnpublished(ctx); err != nil || published < 1 {
		t.Fatalf("published=%d err=%v", published, err)
	}
	if err := platformdb.WithTenantSession(ctx, app, uuid.Nil, true, func(scoped context.Context) error {
		got, err := repo.FindByID(scoped, event.ID)
		if err != nil {
			return err
		}
		if got == nil || got.PublishedAt == nil {
			t.Fatal("publisher did not mark event inside runtime session")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
