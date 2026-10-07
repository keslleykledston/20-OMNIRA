package adapters

import (
	"context"
	"encoding/json"
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
		       (cc.status = 'active' AND cc.capabilities ? 'text'), ct.phone_e164, COALESCE(cc.provider,''),
		       (SELECT max(m.created_at) FROM messages m WHERE m.tenant_id = c.tenant_id AND m.conversation_id = c.id AND m.direction = 'inbound'),
		       (c.status = 'closed')
		FROM conversations c
		JOIN contacts ct ON ct.tenant_id = c.tenant_id AND ct.id = c.contact_id
		LEFT JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id
		WHERE c.tenant_id = $1 AND c.id = $2`, tenantID, conversationID).
		Scan(&out.ConversationID, &out.AssignedTo, &out.ConnectionID, &ready, &phone, &out.Provider, &out.LastInboundAt, &out.Closed)
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

// LoadTemplate reads a template only if it belongs to that connection: a template id from another line or tenant is nil.
func (s *PostgresOutboundStore) LoadTemplate(ctx context.Context, connectionID, templateID uuid.UUID) (*ports.Template, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	t := &ports.Template{}
	err = platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		SELECT id, name, language, body_text, status, variable_count, sendable
		FROM channel_message_templates WHERE tenant_id = $1 AND connection_id = $2 AND id = $3`, tenantID, connectionID, templateID).
		Scan(&t.ID, &t.Name, &t.Language, &t.Body, &t.Status, &t.VariableCount, &t.Sendable)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("messages: load template: %w", err)
	}
	return t, nil
}

func (s *PostgresOutboundStore) InsertQueuedTemplate(ctx context.Context, sender uuid.UUID, in ports.SendContext, body, key, hash string, requireAssignee bool, tpl ports.TemplateSend) (*ports.QueuedMessage, bool, error) {
	msg, replayed, err := s.InsertQueued(ctx, sender, in, body, key, hash, requireAssignee)
	if err != nil || replayed {
		return msg, replayed, err
	}
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, false, err
	}
	params, err := json.Marshal(tpl.Params)
	if err != nil {
		return nil, false, fmt.Errorf("messages: encode template params: %w", err)
	}
	// Same request transaction as the message and its job: either all three exist or none does.
	if _, err := platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx,
		`INSERT INTO message_template_sends (tenant_id, message_id, template_name, language, params) VALUES ($1,$2,$3,$4,$5)`,
		tenantID, msg.ID, tpl.Name, tpl.Language, params); err != nil {
		return nil, false, fmt.Errorf("messages: record template send: %w", err)
	}
	return msg, false, nil
}

func (s *PostgresOutboundStore) InsertQueued(ctx context.Context, sender uuid.UUID, in ports.SendContext, body, key, hash string, requireAssignee bool) (*ports.QueuedMessage, bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, false, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	msg := &ports.QueuedMessage{ID: uuid.New(), ConversationID: in.ConversationID, Body: body, Status: "queued", RequestHash: hash}
	// One statement: the message, its delivery job (reference only — no text,
	// phone or secret enters the queue) and the conversation touch commit together.
	err = q.QueryRow(ctx, `
		WITH target AS (
		  SELECT c.id, c.tenant_id, c.channel_connection_id
		  FROM conversations c
		  JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id
		       AND cc.status = 'active' AND cc.capabilities ? 'text'
		  WHERE c.tenant_id = $2 AND c.id = $3
		    AND c.channel_connection_id IS NOT DISTINCT FROM $4::uuid
		    AND (NOT $11::bool OR c.assigned_to_user_id = $8)
		  FOR SHARE OF c            -- blocks a concurrent assign/unassign (FOR UPDATE) until we commit
		), ins AS (
		  INSERT INTO messages
		    (id, tenant_id, conversation_id, channel_connection_id, direction, message_type, body, status,
		     idempotency_key, request_hash, sent_by_user_id)
		  SELECT $1, t.tenant_id, t.id, t.channel_connection_id, 'outbound', 'text', $5, 'queued', $6, $7, $8
		  FROM target t
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
		msg.ID, tenantID, in.ConversationID, in.ConnectionID, body, key, hash, sender, uuid.New(), uuid.New(), requireAssignee).
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
	if errors.Is(err, pgx.ErrNoRows) {
		// Neither inserted nor a replay: the conversation's assignee/channel changed after the checks.
		return nil, false, ports.ErrConversationChanged
	}
	if err != nil {
		return nil, false, fmt.Errorf("messages: load idempotent message: %w", err)
	}
	return existing, true, nil
}
