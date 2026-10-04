package ports

import (
	"context"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/domain"
	ticketdomain "github.com/omnira/omnira/internal/tickets/domain"
)

// TicketPolicyRepository reads the facts the ticket policy needs, under the tenant's RLS.
type TicketPolicyRepository interface {
	TopicPolicyFacts(ctx context.Context, tenantID, topicID uuid.UUID) (PolicyFacts, error)
	// ActiveTicket is the conversation's single active ticket (open/in_progress/waiting), if any, with the topic that owns it.
	ActiveTicket(ctx context.Context, tenantID, conversationID uuid.UUID) (*domain.ActiveTicketFacts, error)
	// LockConversation serialises ticket decisions for one conversation until the transaction ends.
	LockConversation(ctx context.Context, tenantID, conversationID uuid.UUID) error
	// LegacyTicket: the conversation's active ticket when no topic is linked to it yet (for the on-demand backfill).
	LegacyTicket(ctx context.Context, tenantID, conversationID uuid.UUID) (*LegacyTicket, error)
}

type PolicyFacts struct {
	Open            bool
	HasPrimary      bool
	Conversations   []uuid.UUID
	GroupLinks      int
	OtherOpenTopics int
	MessageCount    int
}

type LegacyTicket struct {
	ID      uuid.UUID
	Subject string
}

// LocalTicketCreator opens a local ticket in a conversation. It is the inbox's own store (the same code path inbound
// uses), so a ticket opened by the policy is indistinguishable from any other.
type LocalTicketCreator interface {
	Store(ctx context.Context, t *ticketdomain.Ticket) error
}
