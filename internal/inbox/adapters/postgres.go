package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	contactdomain "github.com/omnira/omnira/internal/contacts/domain"
	conversationdomain "github.com/omnira/omnira/internal/conversations/domain"
	inboxapp "github.com/omnira/omnira/internal/inbox/application"
	messagedomain "github.com/omnira/omnira/internal/messages/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketdomain "github.com/omnira/omnira/internal/tickets/domain"
)

// PostgresInboundStore keeps every write scoped by both TenantContext and the
// transaction querier. The service composes these methods in one transaction.
type PostgresInboundStore struct{ pool *pgxpool.Pool }

var _ inboxapp.ContactStore = (*PostgresInboundStore)(nil)
var _ inboxapp.ConversationStore = (*PostgresInboundStore)(nil)
var _ inboxapp.MessageStore = (*PostgresInboundStore)(nil)
var _ inboxapp.TicketStore = TicketStore{}

func NewPostgresInboundStore(pool *pgxpool.Pool) *PostgresInboundStore {
	return &PostgresInboundStore{pool: pool}
}

func (s *PostgresInboundStore) UpsertByPhone(ctx context.Context, contact *contactdomain.Contact) (*contactdomain.Contact, error) {
	if err := sameTenant(ctx, contact.TenantID); err != nil {
		return nil, err
	}
	return scanContact(platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		INSERT INTO contacts (tenant_id, display_name, phone_e164, email, status)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tenant_id, phone_e164) DO UPDATE SET
			-- O nome vem do perfil do remetente, que ele escolhe livremente.
			-- Serve para batizar um contato que ainda não tem nome real (vazio
			-- ou o próprio telefone como placeholder), mas nunca sobrescreve um
			-- nome já curado pelo operador ou pelo CRM.
			display_name = CASE
				WHEN EXCLUDED.display_name = '' THEN contacts.display_name
				WHEN contacts.display_name IN ('', contacts.phone_e164) THEN EXCLUDED.display_name
				ELSE contacts.display_name
			END,
			updated_at = now()
		RETURNING id, tenant_id, display_name, phone_e164, email, status, created_at, updated_at`,
		contact.TenantID, contact.DisplayName, contact.PhoneE164, contact.Email, contact.Status))
}

func (s *PostgresInboundStore) FindOpen(ctx context.Context, contactID, connectionID uuid.UUID) (*conversationdomain.Conversation, error) {
	tenantID, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	return scanConversation(platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		SELECT id, tenant_id, contact_id, channel_connection_id, provider_chat_id, crm_contact_id, status, title, created_at, updated_at, closed_at
		FROM conversations WHERE tenant_id=$1 AND contact_id=$2 AND channel_connection_id=$3 AND status='open'
		ORDER BY updated_at DESC, id DESC LIMIT 1`, tenantID, contactID, connectionID))
}

func (s *PostgresInboundStore) Store(ctx context.Context, conversation *conversationdomain.Conversation) error {
	if conversation == nil {
		return errors.New("inbox: conversation required")
	}
	if err := sameTenant(ctx, conversation.TenantID); err != nil {
		return err
	}
	_, err := platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx, `
		INSERT INTO conversations (id, tenant_id, contact_id, channel_connection_id, provider_chat_id, crm_contact_id, status, title, created_at, updated_at, closed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, conversation.ID, conversation.TenantID, conversation.ContactID, conversation.ChannelConnectionID, conversation.ProviderChatID, conversation.CRMContactID, conversation.Status, conversation.Title, conversation.CreatedAt, conversation.UpdatedAt, conversation.ClosedAt)
	return err
}

// RouteNew selects only the tenant's explicit default queue. Round-robin
// queues enqueue a durable reference in the same transaction as inbound
// persistence; manual queues remain visible for operator claim.
func (s *PostgresInboundStore) RouteNew(ctx context.Context, conversationID uuid.UUID) error {
	tenantID, err := tenantID(ctx)
	if err != nil {
		return err
	}
	_, err = platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx, `
		WITH selected AS (
		  SELECT id,mode FROM queues WHERE tenant_id=$1 AND is_default ORDER BY id LIMIT 1
		), routed AS (
		  -- routing_retry_at starts comfortably past the JetStream in-flight
		  -- window (routing.SweepRetryBackoff, internal/worker/routing/liveness.go)
		  -- so the IAM4.2-B0 safety sweep never fires on a conversation still
		  -- being retried by its original job. NULL for non-round_robin
		  -- (manual) queues: the sweep already excludes them by mode.
		  UPDATE conversations c SET queue_id=selected.id,updated_at=now(),
		    routing_retry_at = CASE WHEN selected.mode='round_robin' THEN now() + interval '90 seconds' ELSE NULL END
		  FROM selected WHERE c.tenant_id=$1 AND c.id=$2 AND c.queue_id IS NULL
		    -- ADR-0014: a spam contact's conversation is stored but never routed to a queue.
		    AND NOT EXISTS (SELECT 1 FROM contacts ct WHERE ct.tenant_id=c.tenant_id AND ct.id=c.contact_id AND ct.kind='spam')
		  RETURNING c.id,selected.mode
		)
		INSERT INTO outbox_events
		  (id,tenant_id,event_type,aggregate_type,aggregate_id,correlation_id,payload)
		SELECT $3,$1,'job.routing.assign.v1','conversation',id::text,$4,'{}'::jsonb
		FROM routed WHERE mode='round_robin'`, tenantID, conversationID, uuid.New(), uuid.New())
	return err
}

func (s *PostgresInboundStore) StoreInbound(ctx context.Context, message *messagedomain.Message) (*messagedomain.Message, bool, error) {
	if message == nil {
		return nil, false, errors.New("inbox: message required")
	}
	if err := sameTenant(ctx, message.TenantID); err != nil {
		return nil, false, err
	}
	stored, err := scanMessage(platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		INSERT INTO messages (id, tenant_id, conversation_id, channel_connection_id, direction, message_type, body, media_ref, mime_type, size_bytes, provider_message_id, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (tenant_id, channel_connection_id, provider_message_id)
		  WHERE provider_message_id <> '' AND channel_connection_id IS NOT NULL DO NOTHING
		RETURNING id, tenant_id, conversation_id, channel_connection_id, direction, message_type, body, media_ref, mime_type, size_bytes, provider_message_id, status, created_at, updated_at`,
		message.ID, message.TenantID, message.ConversationID, message.ChannelConnectionID, message.Direction, message.MessageType, message.Body, message.MediaRef, message.MimeType, message.SizeBytes, message.ProviderMessageID, message.Status, message.CreatedAt, message.UpdatedAt))
	if err == nil {
		return stored, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	duplicate, lookupErr := scanMessage(platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		SELECT id, tenant_id, conversation_id, channel_connection_id, direction, message_type, body, media_ref, mime_type, size_bytes, provider_message_id, status, created_at, updated_at
		FROM messages WHERE tenant_id=$1 AND channel_connection_id=$2 AND provider_message_id=$3`, message.TenantID, message.ChannelConnectionID, message.ProviderMessageID))
	return duplicate, true, lookupErr
}

// WahaMessageIDTail returns the bare message id from WAHA's serialized form
// "<fromMe>_<chatId>_<id>[_<participant>]". The same sent message can carry a different chat part in
// the send answer (the phone, "@c.us") and in later receipts (the opaque "@lid" the contact is
// addressed by), but the id segment is the same, and it is exactly the id OMNIRA reserved before
// sending. Ids that do not have that shape return "" and are matched only by their full value.
func WahaMessageIDTail(providerMessageID string) string {
	parts := strings.Split(providerMessageID, "_")
	if len(parts) >= 3 && (parts[0] == "true" || parts[0] == "false") && parts[2] != "" {
		return parts[2]
	}
	return ""
}

// ApplyDeliveryStatus moves an outbound message forward (never backward) to the status a receipt
// reports. The message is found by its full provider id OR, for messages sent with a reserved id,
// by that id's bare tail (unique per connection), so a receipt that names the chat differently
// still lands on the right message.
func (s *PostgresInboundStore) ApplyDeliveryStatus(ctx context.Context, connectionID uuid.UUID, providerMessageID string, status messagedomain.Status) (bool, error) {
	tenantID, err := tenantID(ctx)
	if err != nil {
		return false, err
	}
	result, err := platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx, `
		UPDATE messages SET status=$4, updated_at=now()
		WHERE tenant_id=$1 AND channel_connection_id=$2
		  AND direction='outbound'
		  AND (provider_message_id=$3 OR ($5 <> '' AND reserved_provider_message_id=$5))
		  AND CASE status
		    WHEN 'queued' THEN 0 WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE 4 END
		      <= CASE $4 WHEN 'queued' THEN 0 WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE 4 END
		  AND NOT (status IN ('delivered','read') AND $4='failed')`, tenantID, connectionID, providerMessageID, status, WahaMessageIDTail(providerMessageID))
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

func (s *PostgresInboundStore) FindOpenByConversation(ctx context.Context, conversationID uuid.UUID) (*ticketdomain.Ticket, error) {
	tenantID, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	return scanTicket(platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		SELECT id, tenant_id, conversation_id, status, priority, subject, assigned_to, created_at, updated_at, resolved_at, closed_at
		FROM tickets WHERE tenant_id=$1 AND conversation_id=$2 AND status IN ('open','in_progress','waiting')
		ORDER BY topic_scoped ASC, updated_at DESC, id DESC LIMIT 1`, tenantID, conversationID))
}

func (s *PostgresInboundStore) StoreTicket(ctx context.Context, ticket *ticketdomain.Ticket) error {
	if ticket == nil {
		return errors.New("inbox: ticket required")
	}
	if err := sameTenant(ctx, ticket.TenantID); err != nil {
		return err
	}
	_, err := platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx, `
		INSERT INTO tickets (id, tenant_id, conversation_id, status, priority, subject, assigned_to, created_at, updated_at, resolved_at, closed_at, topic_scoped)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, ticket.ID, ticket.TenantID, ticket.ConversationID, ticket.Status, ticket.Priority, ticket.Subject, ticket.AssignedTo, ticket.CreatedAt, ticket.UpdatedAt, ticket.ResolvedAt, ticket.ClosedAt, ticket.TopicScoped)
	return err
}

// Store satisfies the ticket port only through this wrapper to avoid a Go
// method-name collision with ConversationStore.Store.
type TicketStore struct{ *PostgresInboundStore }

func (s TicketStore) Store(ctx context.Context, ticket *ticketdomain.Ticket) error {
	return s.StoreTicket(ctx, ticket)
}

func tenantID(ctx context.Context) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("inbox: tenant context required: %w", err)
	}
	return tc.TenantID, nil
}
func sameTenant(ctx context.Context, id uuid.UUID) error {
	current, err := tenantID(ctx)
	if err != nil {
		return err
	}
	if current != id {
		return errors.New("inbox: tenant context mismatch")
	}
	return nil
}

type scanner interface{ Scan(...any) error }

func scanContact(row scanner) (*contactdomain.Contact, error) {
	c := &contactdomain.Contact{}
	err := row.Scan(&c.ID, &c.TenantID, &c.DisplayName, &c.PhoneE164, &c.Email, &c.Status, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}
func scanConversation(row scanner) (*conversationdomain.Conversation, error) {
	c := &conversationdomain.Conversation{}
	var status string
	err := row.Scan(&c.ID, &c.TenantID, &c.ContactID, &c.ChannelConnectionID, &c.ProviderChatID, &c.CRMContactID, &status, &c.Title, &c.CreatedAt, &c.UpdatedAt, &c.ClosedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	c.Status = conversationdomain.Status(status)
	return c, err
}
func scanMessage(row scanner) (*messagedomain.Message, error) {
	m := &messagedomain.Message{}
	var direction, status string
	err := row.Scan(&m.ID, &m.TenantID, &m.ConversationID, &m.ChannelConnectionID, &direction, &m.MessageType, &m.Body, &m.MediaRef, &m.MimeType, &m.SizeBytes, &m.ProviderMessageID, &status, &m.CreatedAt, &m.UpdatedAt)
	m.Direction = messagedomain.Direction(direction)
	m.Status = messagedomain.Status(status)
	return m, err
}
func scanTicket(row scanner) (*ticketdomain.Ticket, error) {
	t := &ticketdomain.Ticket{}
	var status, priority string
	err := row.Scan(&t.ID, &t.TenantID, &t.ConversationID, &status, &priority, &t.Subject, &t.AssignedTo, &t.CreatedAt, &t.UpdatedAt, &t.ResolvedAt, &t.ClosedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	t.Status = ticketdomain.Status(status)
	t.Priority = ticketdomain.Priority(priority)
	return t, err
}
