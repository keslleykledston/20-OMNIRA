package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnira/omnira/internal/messages/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

var _ ports.ExternalOutboundStore = (*PostgresOutboundStore)(nil)

// InsertQueuedExternal is InsertQueued for an external application (ADR-0048): sent_by_installation_id names the installation, sent_by_user_id is NULL
// and the idempotency key is NULL too (the command receipt, written by the caller in this same transaction, makes a retry return this message). Instead of
// "assigned to the sender" the statement requires that THIS installation holds the conversation at exactly the control version the caller proved, so a
// transfer, a takeover, a close or a release that got there first leaves the message unwritten.
func (s *PostgresOutboundStore) InsertQueuedExternal(ctx context.Context, installation uuid.UUID, controlVersion int64, in ports.SendContext, body, hash string) (*ports.QueuedMessage, error) {
	return s.insertQueuedExternal(ctx, installation, controlVersion, in, body, hash, in.Provider == "meta_cloud")
}

// insertQueuedExternal: enforceWindow is true for free text on a provider with the 24 h customer-service window (a template is allowed outside it).
func (s *PostgresOutboundStore) insertQueuedExternal(ctx context.Context, installation uuid.UUID, controlVersion int64, in ports.SendContext, body, hash string, enforceWindow bool) (*ports.QueuedMessage, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	msg := &ports.QueuedMessage{ID: uuid.New(), ConversationID: in.ConversationID, Body: body, Status: "queued", RequestHash: hash}
	err = q.QueryRow(ctx, `
		WITH target AS (
		  SELECT c.id, c.tenant_id, c.channel_connection_id
		  FROM conversations c
		  JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id
		       AND cc.status = 'active' AND cc.capabilities ? 'text'
		  WHERE c.tenant_id = $2 AND c.id = $3
		    AND c.status = 'open'
		    AND c.channel_connection_id IS NOT DISTINCT FROM $4::uuid
		    AND c.control_kind = 'crm_application' AND c.control_holder_id = $7 AND c.control_version = $10   -- held by this installation, at this version
		    AND (NOT $11::bool OR EXISTS (SELECT 1 FROM messages w WHERE w.tenant_id = c.tenant_id AND w.conversation_id = c.id AND w.direction = 'inbound'
		                                   AND w.created_at > now() - interval '24 hours'))     -- the provider's 24 h customer-service window, proved here for a free text
		  FOR SHARE OF c
		), ins AS (
		  INSERT INTO messages
		    (id, tenant_id, conversation_id, channel_connection_id, direction, message_type, body, status,
		     request_hash, sent_by_user_id, sent_by_installation_id)
		  SELECT $1, t.tenant_id, t.id, t.channel_connection_id, 'outbound', 'text', $5, 'queued', $6, NULL, $7
		  FROM target t
		  RETURNING id, created_at
		), job AS (
		  INSERT INTO outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, payload)
		  SELECT $8, $2, 'job.channel.send_text.v1', 'message', id::text, $9, '{}'::jsonb FROM ins
		  RETURNING id
		), touched AS (
		  UPDATE conversations SET updated_at = now()
		  WHERE tenant_id = $2 AND id = $3 AND EXISTS (SELECT 1 FROM ins)
		  RETURNING id
		)
		SELECT id, created_at FROM ins`,
		msg.ID, tenantID, in.ConversationID, in.ConnectionID, body, hash, installation, uuid.New(), uuid.New(), controlVersion, enforceWindow).
		Scan(&msg.ID, &msg.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrConversationChanged
	}
	if err != nil {
		return nil, fmt.Errorf("messages: queue external outbound: %w", err)
	}
	return msg, nil
}

func (s *PostgresOutboundStore) InsertQueuedExternalTemplate(ctx context.Context, installation uuid.UUID, controlVersion int64, in ports.SendContext, body, hash string, tpl ports.TemplateSend) (*ports.QueuedMessage, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	// the template must STILL be approved and sendable for this line, proved now and kept (FOR SHARE) until the message is committed
	var one int
	err = platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		SELECT 1 FROM channel_message_templates
		 WHERE tenant_id = $1 AND id = $2 AND connection_id = $3 AND status = 'APPROVED' AND sendable
		 FOR SHARE`, tenantID, tpl.ID, tpl.ConnectionID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrTemplateGone
	}
	if err != nil {
		return nil, fmt.Errorf("messages: re-check template: %w", err)
	}
	msg, err := s.insertQueuedExternal(ctx, installation, controlVersion, in, body, hash, false)
	if err != nil {
		return nil, err
	}
	params, err := json.Marshal(tpl.Params)
	if err != nil {
		return nil, fmt.Errorf("messages: encode template params: %w", err)
	}
	if _, err := platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx,
		`INSERT INTO message_template_sends (tenant_id, message_id, template_name, language, params) VALUES ($1,$2,$3,$4,$5)`,
		tenantID, msg.ID, tpl.Name, tpl.Language, params); err != nil {
		return nil, fmt.Errorf("messages: record template send: %w", err)
	}
	return msg, nil
}
