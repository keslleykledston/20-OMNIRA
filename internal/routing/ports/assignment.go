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

// AuditRecorder appends an audit event in the caller's transaction.
type AuditRecorder interface {
	ConversationAssignmentChanged(ctx context.Context, change AssignmentChange) error
}
