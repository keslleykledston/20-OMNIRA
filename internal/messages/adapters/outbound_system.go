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

var _ ports.SystemOutboundStore = (*PostgresOutboundStore)(nil)

// InsertQueuedSystem is InsertQueued for a non-human sender: sent_by_user_id is NULL and idempotency is enforced by the
// partial unique index messages_system_idempotency_uq (migration 000084), because the operator index keys on the sender and
// NULLs never collide. Instead of "must be assigned to the sender" it requires the opposite: nobody is assigned.
func (s *PostgresOutboundStore) InsertQueuedSystem(ctx context.Context, in ports.SendContext, body, key, hash string) (*ports.QueuedMessage, bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, false, err
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
		    AND c.channel_connection_id IS NOT DISTINCT FROM $4::uuid
		    AND c.assigned_to_user_id IS NULL      -- the bot never talks over an operator
		  FOR SHARE OF c
		), ins AS (
		  INSERT INTO messages
		    (id, tenant_id, conversation_id, channel_connection_id, direction, message_type, body, status,
		     idempotency_key, request_hash, sent_by_user_id)
		  SELECT $1, t.tenant_id, t.id, t.channel_connection_id, 'outbound', 'text', $5, 'queued', $6, $7, NULL
		  FROM target t
		  ON CONFLICT (tenant_id, idempotency_key) WHERE sent_by_user_id IS NULL AND idempotency_key IS NOT NULL DO NOTHING
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
		msg.ID, tenantID, in.ConversationID, in.ConnectionID, body, key, hash, uuid.New(), uuid.New()).
		Scan(&msg.ID, &msg.CreatedAt)
	if err == nil {
		return msg, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("messages: queue system outbound: %w", err)
	}
	existing := &ports.QueuedMessage{}
	err = q.QueryRow(ctx, `
		SELECT id, conversation_id, body, status, created_at, COALESCE(request_hash, '')
		FROM messages WHERE tenant_id = $1 AND sent_by_user_id IS NULL AND idempotency_key = $2`, tenantID, key).
		Scan(&existing.ID, &existing.ConversationID, &existing.Body, &existing.Status, &existing.CreatedAt, &existing.RequestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ports.ErrConversationChanged
	}
	if err != nil {
		return nil, false, fmt.Errorf("messages: load idempotent system message: %w", err)
	}
	return existing, true, nil
}

var _ ports.SystemInteractiveStore = (*PostgresOutboundStore)(nil)

// InsertQueuedSystemInteractive is InsertQueuedSystem plus the interactive record, in the same transaction.
func (s *PostgresOutboundStore) InsertQueuedSystemInteractive(ctx context.Context, in ports.SendContext, text, key, hash string, itx ports.InteractiveSend) (*ports.QueuedMessage, bool, error) {
	msg, replayed, err := s.InsertQueuedSystem(ctx, in, text, key, hash)
	if err != nil || replayed {
		return msg, replayed, err
	}
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, false, err
	}
	type opt struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	opts := make([]opt, len(itx.Options))
	for i, o := range itx.Options {
		opts[i] = opt{ID: o.ID, Title: o.Title}
	}
	raw, err := json.Marshal(opts)
	if err != nil {
		return nil, false, fmt.Errorf("messages: encode interactive options: %w", err)
	}
	if _, err := platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx,
		`INSERT INTO message_interactive_sends (tenant_id, message_id, body, list_label, options) VALUES ($1,$2,$3,$4,$5)`,
		tenantID, msg.ID, itx.Body, itx.ListLabel, raw); err != nil {
		return nil, false, fmt.Errorf("messages: record interactive send: %w", err)
	}
	return msg, false, nil
}
