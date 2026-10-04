package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	contactdomain "github.com/omnira/omnira/internal/contacts/domain"
	conversationdomain "github.com/omnira/omnira/internal/conversations/domain"
	messagedomain "github.com/omnira/omnira/internal/messages/domain"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketdomain "github.com/omnira/omnira/internal/tickets/domain"
)

type memoryStores struct {
	contact      *contactdomain.Contact
	conversation *conversationdomain.Conversation
	message      *messagedomain.Message
	ticket       *ticketdomain.Ticket
	routed       uuid.UUID
	routeCalls   int
}

func (m *memoryStores) UpsertByPhone(_ context.Context, c *contactdomain.Contact) (*contactdomain.Contact, error) {
	if m.contact == nil {
		m.contact = c
	}
	return m.contact, nil
}
func (m *memoryStores) FindOpen(_ context.Context, _, _ uuid.UUID) (*conversationdomain.Conversation, error) {
	return m.conversation, nil
}
func (m *memoryStores) Store(_ context.Context, c *conversationdomain.Conversation) error {
	m.conversation = c
	return nil
}
func (m *memoryStores) StoreInbound(_ context.Context, msg *messagedomain.Message) (*messagedomain.Message, bool, error) {
	if m.message != nil {
		return m.message, true, nil
	}
	m.message = msg
	return msg, false, nil
}
func (m *memoryStores) ApplyDeliveryStatus(_ context.Context, _ uuid.UUID, _ string, status messagedomain.Status) (bool, error) {
	if m.message == nil {
		return false, nil
	}
	m.message.Status = status
	return true, nil
}
func (m *memoryStores) FindOpenByConversation(_ context.Context, _ uuid.UUID) (*ticketdomain.Ticket, error) {
	return m.ticket, nil
}
func (m *memoryStores) StoreTicket(_ context.Context, ticket *ticketdomain.Ticket) error {
	m.ticket = ticket
	return nil
}
func (m *memoryStores) RouteNew(_ context.Context, conversationID uuid.UUID) error {
	m.routed = conversationID
	m.routeCalls++
	return nil
}

type ticketAdapter struct{ *memoryStores }

func (a ticketAdapter) Store(ctx context.Context, ticket *ticketdomain.Ticket) error {
	return a.StoreTicket(ctx, ticket)
}

func inboundContext(t *testing.T, tenantID uuid.UUID) context.Context {
	t.Helper()
	tc, err := tenancydomain.NewTenantContext(tenantID, uuid.New(), tenancydomain.AccessSourceSystem)
	if err != nil {
		t.Fatal(err)
	}
	return tenancydomain.WithTenantContext(context.Background(), tc)
}

func TestIngestCreatesCanonicalTenantOwnedChain(t *testing.T) {
	tenantID, connectionID := uuid.New(), uuid.New()
	stores := &memoryStores{}
	svc := NewInboundService(stores, stores, stores, ticketAdapter{stores}, stores)
	result, err := svc.Ingest(inboundContext(t, tenantID), channeldomain.ChannelConnection{ID: connectionID, TenantID: tenantID}, channeldomain.InboundMessage{ConnectionID: connectionID.String(), ProviderMessageID: "wamid-1", FromE164: "+5511999999999", Text: "oi"})
	if err != nil || result.Duplicate || result.Ticket == nil {
		t.Fatalf("unexpected ingest: %+v %v", result, err)
	}
	if result.Contact.TenantID != tenantID || result.Conversation.TenantID != tenantID || result.Message.TenantID != tenantID || result.Ticket.TenantID != tenantID {
		t.Fatal("tenant ownership not propagated")
	}
	if stores.routeCalls != 1 || stores.routed != result.Conversation.ID {
		t.Fatalf("initial routing calls=%d conversation=%s", stores.routeCalls, stores.routed)
	}
}

func TestIngestRejectsConnectionTenantConfusion(t *testing.T) {
	stores := &memoryStores{}
	svc := NewInboundService(stores, stores, stores, ticketAdapter{stores})
	connectionID := uuid.New()
	_, err := svc.Ingest(inboundContext(t, uuid.New()), channeldomain.ChannelConnection{ID: connectionID, TenantID: uuid.New()}, channeldomain.InboundMessage{ConnectionID: connectionID.String(), ProviderMessageID: "wamid-1", FromE164: "+5511999999999", Text: "oi"})
	if err == nil {
		t.Fatal("cross-tenant connection accepted")
	}
}

func TestIngestRedeliveryDoesNotCreateSecondTicket(t *testing.T) {
	tenantID, connectionID := uuid.New(), uuid.New()
	stores := &memoryStores{}
	svc := NewInboundService(stores, stores, stores, ticketAdapter{stores}, stores)
	ctx := inboundContext(t, tenantID)
	connection := channeldomain.ChannelConnection{ID: connectionID, TenantID: tenantID}
	inbound := channeldomain.InboundMessage{ConnectionID: connectionID.String(), ProviderMessageID: "wamid-1", FromE164: "+5511999999999", Text: "oi"}
	if _, err := svc.Ingest(ctx, connection, inbound); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Ingest(ctx, connection, inbound)
	if err != nil || !got.Duplicate || got.Ticket != nil {
		t.Fatalf("redelivery was not idempotent: %+v %v", got, err)
	}
	if stores.routeCalls != 1 {
		t.Fatalf("redelivery routed %d times", stores.routeCalls)
	}
}

func TestIngestMediaOnlyMessage(t *testing.T) {
	tenantID, connectionID := uuid.New(), uuid.New()
	stores := &memoryStores{}
	svc := NewInboundService(stores, stores, stores, ticketAdapter{stores})
	result, err := svc.Ingest(inboundContext(t, tenantID), channeldomain.ChannelConnection{ID: connectionID, TenantID: tenantID}, channeldomain.InboundMessage{ConnectionID: connectionID.String(), ProviderMessageID: "media-1", FromE164: "+5511999999999", Media: &channeldomain.InboundMedia{Kind: channeldomain.MediaKindImage, MediaRef: "opaque-ref", MimeType: "image/jpeg"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.MessageType != "image" || result.Message.MediaRef != "opaque-ref" || result.Message.MimeType != "image/jpeg" {
		t.Fatalf("media not canonicalized: %+v", result.Message)
	}
}

type recorderStub struct {
	calls []ParticipantInput
	err   error
}

func (r *recorderStub) RecordInbound(_ context.Context, in ParticipantInput) error {
	r.calls = append(r.calls, in)
	return r.err
}

// ADR-0017 Wave 2: participant and reply metadata flow to the recorder once per NEW message, never for a redelivery,
// and a service without a recorder behaves exactly as before.
func TestIngestRecordsParticipantAndReplyOncePerNewMessage(t *testing.T) {
	tenantID, connectionID := uuid.New(), uuid.New()
	stores, rec := &memoryStores{}, &recorderStub{}
	svc := NewInboundService(stores, stores, stores, ticketAdapter{stores}, stores).WithParticipants(rec)
	ctx := inboundContext(t, tenantID)
	connection := channeldomain.ChannelConnection{ID: connectionID, TenantID: tenantID, Provider: channeldomain.ProviderWAHA}
	inbound := channeldomain.InboundMessage{ConnectionID: connectionID.String(), ProviderMessageID: "m-1", FromE164: "+5511999999999", Text: "sim",
		SenderName: "Ana", ParticipantID: "111@lid", ReplyToExternalID: "3EB0ORIG"}
	first, err := svc.Ingest(ctx, connection, inbound)
	if err != nil || len(rec.calls) != 1 {
		t.Fatalf("calls=%d err=%v", len(rec.calls), err)
	}
	c := rec.calls[0]
	if c.ExternalID != "111@lid" || c.ReplyToExternalID != "3EB0ORIG" || c.Provider != "waha" || c.DisplayName != "Ana" ||
		c.ContactID != first.Contact.ID || c.ConversationID != first.Conversation.ID || c.MessageID != first.Message.ID || c.ConnectionID != connectionID {
		t.Fatalf("recorder input = %+v", c)
	}
	if _, err := svc.Ingest(ctx, connection, inbound); err != nil || len(rec.calls) != 1 {
		t.Fatalf("a redelivery must not record again: calls=%d err=%v", len(rec.calls), err)
	}
	// nothing to record: no call (ingestion stays as cheap as before)
	plain := channeldomain.InboundMessage{ConnectionID: connectionID.String(), ProviderMessageID: "m-2", FromE164: "+5511999999999", Text: "oi"}
	if _, err := svc.Ingest(ctx, connection, plain); err != nil || len(rec.calls) != 1 {
		t.Fatalf("no metadata, no call: calls=%d err=%v", len(rec.calls), err)
	}
}

func TestIngestWithoutARecorderIsUnchangedAndARecorderFailureIsNotSwallowed(t *testing.T) {
	tenantID, connectionID := uuid.New(), uuid.New()
	ctx := inboundContext(t, tenantID)
	connection := channeldomain.ChannelConnection{ID: connectionID, TenantID: tenantID}
	inbound := channeldomain.InboundMessage{ConnectionID: connectionID.String(), ProviderMessageID: "m-1", FromE164: "+5511999999999", Text: "oi", ParticipantID: "1@lid"}
	stores := &memoryStores{}
	if _, err := NewInboundService(stores, stores, stores, ticketAdapter{stores}, stores).Ingest(ctx, connection, inbound); err != nil {
		t.Fatalf("no recorder configured must be fine: %v", err)
	}
	stores2 := &memoryStores{}
	failing := NewInboundService(stores2, stores2, stores2, ticketAdapter{stores2}, stores2).WithParticipants(&recorderStub{err: context.DeadlineExceeded})
	if _, err := failing.Ingest(ctx, connection, inbound); err == nil {
		t.Fatal("a failing recorder must surface (the webhook retries) instead of silently dropping the metadata")
	}
}
