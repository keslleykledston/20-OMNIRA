package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnira/omnira/internal/messages/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

var _ ports.AttachmentStore = (*PostgresOutboundStore)(nil)

func (s *PostgresOutboundStore) InsertAttachment(ctx context.Context, a ports.OutboundAttachment) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx, `
		INSERT INTO message_outbound_media (id, tenant_id, conversation_id, uploaded_by, kind, mime, size_bytes, sha256, file_name, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		a.ID, tenantID, a.ConversationID, a.UploadedBy, a.Kind, a.Mime, a.SizeBytes, a.SHA256, a.FileName, a.ExpiresAt)
	if err != nil {
		return fmt.Errorf("messages: record attachment: %w", err)
	}
	return nil
}

func (s *PostgresOutboundStore) CountPendingAttachments(ctx context.Context, conversationID, actor uuid.UUID) (int, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return 0, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	// Serialise this operator's uploads in this conversation until the request's transaction ends, so two concurrent uploads cannot both
	// see "4 pending" and both insert the 5th and 6th.
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('outbound_media:' || $1::text || ':' || $2::text, 0))`, conversationID.String(), actor.String()); err != nil {
		return 0, fmt.Errorf("messages: lock pending attachments: %w", err)
	}
	var n int
	err = q.QueryRow(ctx, `
		SELECT count(*) FROM message_outbound_media
		WHERE tenant_id=$1 AND conversation_id=$2 AND uploaded_by=$3 AND message_id IS NULL AND file_purged_at IS NULL AND expires_at > now()`,
		tenantID, conversationID, actor).Scan(&n)
	return n, err
}

func (s *PostgresOutboundStore) LoadAttachment(ctx context.Context, id uuid.UUID) (*ports.OutboundAttachment, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	a := &ports.OutboundAttachment{}
	var purged bool
	err = platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		SELECT id, conversation_id, uploaded_by, kind, mime, size_bytes, sha256, file_name, message_id, expires_at, file_purged_at IS NOT NULL
		FROM message_outbound_media WHERE tenant_id=$1 AND id=$2`, tenantID, id).
		Scan(&a.ID, &a.ConversationID, &a.UploadedBy, &a.Kind, &a.Mime, &a.SizeBytes, &a.SHA256, &a.FileName, &a.MessageID, &a.ExpiresAt, &purged)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("messages: load attachment: %w", err)
	}
	a.Purged = purged
	return a, nil
}

func (s *PostgresOutboundStore) ExpireAttachment(ctx context.Context, id, actor uuid.UUID) (bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	tag, err := platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx, `
		UPDATE message_outbound_media SET expires_at = now()
		WHERE tenant_id=$1 AND id=$2 AND uploaded_by=$3 AND message_id IS NULL AND file_purged_at IS NULL`, tenantID, id, actor)
	if err != nil {
		return false, fmt.Errorf("messages: expire attachment: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// InsertQueuedMedia: ONE statement - the upload is linked to the new message only if it is still the actor's unsent, unexpired,
// unpurged upload in this conversation, and the message (with its delivery job) is created only if that link happened. A concurrent
// second send of the same upload therefore loses (zero rows) instead of producing two messages with one file.
func (s *PostgresOutboundStore) InsertQueuedMedia(ctx context.Context, sender uuid.UUID, in ports.SendContext, att ports.OutboundAttachment, caption, key, hash string, requireAssignee bool) (*ports.QueuedMessage, bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, false, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	msg := &ports.QueuedMessage{ID: uuid.New(), ConversationID: in.ConversationID, Body: caption, Status: "queued", RequestHash: hash}
	err = q.QueryRow(ctx, `
		WITH target AS (
		  SELECT c.id, c.tenant_id, c.channel_connection_id
		  FROM conversations c
		  JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id AND cc.status = 'active'
		  WHERE c.tenant_id = $2 AND c.id = $3
		    AND c.channel_connection_id IS NOT DISTINCT FROM $4::uuid
		    AND (NOT $11::bool OR c.assigned_to_user_id = $8)
		  FOR SHARE OF c
		), claim AS (
		  SELECT id FROM message_outbound_media
		  WHERE tenant_id = $2 AND id = $12 AND conversation_id = $3 AND uploaded_by = $8
		    AND message_id IS NULL AND file_purged_at IS NULL AND expires_at > now()
		    AND EXISTS (SELECT 1 FROM target)
		  FOR UPDATE
		), ins AS (
		  INSERT INTO messages
		    (id, tenant_id, conversation_id, channel_connection_id, direction, message_type, body, mime_type, size_bytes, status,
		     idempotency_key, request_hash, sent_by_user_id)
		  SELECT $1, t.tenant_id, t.id, t.channel_connection_id, 'outbound', $13, $5, $14, $15, 'queued', $6, $7, $8
		  FROM target t WHERE EXISTS (SELECT 1 FROM claim)
		  ON CONFLICT (tenant_id, sent_by_user_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		  RETURNING id, created_at
		), link AS (
		  UPDATE message_outbound_media SET message_id = (SELECT id FROM ins)
		  WHERE tenant_id = $2 AND id = $12 AND EXISTS (SELECT 1 FROM ins)
		  RETURNING id
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
		msg.ID, tenantID, in.ConversationID, in.ConnectionID, caption, key, hash, sender, uuid.New(), uuid.New(), requireAssignee,
		att.ID, att.Kind, att.Mime, att.SizeBytes).
		Scan(&msg.ID, &msg.CreatedAt)
	if err == nil {
		return msg, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("messages: queue outbound media: %w", err)
	}
	existing := &ports.QueuedMessage{}
	err = q.QueryRow(ctx, `
		SELECT id, conversation_id, body, status, created_at, COALESCE(request_hash, '')
		FROM messages WHERE tenant_id = $1 AND sent_by_user_id = $2 AND idempotency_key = $3`,
		tenantID, sender, key).
		Scan(&existing.ID, &existing.ConversationID, &existing.Body, &existing.Status, &existing.CreatedAt, &existing.RequestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ports.ErrConversationChanged
	}
	if err != nil {
		return nil, false, fmt.Errorf("messages: load idempotent media message: %w", err)
	}
	return existing, true, nil
}
