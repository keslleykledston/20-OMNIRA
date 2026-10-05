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
	ApplyDeliveryStatus(context.Context, uuid.UUID, string, messagedomain.Status) (bool, error)
}

type TicketStore interface {
	FindOpenByConversation(context.Context, uuid.UUID) (*ticketdomain.Ticket, error)
	Store(context.Context, *ticketdomain.Ticket) error
}

type CRMConnector interface {
	FindCustomerByPhone(ctx context.Context, phone, companyID string) (contactID string, err error)
	CreateContact(ctx context.Context, name, phone, companyID string) (contactID string, err error)
}

// SenderIdentity is who an inbound sender is, decided from VERIFIED internal identities only (ADR-0018).
//   - Internal: a single verified staff member owns this phone / provider participant (no open conflict). The
//     sender is NOT a contact and gets no Contact row. It grants no permission: RBAC is untouched.
//   - Conflict: a verified internal identity AND an external contact share this address and a human has not decided yet.
//     The sender is handled as the existing contact but the conversation is "unclassified": no customer automation.
type SenderIdentity struct {
	Internal bool
	UserID   uuid.UUID
	Conflict bool
}

// IdentityResolver resolves the inbound sender against verified internal identities. Without one, ingestion is exactly as before.
type IdentityResolver interface {
	ResolveSender(ctx context.Context, connection channeldomain.ChannelConnection, phoneE164, participantID string) (SenderIdentity, error)
}

// InternalConversationFinder finds the open conversation with a staff member on one connection.
type InternalConversationFinder interface {
	FindOpenInternal(ctx context.Context, userID, connectionID uuid.UUID) (*conversationdomain.Conversation, error)
}

type InitialRouter interface {
	RouteNew(context.Context, uuid.UUID) error
}

type InboundService struct {
	contacts      ContactStore
	conversations ConversationStore
	messages      MessageStore
	tickets       TicketStore
	router        InitialRouter
	crm           CRMConnector
	crmCompanyID  string
	participants  ParticipantRecorder
	identity      IdentityResolver
	internal      InternalConversationFinder
}

type InboundResult struct {
	Contact      *contactdomain.Contact
	Conversation *conversationdomain.Conversation
	Message      *messagedomain.Message
	Ticket       *ticketdomain.Ticket
	Duplicate    bool
}

func NewInboundService(contacts ContactStore, conversations ConversationStore, messages MessageStore, tickets TicketStore, router ...InitialRouter) *InboundService {
	service := &InboundService{contacts: contacts, conversations: conversations, messages: messages, tickets: tickets}
	if len(router) > 0 {
		service.router = router[0]
	}
	return service
}

// ParticipantInput is the metadata the provider gave about who wrote an inbound message and what it replied to.
type ParticipantInput struct {
	ConnectionID      uuid.UUID
	Provider          string
	ExternalID        string // empty when the provider gave no reliable sender id
	DisplayName       string
	ContactID         uuid.UUID
	ConversationID    uuid.UUID
	MessageID         uuid.UUID
	ReplyToExternalID string
	// Role of the participant in the conversation: "customer" (default) or "member" for a verified staff sender.
	Role string
}

// ParticipantRecorder stores the external participant, binds it to the conversation and resolves the reply relation.
// It must be idempotent: a replayed webhook never creates a second participant.
type ParticipantRecorder interface {
	RecordInbound(ctx context.Context, in ParticipantInput) error
}

// WithParticipants enables participant and reply tracking (ADR-0017). Without it ingestion is exactly as before.
func (s *InboundService) WithParticipants(r ParticipantRecorder) *InboundService {
	s.participants = r
	return s
}

// WithIdentity enables the verified-internal-identity resolver (ADR-0018). A staff sender never becomes a Contact.
func (s *InboundService) WithIdentity(r IdentityResolver, f InternalConversationFinder) *InboundService {
	s.identity, s.internal = r, f
	return s
}

func (s *InboundService) WithCRM(crm CRMConnector, companyID string) *InboundService {
	s.crm = crm
	s.crmCompanyID = companyID
	return s
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
	// ADR-0018 resolver order: a VERIFIED internal identity first (never a name, a guessed phone or an AI), then the
	// external contact. An internal sender never reaches the contact upsert below.
	if s.identity != nil && s.internal != nil {
		sender, err := s.identity.ResolveSender(ctx, connection, inbound.FromE164, inbound.ParticipantID)
		if err != nil {
			return nil, fmt.Errorf("inbox: resolve sender identity: %w", err)
		}
		if sender.Internal && sender.UserID != uuid.Nil {
			return s.ingestInternal(ctx, tc.TenantID, connection, inbound, sender.UserID)
		}
	}
	contact, err := contactdomain.NewContact(tc.TenantID, inbound.FromE164, inbound.SenderName)
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
		// Guarda o endereço em que a mensagem chegou para responder nele: o
		// provedor pode endereçar o contato por um identificador que não se
		// deriva do telefone (ex.: LID do WhatsApp).
		conversation.ProviderChatID = inbound.ProviderChatID

		// Sincroniza contato com CRM (R5): procura por número e empresa; se não
		// existe, cria automaticamente no primeiro atendimento.
		if s.crm != nil && s.crmCompanyID != "" {
			crmContactID, err := s.crm.FindCustomerByPhone(ctx, inbound.FromE164, s.crmCompanyID)
			if err != nil {
				return nil, fmt.Errorf("inbox: find customer in crm: %w", err)
			}
			if crmContactID == "" {
				crmContactID, err = s.crm.CreateContact(ctx, contact.DisplayName, inbound.FromE164, s.crmCompanyID)
				if err != nil {
					return nil, fmt.Errorf("inbox: create contact in crm: %w", err)
				}
			}
			if crmContactID != "" {
				id, parseErr := uuid.Parse(crmContactID)
				if parseErr == nil {
					conversation.CRMContactID = &id
				}
			}
		}

		if err := s.conversations.Store(ctx, conversation); err != nil {
			return nil, fmt.Errorf("inbox: store conversation: %w", err)
		}
		if s.router != nil {
			if err := s.router.RouteNew(ctx, conversation.ID); err != nil {
				return nil, fmt.Errorf("inbox: route conversation: %w", err)
			}
		}
	}
	messageType, mediaRef, mimeType, sizeBytes := "text", "", "", int64(0)
	if inbound.Media != nil {
		messageType = string(inbound.Media.Kind)
		mediaRef, mimeType, sizeBytes = inbound.Media.MediaRef, inbound.Media.MimeType, inbound.Media.SizeBytes
	}
	message, err := messagedomain.NewMessage(tc.TenantID, conversation.ID, messagedomain.DirectionInbound, messageType, inbound.Text, mediaRef, mimeType, sizeBytes, inbound.ProviderMessageID)
	if err != nil {
		return nil, err
	}
	message.ChannelConnectionID = &connection.ID
	stored, duplicate, err := s.messages.StoreInbound(ctx, message)
	if err != nil {
		return nil, fmt.Errorf("inbox: store message: %w", err)
	}
	if duplicate {
		return &InboundResult{Contact: contact, Conversation: conversation, Message: stored, Duplicate: true}, nil
	}
	if s.participants != nil && (inbound.ParticipantID != "" || inbound.ReplyToExternalID != "") {
		if err := s.participants.RecordInbound(ctx, ParticipantInput{
			ConnectionID: connection.ID, Provider: string(connection.Provider), ExternalID: inbound.ParticipantID,
			DisplayName: inbound.SenderName, ContactID: contact.ID, ConversationID: conversation.ID, MessageID: stored.ID,
			ReplyToExternalID: inbound.ReplyToExternalID,
		}); err != nil {
			return nil, fmt.Errorf("inbox: record participant: %w", err)
		}
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

// ingestInternal stores a message from a verified staff member: a conversation WITHOUT a Contact (kind internal), no
// queue routing, no CRM sync, no placeholder ticket, none of the customer automation.
func (s *InboundService) ingestInternal(ctx context.Context, tenantID uuid.UUID, connection channeldomain.ChannelConnection, inbound channeldomain.InboundMessage, userID uuid.UUID) (*InboundResult, error) {
	conversation, err := s.internal.FindOpenInternal(ctx, userID, connection.ID)
	if err != nil {
		return nil, fmt.Errorf("inbox: find internal conversation: %w", err)
	}
	if conversation == nil {
		conversation, err = conversationdomain.NewInternalConversation(tenantID, userID, &connection.ID)
		if err != nil {
			return nil, err
		}
		conversation.ProviderChatID = inbound.ProviderChatID
		if err := s.conversations.Store(ctx, conversation); err != nil {
			return nil, fmt.Errorf("inbox: store internal conversation: %w", err)
		}
	}
	messageType, mediaRef, mimeType, sizeBytes := "text", "", "", int64(0)
	if inbound.Media != nil {
		messageType = string(inbound.Media.Kind)
		mediaRef, mimeType, sizeBytes = inbound.Media.MediaRef, inbound.Media.MimeType, inbound.Media.SizeBytes
	}
	message, err := messagedomain.NewMessage(tenantID, conversation.ID, messagedomain.DirectionInbound, messageType, inbound.Text, mediaRef, mimeType, sizeBytes, inbound.ProviderMessageID)
	if err != nil {
		return nil, err
	}
	message.ChannelConnectionID = &connection.ID
	stored, duplicate, err := s.messages.StoreInbound(ctx, message)
	if err != nil {
		return nil, fmt.Errorf("inbox: store message: %w", err)
	}
	if !duplicate && s.participants != nil && (inbound.ParticipantID != "" || inbound.ReplyToExternalID != "") {
		if err := s.participants.RecordInbound(ctx, ParticipantInput{
			ConnectionID: connection.ID, Provider: string(connection.Provider), ExternalID: inbound.ParticipantID,
			DisplayName: inbound.SenderName, ConversationID: conversation.ID, MessageID: stored.ID,
			ReplyToExternalID: inbound.ReplyToExternalID, Role: "member",
		}); err != nil {
			return nil, fmt.Errorf("inbox: record participant: %w", err)
		}
	}
	return &InboundResult{Conversation: conversation, Message: stored, Duplicate: duplicate}, nil
}

func (s *InboundService) ApplyDeliveryStatus(ctx context.Context, connection channeldomain.ChannelConnection, update channeldomain.DeliveryStatusUpdate) (bool, error) {
	if s == nil || s.messages == nil {
		return false, errors.New("inbox: inbound service is not configured")
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || connection.TenantID != tc.TenantID || connection.ID == uuid.Nil || update.ProviderMessageID == "" {
		return false, errors.New("inbox: trusted delivery status context required")
	}
	status := messagedomain.Status(update.State)
	switch status {
	case messagedomain.StatusQueued, messagedomain.StatusSent, messagedomain.StatusDelivered, messagedomain.StatusRead, messagedomain.StatusFailed:
	default:
		return false, errors.New("inbox: unsupported delivery status")
	}
	return s.messages.ApplyDeliveryStatus(ctx, connection.ID, update.ProviderMessageID, status)
}
