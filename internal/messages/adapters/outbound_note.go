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

var _ ports.ExternalNoteStore = (*PostgresOutboundStore)(nil)

// InsertNote (ADR-0053) is the ONLY statement that can create a private note, and it is deliberately its own file: there is no "job" CTE here,
// unlike insertQueuedExternal in outbound_external.go. That is the actual barrier against a note ever reaching WAHA or the Meta Cloud API — not
// a flag some other code path has to remember to check, but the simple fact that no outbox_events row is ever written for this call. Anyone
// touching this file should keep it that way: adding a delivery job here would defeat the whole point of ADR-0053.
func (s *PostgresOutboundStore) InsertNote(ctx context.Context, installation, conversationID uuid.UUID, controlVersion int64, body string) (*ports.QueuedMessage, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	msg := &ports.QueuedMessage{ID: uuid.New(), ConversationID: conversationID, Body: body, Status: "noted"}
	err = q.QueryRow(ctx, `
		WITH target AS (
		  SELECT c.id, c.tenant_id, c.channel_connection_id
		    FROM conversations c
		   WHERE c.tenant_id = $2 AND c.id = $3
		     AND c.status = 'open'
		     AND c.control_kind = 'crm_application' AND c.control_holder_id = $5 AND c.control_version = $6   -- held by this installation, at this version
		   FOR SHARE OF c
		), ins AS (
		  INSERT INTO messages
		    (id, tenant_id, conversation_id, channel_connection_id, direction, message_type, body, status, private, sent_by_user_id, sent_by_installation_id)
		  SELECT $1, t.tenant_id, t.id, t.channel_connection_id, 'outbound', 'text', $4, 'noted', true, NULL, $5
		  FROM target t
		  RETURNING id, created_at
		), touched AS (
		  UPDATE conversations SET updated_at = now()
		  WHERE tenant_id = $2 AND id = $3 AND EXISTS (SELECT 1 FROM ins)
		  RETURNING id
		)
		SELECT id, created_at FROM ins`,
		msg.ID, tenantID, conversationID, body, installation, controlVersion).
		Scan(&msg.ID, &msg.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrConversationChanged
	}
	if err != nil {
		return nil, fmt.Errorf("messages: insert note: %w", err)
	}
	return msg, nil
}
