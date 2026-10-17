package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrHeldByExternal: an external application holds the conversation (ADR-0048). A person cannot claim, assign, release or transfer it; a supervisor
// with conversation.takeover_external takes it over explicitly, with a reason. The manual paths see it BEFORE writing (the guard trigger in the
// database is only the net under them).
var ErrHeldByExternal = errors.New("routing: conversation is held by an external application")

// ErrQueueUnavailable: the queue does not exist in this company, or its sector was switched off (a person cannot send a conversation there).
var ErrQueueUnavailable = errors.New("routing: the queue does not exist in this company or its sector is off")

// ErrNotRoutable: the conversation is closed or is a conversation with a team member; neither is sent to a queue.
var ErrNotRoutable = errors.New("routing: the conversation is closed or is a conversation with staff")

// QueueMove is the outcome of sending a conversation to a queue.
type QueueMove struct {
	FromQueue        *uuid.UUID
	ToQueue          uuid.UUID
	PreviousAssignee *uuid.UUID // who held it before (released by the move), nil when nobody
	Changed          bool       // false: it was already in that queue with nobody holding it
}

// QueueRouter is the optional persistence port of "send this conversation to a queue" (a person, not the routing engine, decides). It runs in the caller's tenant
// session; the conversation row is locked first. found=false covers "does not exist" and "belongs to another company". The routing engine then distributes it
// inside the new queue: the move writes the queue, releases a holder with history, and queues the same routing job a new conversation gets.
type QueueRouter interface {
	MoveToQueue(ctx context.Context, conversationID, queueID, actor uuid.UUID) (move QueueMove, found bool, err error)
}

// QueueAuditRecorder is the optional audit of a queue change (kept apart from AuditRecorder so existing doubles stay as they are).
type QueueAuditRecorder interface {
	ConversationQueueChanged(ctx context.Context, conversationID, actor uuid.UUID, from *uuid.UUID, to uuid.UUID) error
}

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

// SelfClaimEligibility is the eligibility of someone TAKING a chat themself: an active member with an active agent profile.
// Queue availability and capacity are rules of automatic distribution and of assigning someone else, not of a person who
// decides to take a conversation (it may be a future customer or just a chat; nothing about it ties it to a company).
type SelfClaimEligibility interface {
	IsActiveAgent(context.Context, uuid.UUID) (bool, error)
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
