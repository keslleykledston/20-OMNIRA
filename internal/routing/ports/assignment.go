package ports

import (
	"context"

	"github.com/google/uuid"
)

// AssignmentChange is one applied assignee transition, recorded append-only.
type AssignmentChange struct {
	ConversationID uuid.UUID
	From, To       *uuid.UUID
	Actor          uuid.UUID
	Reason         string
}

// ConversationAssigner is the persistence port for manual assign/unassign.
// Every method runs in the caller's tenant session (SET LOCAL + RLS); the
// tenant is always taken from the TenantContext, never from a parameter
// supplied by the client.
type ConversationAssigner interface {
	// LockAssignee reads the current assignee under a row lock (FOR UPDATE), so
	// concurrent claims serialize and the loser observes the winner. found=false
	// covers both "does not exist" and "belongs to another tenant" (RLS).
	LockAssignee(ctx context.Context, conversationID uuid.UUID) (current *uuid.UUID, found bool, err error)
	// SetAssignee updates the conversation and appends the assignment history.
	SetAssignee(ctx context.Context, change AssignmentChange) error
	// HasPermission checks the actor's active membership role in the tenant.
	HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error)
}

// OperationalEligibility is implemented by production routing adapters. It is
// separate to keep existing co-attendance test doubles focused on their port.
type OperationalEligibility interface {
	IsEligibleForConversation(context.Context, uuid.UUID, uuid.UUID) (bool, error)
}

// AuditRecorder appends an audit event in the caller's transaction.
type AuditRecorder interface {
	ConversationAssignmentChanged(ctx context.Context, change AssignmentChange) error
	// Participant events (co-attendance + transfer)
	ParticipantInvited(ctx context.Context, conversationID, actor, targetUser uuid.UUID) error
	ParticipantAccepted(ctx context.Context, conversationID, actor uuid.UUID) error
	ParticipantRejected(ctx context.Context, conversationID, actor uuid.UUID) error
	ParticipantLeft(ctx context.Context, conversationID, actor uuid.UUID) error
	ConversationTransferred(ctx context.Context, conversationID, actor uuid.UUID, from, to *uuid.UUID) error
}
