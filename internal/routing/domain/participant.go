package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type ParticipantRole string

const (
	RoleAssignee   ParticipantRole = "ASSIGNEE"
	RoleInvited    ParticipantRole = "INVITED"
	RoleCoAttendee ParticipantRole = "CO_ATTENDEE"
)

type ConversationParticipant struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	ConversationID uuid.UUID
	UserID         uuid.UUID
	Role           ParticipantRole
	JoinedAt       *time.Time // nil para INVITED, populated para CO_ATTENDEE
	LeftAt         *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func NewInvite(tenantID, conversationID, userID uuid.UUID) (*ConversationParticipant, error) {
	if tenantID == uuid.Nil || conversationID == uuid.Nil || userID == uuid.Nil {
		return nil, errors.New("participant: tenant, conversation, and user are required")
	}
	now := time.Now().UTC()
	return &ConversationParticipant{
		ID:             uuid.New(),
		TenantID:       tenantID,
		ConversationID: conversationID,
		UserID:         userID,
		Role:           RoleInvited,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

func (p *ConversationParticipant) AcceptInvite() error {
	if p.Role != RoleInvited {
		return errors.New("participant: only invited participants can accept")
	}
	now := time.Now().UTC()
	p.Role = RoleCoAttendee
	p.JoinedAt = &now
	p.UpdatedAt = now
	return nil
}

func (p *ConversationParticipant) Leave() error {
	if p.Role != RoleCoAttendee {
		return errors.New("participant: only co-attendees can leave")
	}
	now := time.Now().UTC()
	p.LeftAt = &now
	p.UpdatedAt = now
	return nil
}

func (p *ConversationParticipant) IsActive() bool {
	return p.LeftAt == nil
}
