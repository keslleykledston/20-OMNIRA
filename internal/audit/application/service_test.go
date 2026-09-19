package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/audit/domain"
)

// MockAuditEventRepository — mock para testes.
type MockAuditEventRepository struct {
	events map[uuid.UUID]*domain.AuditEvent
}

func (m *MockAuditEventRepository) Store(ctx context.Context, event *domain.AuditEvent) error {
	m.events[event.ID] = event
	return nil
}

func (m *MockAuditEventRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.AuditEvent, error) {
	return m.events[id], nil
}

func (m *MockAuditEventRepository) FindByTenantAndCorrelation(ctx context.Context, tenantID, correlationID uuid.UUID) ([]*domain.AuditEvent, error) {
	var result []*domain.AuditEvent
	for _, e := range m.events {
		if e.TenantID == tenantID && e.CorrelationID == correlationID {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *MockAuditEventRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.AuditEvent, error) {
	var result []*domain.AuditEvent
	for _, e := range m.events {
		if e.TenantID == tenantID {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *MockAuditEventRepository) FindByAction(ctx context.Context, tenantID uuid.UUID, action domain.AuditAction, limit, offset int) ([]*domain.AuditEvent, error) {
	var result []*domain.AuditEvent
	for _, e := range m.events {
		if e.TenantID == tenantID && e.Action == action {
			result = append(result, e)
		}
	}
	return result, nil
}

func TestRecordEvent(t *testing.T) {
	repo := &MockAuditEventRepository{events: make(map[uuid.UUID]*domain.AuditEvent)}
	svc := NewAuditService(repo)

	tenantID := uuid.New()
	actorID := uuid.New()
	resourceID := uuid.New()
	correlationID := uuid.New()

	// Record event
	event, err := svc.RecordEvent(
		context.Background(),
		tenantID,
		actorID,
		domain.ActionTenantCreated,
		domain.ResourceTenant,
		resourceID,
		domain.OutcomeSuccess,
		correlationID,
	)
	if err != nil {
		t.Fatalf("expected record to succeed, got error: %v", err)
	}

	// Validar evento
	if event.TenantID != tenantID {
		t.Errorf("expected tenant_id %s, got %s", tenantID, event.TenantID)
	}
	if event.ActorID != actorID {
		t.Errorf("expected actor_id %s, got %s", actorID, event.ActorID)
	}
	if event.CorrelationID != correlationID {
		t.Errorf("expected correlation_id %s, got %s", correlationID, event.CorrelationID)
	}
}

func TestEventMetadata(t *testing.T) {
	repo := &MockAuditEventRepository{events: make(map[uuid.UUID]*domain.AuditEvent)}
	svc := NewAuditService(repo)

	tenantID := uuid.New()
	actorID := uuid.New()
	resourceID := uuid.New()

	event, _ := svc.RecordEvent(
		context.Background(),
		tenantID,
		actorID,
		domain.ActionTenantCreated,
		domain.ResourceTenant,
		resourceID,
		domain.OutcomeSuccess,
		uuid.New(),
	)

	// Adicionar metadados
	event.SetMetadata("legal_name", "Company A")
	event.SetMetadata("isolation_profile", "shared_strong_isolation")

	// Validar metadados
	if legal_name, ok := event.Metadata["legal_name"]; !ok || legal_name != "Company A" {
		t.Error("expected legal_name in metadata")
	}
	if profile, ok := event.Metadata["isolation_profile"]; !ok || profile != "shared_strong_isolation" {
		t.Error("expected isolation_profile in metadata")
	}
}

func TestGetEventsByCorrelation(t *testing.T) {
	repo := &MockAuditEventRepository{events: make(map[uuid.UUID]*domain.AuditEvent)}
	svc := NewAuditService(repo)

	tenantID := uuid.New()
	actorID := uuid.New()
	correlationID := uuid.New()

	// Record 3 events com mesma correlationID
	for i := 0; i < 3; i++ {
		svc.RecordEvent(
			context.Background(),
			tenantID,
			actorID,
			domain.ActionMembershipGranted,
			domain.ResourceMembership,
			uuid.New(),
			domain.OutcomeSuccess,
			correlationID,
		)
	}

	// Buscar por correlation
	events, err := svc.GetEventsByCorrelation(context.Background(), tenantID, correlationID)
	if err != nil {
		t.Fatalf("expected get to succeed, got error: %v", err)
	}

	if len(events) != 3 {
		t.Errorf("expected 3 events, got %d", len(events))
	}
}

func TestListTenantEvents(t *testing.T) {
	repo := &MockAuditEventRepository{events: make(map[uuid.UUID]*domain.AuditEvent)}
	svc := NewAuditService(repo)

	tenantA := uuid.New()
	tenantB := uuid.New()
	actorID := uuid.New()

	// Record events para 2 tenants
	for i := 0; i < 2; i++ {
		svc.RecordEvent(
			context.Background(),
			tenantA,
			actorID,
			domain.ActionTenantCreated,
			domain.ResourceTenant,
			uuid.New(),
			domain.OutcomeSuccess,
			uuid.New(),
		)
	}

	for i := 0; i < 3; i++ {
		svc.RecordEvent(
			context.Background(),
			tenantB,
			actorID,
			domain.ActionMembershipGranted,
			domain.ResourceMembership,
			uuid.New(),
			domain.OutcomeSuccess,
			uuid.New(),
		)
	}

	// Listar eventos de tenantA
	eventsA, _ := svc.ListTenantEvents(context.Background(), tenantA, 10, 0)
	if len(eventsA) != 2 {
		t.Errorf("expected 2 events for tenantA, got %d", len(eventsA))
	}

	// Listar eventos de tenantB
	eventsB, _ := svc.ListTenantEvents(context.Background(), tenantB, 10, 0)
	if len(eventsB) != 3 {
		t.Errorf("expected 3 events for tenantB, got %d", len(eventsB))
	}
}

func TestListEventsByAction(t *testing.T) {
	repo := &MockAuditEventRepository{events: make(map[uuid.UUID]*domain.AuditEvent)}
	svc := NewAuditService(repo)

	tenantID := uuid.New()
	actorID := uuid.New()

	// Record different actions
	svc.RecordEvent(context.Background(), tenantID, actorID, domain.ActionTenantCreated, domain.ResourceTenant, uuid.New(), domain.OutcomeSuccess, uuid.New())
	svc.RecordEvent(context.Background(), tenantID, actorID, domain.ActionMembershipGranted, domain.ResourceMembership, uuid.New(), domain.OutcomeSuccess, uuid.New())
	svc.RecordEvent(context.Background(), tenantID, actorID, domain.ActionMembershipGranted, domain.ResourceMembership, uuid.New(), domain.OutcomeSuccess, uuid.New())

	// Filter by action
	membershipEvents, _ := svc.ListEventsByAction(context.Background(), tenantID, domain.ActionMembershipGranted, 10, 0)
	if len(membershipEvents) != 2 {
		t.Errorf("expected 2 membership events, got %d", len(membershipEvents))
	}
}
