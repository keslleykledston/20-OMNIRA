// Package application orchestrates the provider-neutral inbound persistence
// path. It deliberately accepts a trusted ChannelConnection, never a tenant
// identifier supplied by a webhook body.
package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	contactdomain "github.com/omnira/omnira/internal/contacts/domain"
	conversationdomain "github.com/omnira/omnira/internal/conversations/domain"
	messagedomain "github.com/omnira/omnira/internal/messages/domain"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketdomain "github.com/omnira/omnira/internal/tickets/domain"
)

type ContactStore interface {
	UpsertByPhone(context.Context, *contactdomain.Contact) (*contactdomain.Contact, error)
}

type ConversationStore interface {
	FindOpen(context.Context, uuid.UUID, uuid.UUID) (*conversationdomain.Conversation, error)
	Store(context.Context, *conversationdomain.Conversation) error
}

type MessageStore interface {
	StoreInbound(context.Context, *messagedomain.Message) (*messagedomain.Message, bool, error)
}

type TicketStore interface {
	FindOpenByConversation(context.Context, uuid.UUID) (*ticketdomain.Ticket, error)
	Store(context.Context, *ticketdomain.Ticket) error
}

type InboundService struct {
	contacts      ContactStore
	conversations ConversationStore
	messages      MessageStore
	tickets       TicketStore
}

type InboundResult struct {
	Contact      *contactdomain.Contact
	Conversation *conversationdomain.Conversation
	Message      *messagedomain.Message
	Ticket       *ticketdomain.Ticket
	Duplicate    bool
}

func NewInboundService(contacts ContactStore, conversations ConversationStore, messages MessageStore, tickets TicketStore) *InboundService {
	return &InboundService{contacts: contacts, conversations: conversations, messages: messages, tickets: tickets}
}

// Ingest persists an inbound canonical message. Callers must execute this in
// one tenant session/transaction. Connection ownership is checked against the
// TenantContext before any write, so a provider payload cannot switch tenant.
func (s *InboundService) Ingest(ctx context.Context, connection channeldomain.ChannelConnection, inbound channeldomain.InboundMessage) (*InboundResult, error) {
	if s == nil || s.contacts == nil || s.conversations == nil || s.messages == nil || s.tickets == nil {
		return nil, errors.New("inbox: inbound service is not configured")
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("inbox: tenant context required: %w", err)
	}
	if connection.ID == uuid.Nil || connection.TenantID != tc.TenantID || inbound.ConnectionID != connection.ID.String() {
		return nil, errors.New("inbox: trusted connection mismatch")
	}
	if inbound.ProviderMessageID == "" || inbound.FromE164 == "" {
		return nil, errors.New("inbox: inbound message identity is required")
	}
	contact, err := contactdomain.NewContact(tc.TenantID, inbound.FromE164, "")
	if err != nil {
		return nil, err
	}
	contact, err = s.contacts.UpsertByPhone(ctx, contact)
	if err != nil || contact == nil {
		return nil, fmt.Errorf("inbox: resolve contact: %w", err)
	}
	conversation, err := s.conversations.FindOpen(ctx, contact.ID, connection.ID)
	if err != nil {
		return nil, fmt.Errorf("inbox: find conversation: %w", err)
	}
	if conversation == nil {
		conversation, err = conversationdomain.NewConversation(tc.TenantID, contact.ID, &connection.ID)
		if err != nil {
			return nil, err
		}
		if err := s.conversations.Store(ctx, conversation); err != nil {
			return nil, fmt.Errorf("inbox: store conversation: %w", err)
		}
	}
	message, err := messagedomain.NewTextMessage(tc.TenantID, conversation.ID, messagedomain.DirectionInbound, inbound.Text, inbound.ProviderMessageID)
	if err != nil {
		return nil, err
	}
	stored, duplicate, err := s.messages.StoreInbound(ctx, message)
	if err != nil {
		return nil, fmt.Errorf("inbox: store message: %w", err)
	}
	if duplicate {
		return &InboundResult{Contact: contact, Conversation: conversation, Message: stored, Duplicate: true}, nil
	}
	ticket, err := s.tickets.FindOpenByConversation(ctx, conversation.ID)
	if err != nil {
		return nil, fmt.Errorf("inbox: find ticket: %w", err)
	}
	if ticket == nil {
		ticket, err = ticketdomain.NewTicket(tc.TenantID, conversation.ID, "")
		if err != nil {
			return nil, err
		}
		if err := s.tickets.Store(ctx, ticket); err != nil {
			return nil, fmt.Errorf("inbox: store ticket: %w", err)
		}
	}
	return &InboundResult{Contact: contact, Conversation: conversation, Message: stored, Ticket: ticket}, nil
}
