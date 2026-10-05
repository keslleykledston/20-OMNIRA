package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusOpen   Status = "open"
	StatusClosed Status = "closed"
)

type Conversation struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	ContactID           uuid.UUID
	ChannelConnectionID *uuid.UUID
	// ProviderChatID é o endereço da conversa no provedor, como ele o informou.
	// A resposta deve ir para esse endereço: derivá-lo do telefone falha quando
	// o provedor endereça o contato por outro identificador (ex.: LID do
	// WhatsApp). Vazio significa "desconhecido" — aí o envio deriva do telefone.
	ProviderChatID string
	// CRMContactID é o UUID do contato no CRM K3G, gravado quando inbound chega.
	// Permite reusar contato no CRM sem duplicata em próximos atendimentos.
	CRMContactID *uuid.UUID
	// InternalUserID is set INSTEAD of ContactID when the other side is a verified internal user (ADR-0018): staff are
	// never contacts.
	InternalUserID *uuid.UUID
	// Kind is derived from who takes part; the zero value means "let the store derive it from the contact".
	Kind                        Kind
	HasUnclassifiedParticipants bool
	Status                      Status
	Title                       string
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
	ClosedAt                    *time.Time
}

func NewConversation(tenantID, contactID uuid.UUID, connectionID *uuid.UUID) (*Conversation, error) {
	if tenantID == uuid.Nil || contactID == uuid.Nil {
		return nil, errors.New("conversation: tenant and contact are required")
	}
	now := time.Now().UTC()
	return &Conversation{ID: uuid.New(), TenantID: tenantID, ContactID: contactID, ChannelConnectionID: connectionID, Status: StatusOpen, CreatedAt: now, UpdatedAt: now}, nil
}

// NewInternalConversation is a 1:1 conversation with a verified internal user. It has no Contact.
func NewInternalConversation(tenantID, userID uuid.UUID, connectionID *uuid.UUID) (*Conversation, error) {
	if tenantID == uuid.Nil || userID == uuid.Nil {
		return nil, errors.New("conversation: tenant and internal user are required")
	}
	now := time.Now().UTC()
	return &Conversation{ID: uuid.New(), TenantID: tenantID, InternalUserID: &userID, Kind: KindInternal, ChannelConnectionID: connectionID, Status: StatusOpen, CreatedAt: now, UpdatedAt: now}, nil
}

func (c *Conversation) Close() {
	now := time.Now().UTC()
	c.Status, c.ClosedAt, c.UpdatedAt = StatusClosed, &now, now
}
