package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/routing/domain"
	"github.com/omnira/omnira/internal/routing/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Mock implementations for testing

type mockParticipantRepo struct {
	participants map[uuid.UUID]*domain.ConversationParticipant
}

func newMockParticipantRepo() *mockParticipantRepo {
	return &mockParticipantRepo{
		participants: make(map[uuid.UUID]*domain.ConversationParticipant),
	}
}

func (m *mockParticipantRepo) Create(ctx context.Context, p *domain.ConversationParticipant) error {
	if _, exists := m.participants[p.ID]; !exists {
		m.participants[p.ID] = p
	}
	return nil
}

func (m *mockParticipantRepo) FindByConversation(ctx context.Context, conversationID uuid.UUID) ([]*domain.ConversationParticipant, error) {
	var result []*domain.ConversationParticipant
	for _, p := range m.participants {
		if p.ConversationID == conversationID {
			result = append(result, p)
		}
	}
	return result, nil
}

func (m *mockParticipantRepo) FindAssignee(ctx context.Context, conversationID uuid.UUID) (*domain.ConversationParticipant, error) {
	for _, p := range m.participants {
		if p.ConversationID == conversationID && p.Role == domain.RoleAssignee {
			return p, nil
		}
	}
	return nil, nil
}

func (m *mockParticipantRepo) FindByUserAndConversation(ctx context.Context, conversationID, userID uuid.UUID) (*domain.ConversationParticipant, error) {
	for _, p := range m.participants {
		if p.ConversationID == conversationID && p.UserID == userID {
			return p, nil
		}
	}
	return nil, nil
}

func (m *mockParticipantRepo) UpdateRole(ctx context.Context, participantID uuid.UUID, role domain.ParticipantRole) error {
	if p, exists := m.participants[participantID]; exists {
		p.Role = role
	}
	return nil
}

func (m *mockParticipantRepo) MarkAsJoined(ctx context.Context, participantID uuid.UUID) error {
	if p, exists := m.participants[participantID]; exists {
		if p.Role == domain.RoleInvited {
			p.AcceptInvite()
		}
	}
	return nil
}

func (m *mockParticipantRepo) MarkAsLeft(ctx context.Context, participantID uuid.UUID) error {
	if p, exists := m.participants[participantID]; exists {
		p.Leave()
	}
	return nil
}

func (m *mockParticipantRepo) ListActiveParticipants(ctx context.Context, conversationID uuid.UUID) ([]*domain.ConversationParticipant, error) {
	var result []*domain.ConversationParticipant
	for _, p := range m.participants {
		if p.ConversationID == conversationID && p.IsActive() {
			result = append(result, p)
		}
	}
	return result, nil
}

type mockConversationAssigner struct {
	assignee    *uuid.UUID
	permissions map[string]bool
}

func newMockConversationAssigner(assignee *uuid.UUID) *mockConversationAssigner {
	return &mockConversationAssigner{
		assignee:    assignee,
		permissions: make(map[string]bool),
	}
}

func (m *mockConversationAssigner) LockAssignee(ctx context.Context, conversationID uuid.UUID) (*uuid.UUID, bool, error) {
	return m.assignee, true, nil
}

func (m *mockConversationAssigner) SetAssignee(ctx context.Context, change ports.AssignmentChange) error {
	m.assignee = change.To
	return nil
}

func (m *mockConversationAssigner) HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error) {
	return m.permissions[userID.String()+":"+permission], nil
}

// Tests

func TestInviteSuccessful(t *testing.T) {
	tenantID := uuid.New()
	conversationID := uuid.New()
	actorID := uuid.New()
	targetID := uuid.New()

	repo := newMockParticipantRepo()
	assigner := newMockConversationAssigner(&actorID)
	assigner.permissions[actorID.String()+":conversation.manage"] = true
	assigner.permissions[targetID.String()+":conversation.claim"] = true

	svc := NewParticipantService(repo, assigner, nil)

	ctx := context.Background()
	ctx = tenancydomain.WithContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})

	result, err := svc.Invite(ctx, conversationID, targetID)
	if err != nil {
		t.Fatalf("invite failed: %v", err)
	}

	if !result.Changed {
		t.Fatal("expected changed=true")
	}

	if result.Role != domain.RoleInvited {
		t.Fatalf("expected role=INVITED, got %s", result.Role)
	}
}

func TestInviteForbiddenWithoutPermission(t *testing.T) {
	tenantID := uuid.New()
	conversationID := uuid.New()
	actorID := uuid.New()
	targetID := uuid.New()

	repo := newMockParticipantRepo()
	assigner := newMockConversationAssigner(&actorID)
	// No permissions granted

	svc := NewParticipantService(repo, assigner, nil)

	ctx := context.Background()
	ctx = tenancydomain.WithContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})

	_, err := svc.Invite(ctx, conversationID, targetID)
	if err == nil {
		t.Fatal("expected error for missing permission")
	}
}

func TestAcceptInviteSuccessful(t *testing.T) {
	tenantID := uuid.New()
	conversationID := uuid.New()
	actorID := uuid.New()

	repo := newMockParticipantRepo()
	p, _ := domain.NewInvite(tenantID, conversationID, actorID)
	repo.Create(context.Background(), p)

	assigner := newMockConversationAssigner(nil)
	svc := NewParticipantService(repo, assigner, nil)

	ctx := context.Background()
	ctx = tenancydomain.WithContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})

	result, err := svc.AcceptInvite(ctx, conversationID)
	if err != nil {
		t.Fatalf("accept failed: %v", err)
	}

	if result.JoinedAt == nil {
		t.Fatal("expected joined_at to be set")
	}

	// Verify participant was updated
	updated, _ := repo.FindByUserAndConversation(ctx, conversationID, actorID)
	if updated.Role != domain.RoleCoAttendee {
		t.Fatalf("expected role=CO_ATTENDEE after accept, got %s", updated.Role)
	}
}
