package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/messages/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// PostgresOutboundStore runs inside the request's tenant session (SET LOCAL +
// RLS); it never opens its own transaction.
type PostgresOutboundStore struct{ pool *pgxpool.Pool }

var _ ports.OutboundStore = (*PostgresOutboundStore)(nil)

func NewPostgresOutboundStore(pool *pgxpool.Pool) *PostgresOutboundStore {
	return &PostgresOutboundStore{pool: pool}
}

func tenantOf(ctx context.Context) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("messages: tenant context required")
	}
	return tc.TenantID, nil
}

func (s *PostgresOutboundStore) LoadSendContext(ctx context.Context, conversationID uuid.UUID) (*ports.SendContext, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var (
		out   ports.SendContext
		ready *bool
		phone *string
	)
	err = platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		SELECT c.id, c.assigned_to_user_id, c.channel_connection_id,
		       (cc.status = 'active' AND cc.capabilities ? 'text'), ct.phone_e164
		FROM conversations c
		JOIN contacts ct ON ct.tenant_id = c.tenant_id AND ct.id = c.contact_id
		LEFT JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id
		WHERE c.tenant_id = $1 AND c.id = $2`, tenantID, conversationID).
		Scan(&out.ConversationID, &out.AssignedTo, &out.ConnectionID, &ready, &phone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("messages: load conversation: %w", err)
	}
	out.ConnectionReady = ready != nil && *ready
	if phone != nil {
		out.ToE164 = *phone
	}
	return &out, nil
}

func (s *PostgresOutboundStore) InsertQueued(ctx context.Context, sender uuid.UUID, in ports.SendContext, body, key, hash string) (*ports.QueuedMessage, bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, false, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	msg := &ports.QueuedMessage{ID: uuid.New(), ConversationID: in.ConversationID, Body: body, Status: "queued", RequestHash: hash}
	// One statement: the message, its delivery job (reference only — no text,
	// phone or secret enters the queue) and the conversation touch commit together.
	err = q.QueryRow(ctx, `
		WITH ins AS (
		  INSERT INTO messages
		    (id, tenant_id, conversation_id, channel_connection_id, direction, message_type, body, status,
		     idempotency_key, request_hash, sent_by_user_id)
		  VALUES ($1,$2,$3,$4,'outbound','text',$5,'queued',$6,$7,$8)
		  ON CONFLICT (tenant_id, sent_by_user_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		  RETURNING id, created_at
		), job AS (
		  INSERT INTO outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, payload)
		  SELECT $9, $2, 'job.channel.send_text.v1', 'message', id::text, $10, '{}'::jsonb FROM ins
		  RETURNING id
		), touched AS (
		  UPDATE conversations SET updated_at = now()
		  WHERE tenant_id = $2 AND id = $3 AND EXISTS (SELECT 1 FROM ins)
		  RETURNING id
		)
		SELECT id, created_at FROM ins`,
		msg.ID, tenantID, in.ConversationID, in.ConnectionID, body, key, hash, sender, uuid.New(), uuid.New()).
		Scan(&msg.ID, &msg.CreatedAt)
	if err == nil {
		return msg, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("messages: queue outbound: %w", err)
	}
	existing := &ports.QueuedMessage{}
	err = q.QueryRow(ctx, `
		SELECT id, conversation_id, body, status, created_at, COALESCE(request_hash, '')
		FROM messages WHERE tenant_id = $1 AND sent_by_user_id = $2 AND idempotency_key = $3`,
		tenantID, sender, key).
		Scan(&existing.ID, &existing.ConversationID, &existing.Body, &existing.Status, &existing.CreatedAt, &existing.RequestHash)
	if err != nil {
		return nil, false, fmt.Errorf("messages: load idempotent message: %w", err)
	}
	return existing, true, nil
}
