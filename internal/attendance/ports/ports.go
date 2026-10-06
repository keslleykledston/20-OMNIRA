// Package ports holds what the attendance service needs from the outside world. Every method runs inside the caller's
// tenant-scoped transaction (the TenantContext in ctx); none takes a tenant id.
package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/attendance/domain"
)

// ConversationFacts is what finalizing needs to know about the conversation, read with a row lock.
type ConversationFacts struct {
	ID         uuid.UUID
	ContactID  *uuid.UUID // nil for an internal (staff) conversation
	Status     string     // open | closed
	Kind       string
	AssignedTo *uuid.UUID
}

type Repository interface {
	// LockConversation reads the conversation FOR UPDATE: the same lock the flow engine takes, so a bot step and a
	// finalization of one conversation are serialized. ErrNotFound when RLS hides it or it does not exist.
	LockConversation(ctx context.Context, conversationID uuid.UUID) (*ConversationFacts, error)
	// ContactOfConversation is the read-only variant (no lock). ErrNotFound when invisible; uuid.Nil for an internal one.
	ContactOfConversation(ctx context.Context, conversationID uuid.UUID) (uuid.UUID, error)
	ClosureByConversation(ctx context.Context, conversationID uuid.UUID) (*domain.Closure, error)
	FollowUpsOfClosure(ctx context.Context, closureID uuid.UUID) ([]domain.FollowUp, error)
	IsActiveMember(ctx context.Context, userID uuid.UUID) (bool, error)
	// CloseLocalTickets closes the conversation's active tickets that are local (no ERP link) and not scoped to a topic. It
	// returns how many it closed and how many active ones it left (ERP-linked or topic-scoped).
	CloseLocalTickets(ctx context.Context, conversationID uuid.UUID) (closed, kept int, err error)
	// MarkClosed sets status=closed, closed_at and automation_mode='none'.
	MarkClosed(ctx context.Context, conversationID uuid.UUID) error
	InsertClosure(ctx context.Context, c *domain.Closure) error
	InsertFollowUps(ctx context.Context, items []domain.FollowUp) error
	ListClosures(ctx context.Context, contactID uuid.UUID, limit int) ([]domain.Closure, error)
	ListFollowUps(ctx context.Context, contactID uuid.UUID, status domain.FollowUpStatus, limit int) ([]domain.FollowUp, error)
	LockFollowUp(ctx context.Context, id uuid.UUID) (*domain.FollowUp, error)
	ResolveFollowUp(ctx context.Context, id uuid.UUID, status domain.FollowUpStatus, note string, by uuid.UUID) error
}

// Authorizer answers from the role -> permission matrix (never by role name).
type Authorizer interface {
	Has(ctx context.Context, userID uuid.UUID, permission string) (bool, error)
}

// Auditor writes to the append-only audit log inside the same transaction. Best effort by contract: it must not fail the
// operation it describes (the transaction already guarantees the data change is atomic with the audit row when it succeeds).
type Auditor interface {
	Record(ctx context.Context, action string, resourceType string, resourceID uuid.UUID, meta map[string]any)
}
