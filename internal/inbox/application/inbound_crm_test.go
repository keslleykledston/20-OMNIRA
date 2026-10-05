package application

import (
	"context"
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
	findPhoneErr     error
	findPhoneResult  map[string]string // phone -> contactID
	createResult     map[string]string // phone -> contactID
}

func (m *mockCRMConnector) FindCustomerByPhone(ctx context.Context, phone, companyID string) (string, error) {
	m.findPhoneCalls = append(m.findPhoneCalls, struct{ phone, companyID string }{phone, companyID})
	if m.findPhoneErr != nil {
		return "", m.findPhoneErr
	}
	return m.findPhoneResult[phone], nil
}

func (m *mockCRMConnector) CreateContact(ctx context.Context, name, phone, companyID string) (string, error) {
	m.createCalls = append(m.createCalls, struct{ name, phone, companyID string }{name, phone, companyID})
	return m.createResult[phone], nil
}

func TestInboundServiceWithCRMCreatesContactOnFirstMessage(t *testing.T) {
	tenantID := uuid.New()
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
	svc.WithCRM(crmMock, companyID).WithCRMAutoCreate(true) // creating a CRM contact is an explicit opt-in

	ctx := tenancydomain.WithTenantContext(context.Background(), &tenancydomain.TenantContext{
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
	tenantID := uuid.New()
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

	ctx := tenancydomain.WithTenantContext(context.Background(), &tenancydomain.TenantContext{
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

	ctx := tenancydomain.WithTenantContext(context.Background(), &tenancydomain.TenantContext{
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

type ambiguousErr struct{}

func (ambiguousErr) Error() string   { return "several contacts match" }
func (ambiguousErr) Ambiguous() bool { return true }

func ingestWithCRM(t *testing.T, crm *mockCRMConnector, autoCreate bool) *InboundResult {
	t.Helper()
	tenantID, connectionID := uuid.New(), uuid.New()
	contacts, conversations, messages, tickets := &memoryContactStore{}, &memoryConversationStore{}, &memoryMessageStore{}, &memoryTicketStore{}
	svc := NewInboundService(contacts, conversations, messages, tickets)
	svc.WithCRM(crm, uuid.New().String()).WithCRMAutoCreate(autoCreate)
	ctx := tenancydomain.WithTenantContext(context.Background(), &tenancydomain.TenantContext{TenantID: tenantID, ActorID: uuid.New(), Source: tenancydomain.AccessSourceDirect})
	contact, _ := contactdomain.NewContact(tenantID, "+5592991740090", "Alice")
	contacts.upserted = contact
	conn := domain.ChannelConnection{ID: connectionID, TenantID: tenantID, Provider: "meta_cloud"}
	res, err := svc.Ingest(ctx, conn, domain.InboundMessage{ConnectionID: conn.ID.String(), ProviderMessageID: "m-1", FromE164: "+5592991740090", SenderName: "Alice", Text: "Olá", ProviderChatID: "c-1"})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return res
}

// ADR-0018: creating a contact in the CRM is a provider write that needs a proven idempotency key; a phone lookup that
// found nothing is not one. It is OFF unless explicitly enabled.
func TestInboundNeverCreatesACRMContactUnlessOptedIn(t *testing.T) {
	crm := &mockCRMConnector{findPhoneResult: map[string]string{}, createResult: map[string]string{"+5592991740090": uuid.NewString()}}
	res := ingestWithCRM(t, crm, false)
	if len(crm.createCalls) != 0 || res.Conversation.CRMContactID != nil {
		t.Fatalf("no CRM write by default: creates=%d bound=%v", len(crm.createCalls), res.Conversation.CRMContactID)
	}
	if len(crm.findPhoneCalls) != 1 {
		t.Fatal("the read-only lookup still happens")
	}
}

// An ambiguous lookup (several contacts match) binds NOTHING and creates NOTHING: never "the first result".
func TestInboundAmbiguousCRMMatchBindsAndCreatesNothing(t *testing.T) {
	crm := &mockCRMConnector{findPhoneErr: ambiguousErr{}, createResult: map[string]string{"+5592991740090": uuid.NewString()}}
	res := ingestWithCRM(t, crm, true) // even with auto-create ON
	if res.Conversation == nil || res.Conversation.CRMContactID != nil || len(crm.createCalls) != 0 {
		t.Fatalf("ambiguous: bound=%v creates=%d", res.Conversation.CRMContactID, len(crm.createCalls))
	}
}

func TestInboundOtherCRMErrorsStillFail(t *testing.T) {
	crm := &mockCRMConnector{findPhoneErr: context.DeadlineExceeded}
	tenantID := uuid.New()
	contacts := &memoryContactStore{}
	contact, _ := contactdomain.NewContact(tenantID, "+5592991740090", "Alice")
	contacts.upserted = contact
	svc := NewInboundService(contacts, &memoryConversationStore{}, &memoryMessageStore{}, &memoryTicketStore{})
	svc.WithCRM(crm, "company")
	ctx := tenancydomain.WithTenantContext(context.Background(), &tenancydomain.TenantContext{TenantID: tenantID, ActorID: uuid.New(), Source: tenancydomain.AccessSourceDirect})
	conn := domain.ChannelConnection{ID: uuid.New(), TenantID: tenantID, Provider: "meta_cloud"}
	if _, err := svc.Ingest(ctx, conn, domain.InboundMessage{ConnectionID: conn.ID.String(), ProviderMessageID: "m", FromE164: "+5592991740090", Text: "x"}); err == nil {
		t.Fatal("a real CRM failure must still surface (the webhook retries)")
	}
}
