package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/outbox/domain"
)

// MockOutboxEventRepository — mock para testes.
type MockOutboxEventRepository struct {
	events map[uuid.UUID]*domain.OutboxEvent
}

func (m *MockOutboxEventRepository) Store(ctx context.Context, event *domain.OutboxEvent) error {
	m.events[event.ID] = event
	return nil
}

func (m *MockOutboxEventRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.OutboxEvent, error) {
	return m.events[id], nil
}

func (m *MockOutboxEventRepository) FindUnpublished(ctx context.Context, limit int) ([]*domain.OutboxEvent, error) {
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

func (m *MockOutboxEventRepository) Update(ctx context.Context, event *domain.OutboxEvent) error {
	m.events[event.ID] = event
	return nil
}

func (m *MockOutboxEventRepository) Delete(ctx context.Context, id uuid.UUID) error {
	delete(m.events, id)
	return nil
}

func (m *MockOutboxEventRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.OutboxEvent, error) {
	var result []*domain.OutboxEvent
	for _, e := range m.events {
		if e.TenantID == tenantID {
			result = append(result, e)
		}
	}
	return result, nil
}

func TestRecordEvent(t *testing.T) {
	repo := &MockOutboxEventRepository{events: make(map[uuid.UUID]*domain.OutboxEvent)}
	svc := NewOutboxService(repo)

	tenantID := uuid.New()
	aggregateID := uuid.New()
	correlationID := uuid.New()
	payload := map[string]interface{}{
		"legal_name": "Company A",
		"tax_id":     "12345",
	}

	event, err := svc.RecordEvent(
		context.Background(),
		tenantID,
		domain.EventTenantCreated,
		domain.AggregateTenant,
		aggregateID,
		correlationID,
		payload,
	)
	if err != nil {
		t.Fatalf("expected record to succeed, got error: %v", err)
	}

	if event.TenantID != tenantID {
		t.Errorf("expected tenant_id %s, got %s", tenantID, event.TenantID)
	}
	if event.IsPublished() {
		t.Error("expected event to not be published yet")
	}
	if event.Payload["legal_name"] != "Company A" {
		t.Error("expected payload to contain legal_name")
	}
}

func TestGetUnpublishedEvents(t *testing.T) {
	repo := &MockOutboxEventRepository{events: make(map[uuid.UUID]*domain.OutboxEvent)}
	svc := NewOutboxService(repo)

	tenantID := uuid.New()
	aggregateID := uuid.New()

	// Record 3 events
	for i := 0; i < 3; i++ {
		svc.RecordEvent(
			context.Background(),
			tenantID,
			domain.EventTenantCreated,
			domain.AggregateTenant,
			aggregateID,
			uuid.New(),
			nil,
		)
	}

	// Get unpublished
	events, err := svc.GetUnpublishedEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("expected get to succeed, got error: %v", err)
	}

	if len(events) != 3 {
		t.Errorf("expected 3 unpublished events, got %d", len(events))
	}
}

func TestMarkPublished(t *testing.T) {
	repo := &MockOutboxEventRepository{events: make(map[uuid.UUID]*domain.OutboxEvent)}
	svc := NewOutboxService(repo)

	tenantID := uuid.New()
	aggregateID := uuid.New()

	// Record event
	event, _ := svc.RecordEvent(
		context.Background(),
		tenantID,
		domain.EventTenantCreated,
		domain.AggregateTenant,
		aggregateID,
		uuid.New(),
		nil,
	)

	if event.IsPublished() {
		t.Error("expected event to not be published initially")
	}

	// Mark published
	err := svc.MarkPublished(context.Background(), event.ID)
	if err != nil {
		t.Fatalf("expected mark published to succeed, got error: %v", err)
	}

	// Verify
	retrieved, _ := repo.FindByID(context.Background(), event.ID)
	if !retrieved.IsPublished() {
		t.Error("expected event to be published")
	}
}

func TestRecordAttempt(t *testing.T) {
	repo := &MockOutboxEventRepository{events: make(map[uuid.UUID]*domain.OutboxEvent)}
	svc := NewOutboxService(repo)

	tenantID := uuid.New()
	aggregateID := uuid.New()

	// Record event
	event, _ := svc.RecordEvent(
		context.Background(),
		tenantID,
		domain.EventTenantCreated,
		domain.AggregateTenant,
		aggregateID,
		uuid.New(),
		nil,
	)

	if event.Attempts != 0 {
		t.Errorf("expected 0 attempts initially, got %d", event.Attempts)
	}

	// Record attempts
	svc.RecordAttempt(context.Background(), event.ID)
	svc.RecordAttempt(context.Background(), event.ID)

	// Verify
	retrieved, _ := repo.FindByID(context.Background(), event.ID)
	if retrieved.Attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", retrieved.Attempts)
	}
}

func TestListTenantEvents(t *testing.T) {
	repo := &MockOutboxEventRepository{events: make(map[uuid.UUID]*domain.OutboxEvent)}
	svc := NewOutboxService(repo)

	tenantA := uuid.New()
	tenantB := uuid.New()

	// Record events para 2 tenants
	for i := 0; i < 2; i++ {
		svc.RecordEvent(
			context.Background(),
			tenantA,
			domain.EventTenantCreated,
			domain.AggregateTenant,
			uuid.New(),
			uuid.New(),
			nil,
		)
	}

	for i := 0; i < 3; i++ {
		svc.RecordEvent(
			context.Background(),
			tenantB,
			domain.EventMembershipGranted,
			domain.AggregateMembership,
			uuid.New(),
			uuid.New(),
			nil,
		)
	}

	// List eventos de tenantA
	eventsA, _ := svc.ListTenantEvents(context.Background(), tenantA, 10, 0)
	if len(eventsA) != 2 {
		t.Errorf("expected 2 events for tenantA, got %d", len(eventsA))
	}

	// List eventos de tenantB
	eventsB, _ := svc.ListTenantEvents(context.Background(), tenantB, 10, 0)
	if len(eventsB) != 3 {
		t.Errorf("expected 3 events for tenantB, got %d", len(eventsB))
	}
}
