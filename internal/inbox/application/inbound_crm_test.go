package application

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	contactdomain "github.com/omnira/omnira/internal/contacts/domain"
	conversationdomain "github.com/omnira/omnira/internal/conversations/domain"
	messagedomain "github.com/omnira/omnira/internal/messages/domain"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketdomain "github.com/omnira/omnira/internal/tickets/domain"
)

type mockCRMConnector struct {
	findPhoneCalls   []struct{ phone, companyID string }
	createCalls      []struct{ name, phone, companyID string }
	findPhoneResult  map[string]string // phone -> contactID
	createResult     map[string]string // phone -> contactID
}

func (m *mockCRMConnector) FindCustomerByPhone(ctx context.Context, phone, companyID string) (string, error) {
	m.findPhoneCalls = append(m.findPhoneCalls, struct{ phone, companyID string }{phone, companyID})
	return m.findPhoneResult[phone], nil
}

func (m *mockCRMConnector) CreateContact(ctx context.Context, name, phone, companyID string) (string, error) {
	m.createCalls = append(m.createCalls, struct{ name, phone, companyID string }{name, phone, companyID})
	return m.createResult[phone], nil
}

func TestInboundServiceWithCRMCreatesContactOnFirstMessage(t *testing.T) {
	tenantID, contactID := uuid.New(), uuid.New()
	connectionID := uuid.New()
	companyID := uuid.New().String()
	expectedCRMContactID := uuid.New().String()

	// Mock CRM: contato não existe no primeiro envio
	crmMock := &mockCRMConnector{
		findPhoneResult: map[string]string{},
		createResult:    map[string]string{"+5592991740090": expectedCRMContactID},
	}

	// Stores em memória
	contacts := &memoryContactStore{}
	conversations := &memoryConversationStore{}
	messages := &memoryMessageStore{}
	tickets := &memoryTicketStore{}

	svc := NewInboundService(contacts, conversations, messages, tickets)
	svc.WithCRM(crmMock, companyID)

	ctx := tenancydomain.NewContext(context.Background(), &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  uuid.New(),
		Source:   tenancydomain.AccessSourceDirect,
	})

	contact, _ := contactdomain.NewContact(tenantID, "+5592991740090", "Alice")
	contacts.upserted = contact

	connection := domain.ChannelConnection{
		ID:       connectionID,
		TenantID: tenantID,
		Provider: "meta_cloud",
	}

	inbound := domain.InboundMessage{
		ConnectionID:    connection.ID.String(),
		ProviderMessageID: "msg-123",
		FromE164:        "+5592991740090",
		SenderName:      "Alice",
		Text:            "Olá",
		ProviderChatID:  "chat-999",
	}

	result, err := svc.Ingest(ctx, connection, inbound)

	if err != nil {
		t.Fatalf("Ingest falhou: %v", err)
	}

	if result.Conversation == nil {
		t.Fatal("esperava conversation")
	}

	if result.Conversation.CRMContactID == nil {
		t.Fatal("CRMContactID não foi preenchido")
	}

	if result.Conversation.CRMContactID.String() != expectedCRMContactID {
		t.Fatalf("CRMContactID=%s, esperava %s", result.Conversation.CRMContactID, expectedCRMContactID)
	}

	// Validar que FindCustomerByPhone foi chamado
	if len(crmMock.findPhoneCalls) != 1 {
		t.Fatalf("FindCustomerByPhone não foi chamado corretamente: %d calls", len(crmMock.findPhoneCalls))
	}

	// Validar que CreateContact foi chamado (porque não encontrou)
	if len(crmMock.createCalls) != 1 {
		t.Fatalf("CreateContact não foi chamado: %d calls", len(crmMock.createCalls))
	}

	createCall := crmMock.createCalls[0]
	if createCall.phone != "+5592991740090" || createCall.companyID != companyID {
		t.Fatalf("CreateContact chamado com valores errados: %+v", createCall)
	}
}

func TestInboundServiceWithCRMReusesExistingContact(t *testing.T) {
	tenantID, contactID := uuid.New(), uuid.New()
	connectionID := uuid.New()
	companyID := uuid.New().String()
	existingCRMContactID := uuid.New().String()

	// Mock CRM: contato já existe
	crmMock := &mockCRMConnector{
		findPhoneResult: map[string]string{"+5592991740090": existingCRMContactID},
		createResult:    map[string]string{},
	}

	contacts := &memoryContactStore{}
	conversations := &memoryConversationStore{}
	messages := &memoryMessageStore{}
	tickets := &memoryTicketStore{}

	svc := NewInboundService(contacts, conversations, messages, tickets)
	svc.WithCRM(crmMock, companyID)

	ctx := tenancydomain.NewContext(context.Background(), &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  uuid.New(),
		Source:   tenancydomain.AccessSourceDirect,
	})

	contact, _ := contactdomain.NewContact(tenantID, "+5592991740090", "Bob")
	contacts.upserted = contact

	connection := domain.ChannelConnection{
		ID:       connectionID,
		TenantID: tenantID,
		Provider: "meta_cloud",
	}

	inbound := domain.InboundMessage{
		ConnectionID:     connection.ID.String(),
		ProviderMessageID: "msg-456",
		FromE164:        "+5592991740090",
		SenderName:      "Bob",
		Text:            "Oi",
		ProviderChatID:  "chat-888",
	}

	result, err := svc.Ingest(ctx, connection, inbound)

	if err != nil {
		t.Fatalf("Ingest falhou: %v", err)
	}

	if result.Conversation.CRMContactID == nil {
		t.Fatal("CRMContactID não foi preenchido")
	}

	if result.Conversation.CRMContactID.String() != existingCRMContactID {
		t.Fatalf("CRMContactID=%s, esperava %s", result.Conversation.CRMContactID, existingCRMContactID)
	}

	// Validar que FindCustomerByPhone foi chamado
	if len(crmMock.findPhoneCalls) != 1 {
		t.Fatal("FindCustomerByPhone não foi chamado")
	}

	// Validar que CreateContact NÃO foi chamado (porque encontrou)
	if len(crmMock.createCalls) != 0 {
		t.Fatalf("CreateContact não deveria ter sido chamado, mas foi: %d calls", len(crmMock.createCalls))
	}
}

func TestInboundServiceWithoutCRMWorksAsNormal(t *testing.T) {
	tenantID := uuid.New()
	connectionID := uuid.New()

	contacts := &memoryContactStore{}
	conversations := &memoryConversationStore{}
	messages := &memoryMessageStore{}
	tickets := &memoryTicketStore{}

	svc := NewInboundService(contacts, conversations, messages, tickets)
	// Sem WithCRM: crm = nil

	ctx := tenancydomain.NewContext(context.Background(), &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  uuid.New(),
		Source:   tenancydomain.AccessSourceDirect,
	})

	contact, _ := contactdomain.NewContact(tenantID, "+5592991740090", "Charlie")
	contacts.upserted = contact

	connection := domain.ChannelConnection{
		ID:       connectionID,
		TenantID: tenantID,
		Provider: "meta_cloud",
	}

	inbound := domain.InboundMessage{
		ConnectionID:     connection.ID.String(),
		ProviderMessageID: "msg-789",
		FromE164:        "+5592991740090",
		SenderName:      "Charlie",
		Text:            "Tudo bem",
		ProviderChatID:  "chat-777",
	}

	result, err := svc.Ingest(ctx, connection, inbound)

	if err != nil {
		t.Fatalf("Ingest falhou: %v", err)
	}

	// CRMContactID deve estar nil (nenhum CRM configurado)
	if result.Conversation.CRMContactID != nil {
		t.Fatal("CRMContactID deveria ser nil quando nenhum CRM configurado")
	}
}

type memoryContactStore struct {
	upserted *contactdomain.Contact
}

func (m *memoryContactStore) UpsertByPhone(ctx context.Context, c *contactdomain.Contact) (*contactdomain.Contact, error) {
	m.upserted = c
	return c, nil
}

type memoryConversationStore struct {
	stored  *conversationdomain.Conversation
	findErr error
}

func (m *memoryConversationStore) FindOpen(ctx context.Context, contactID, connectionID uuid.UUID) (*conversationdomain.Conversation, error) {
	return nil, m.findErr // Simula: não encontrou (nil é normal para novo inbound)
}

func (m *memoryConversationStore) Store(ctx context.Context, c *conversationdomain.Conversation) error {
	m.stored = c
	return nil
}

type memoryMessageStore struct{}

func (m *memoryMessageStore) StoreInbound(ctx context.Context, msg *messagedomain.Message) (*messagedomain.Message, bool, error) {
	return msg, false, nil // duplicate=false
}

func (m *memoryMessageStore) ApplyDeliveryStatus(ctx context.Context, connID uuid.UUID, providerMsgID string, status messagedomain.Status) (bool, error) {
	return true, nil
}

type memoryTicketStore struct{}

func (m *memoryTicketStore) FindOpenByConversation(ctx context.Context, conversationID uuid.UUID) (*ticketdomain.Ticket, error) {
	return nil, nil // Simula: não encontrou (nil é normal para novo inbound)
}

func (m *memoryTicketStore) Store(ctx context.Context, t *ticketdomain.Ticket) error {
	return nil
}
