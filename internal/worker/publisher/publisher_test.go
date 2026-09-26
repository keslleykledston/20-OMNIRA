package publisher

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/outbox/application"
	"github.com/omnira/omnira/internal/outbox/domain"
)

// MockOutboxRepository — mock para testes.
type MockOutboxRepository struct {
	events map[uuid.UUID]*domain.OutboxEvent
}

// Implementar interface ports.OutboxEventRepository

func (m *MockOutboxRepository) Store(ctx context.Context, event *domain.OutboxEvent) error {
	m.events[event.ID] = event
	return nil
}

func (m *MockOutboxRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.OutboxEvent, error) {
	return m.events[id], nil
}

func (m *MockOutboxRepository) FindUnpublished(ctx context.Context, limit int) ([]*domain.OutboxEvent, error) {
	var result []*domain.OutboxEvent
	for _, e := range m.events {
		if !e.IsPublished() {
			result = append(result, e)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *MockOutboxRepository) Update(ctx context.Context, event *domain.OutboxEvent) error {
	m.events[event.ID] = event
	return nil
}

func (m *MockOutboxRepository) Delete(ctx context.Context, id uuid.UUID) error {
	delete(m.events, id)
	return nil
}

func (m *MockOutboxRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.OutboxEvent, error) {
	var result []*domain.OutboxEvent
	for _, e := range m.events {
		if e.TenantID == tenantID {
			result = append(result, e)
		}
	}
	return result[offset:], nil
}

func TestPublisherInitialization(t *testing.T) {
	repo := &MockOutboxRepository{events: make(map[uuid.UUID]*domain.OutboxEvent)}
	svc := application.NewOutboxService(repo)

	pub := NewPublisher(svc, nil, 0, 0)
	if pub == nil {
		t.Fatalf("expected publisher to be created, got nil")
	}

	if pub.batchSize != 10 {
		t.Errorf("expected default batchSize=10, got %d", pub.batchSize)
	}

	if pub.maxRetries != 3 {
		t.Errorf("expected default maxRetries=3, got %d", pub.maxRetries)
	}
}

func TestPublisherBatchSize(t *testing.T) {
	repo := &MockOutboxRepository{events: make(map[uuid.UUID]*domain.OutboxEvent)}
	svc := application.NewOutboxService(repo)

	pub := NewPublisher(svc, nil, 5, 2)
	if pub.batchSize != 5 {
		t.Errorf("expected batchSize=5, got %d", pub.batchSize)
	}

	if pub.maxRetries != 2 {
		t.Errorf("expected maxRetries=2, got %d", pub.maxRetries)
	}
}

// PILOT.4B test E: for the outbound send job, aggregate_id IS message_id
// (internal/messages/adapters/postgres.go's outbox insert uses the message's
// own id), so the publish-failure log must include it alongside
// outbox_event_id. For every other event type, aggregate_id is a different
// aggregate (tenant, membership, conversation) and must NOT be labeled
// message_id. Verified as a focused unit test of the pure field-selection
// logic rather than a full JetStream-failure integration test — the
// jetstream.JetStream interface is large enough that faking it fully here
// would be its own small testing framework for one log line.
func TestMessageIDSuffixOnlyForOutboundSendJob(t *testing.T) {
	sendEvent, err := domain.NewOutboxEvent(uuid.New(), domain.JobChannelSendText, domain.AggregateMessage, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDSuffix(sendEvent); got != " message_id="+sendEvent.AggregateID.String() {
		t.Fatalf("send-text job: got %q", got)
	}

	other, err := domain.NewOutboxEvent(uuid.New(), domain.EventTenantCreated, domain.AggregateTenant, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDSuffix(other); got != "" {
		t.Fatalf("unrelated event type must not be labeled message_id, got %q", got)
	}
}

func TestRoutingJobUsesCanonicalSubject(t *testing.T) {
	event, err := domain.NewOutboxEvent(uuid.New(), domain.JobRoutingAssign, domain.AggregateConversation, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if got := subjectForEvent(event); got != "job.routing.assign.v1" {
		t.Fatalf("subject=%q", got)
	}
}

func TestPublisherStartWithNilJS(t *testing.T) {
	repo := &MockOutboxRepository{events: make(map[uuid.UUID]*domain.OutboxEvent)}
	svc := application.NewOutboxService(repo)
	pub := NewPublisher(svc, nil, 1, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Deve rodar até timeout sem panic
	_ = pub.Start(ctx, 50*time.Millisecond)
}

func TestPublisherReadsOutboxInsideSessionRunner(t *testing.T) {
	repo := &MockOutboxRepository{events: make(map[uuid.UUID]*domain.OutboxEvent)}
	pub := NewPublisher(application.NewOutboxService(repo), nil, 1, 1)
	calls := 0
	pub.SetSessionRunner(func(ctx context.Context, fn func(context.Context) error) error {
		calls++
		return fn(ctx)
	})
	if published, err := pub.PublishUnpublished(context.Background()); err != nil || published != 0 {
		t.Fatalf("published=%d err=%v", published, err)
	}
	if calls != 1 {
		t.Fatalf("session runner calls=%d", calls)
	}
}

func TestOutboxServiceIntegration(t *testing.T) {
	repo := &MockOutboxRepository{events: make(map[uuid.UUID]*domain.OutboxEvent)}
	svc := application.NewOutboxService(repo)
	ctx := context.Background()

	tenantID := uuid.New()
	aggregateID := uuid.New()
	correlationID := uuid.New()

	// Record event
	event, err := svc.RecordEvent(
		ctx,
		tenantID,
		domain.EventTenantCreated,
		domain.AggregateTenant,
		aggregateID,
		correlationID,
		map[string]interface{}{"name": "Test Tenant"},
	)
	if err != nil {
		t.Fatalf("failed to record event: %v", err)
	}

	if event.ID == uuid.Nil {
		t.Errorf("expected event to have an ID")
	}

	if !event.CreatedAt.Before(time.Now().Add(time.Second)) {
		t.Errorf("expected event to have reasonable timestamp")
	}

	// Verify unpublished
	unpublished, err := svc.GetUnpublishedEvents(ctx, 10)
	if err != nil {
		t.Fatalf("failed to get unpublished events: %v", err)
	}

	if len(unpublished) != 1 {
		t.Errorf("expected 1 unpublished event, got %d", len(unpublished))
	}

	// Mark as published
	err = svc.MarkPublished(ctx, event.ID)
	if err != nil {
		t.Fatalf("failed to mark as published: %v", err)
	}

	// Verify no more unpublished
	unpublished, err = svc.GetUnpublishedEvents(ctx, 10)
	if err != nil {
		t.Fatalf("failed to get unpublished events after mark: %v", err)
	}

	if len(unpublished) != 0 {
		t.Errorf("expected 0 unpublished events after mark, got %d", len(unpublished))
	}
}
