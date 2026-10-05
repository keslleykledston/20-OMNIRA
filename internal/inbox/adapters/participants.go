package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	"github.com/omnira/omnira/internal/inbox/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// PostgresParticipantRecorder implements application.ParticipantRecorder inside the webhook's tenant session.
type PostgresParticipantRecorder struct{ pool *pgxpool.Pool }

var _ application.ParticipantRecorder = (*PostgresParticipantRecorder)(nil)

func NewPostgresParticipantRecorder(pool *pgxpool.Pool) *PostgresParticipantRecorder {
	return &PostgresParticipantRecorder{pool: pool}
}

// RecordInbound is idempotent. It (1) upserts the external participant bound to the contact, (2) links it to the
// conversation as the customer, (3) stamps the message with its sender and reply metadata and (4) resolves the reply
// to a message of the SAME conversation when one matches. A reply that points at nothing we stored keeps the
// provider's id so it can still be shown or resolved later.
func (r *PostgresParticipantRecorder) RecordInbound(ctx context.Context, in application.ParticipantInput) error {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return errors.New("participants: tenant context required")
	}
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var participant *uuid.UUID
	if in.ExternalID != "" {
		contact := in.ContactID
		var contactPtr *uuid.UUID
		if contact != uuid.Nil {
			contactPtr = &contact
		}
		id, err := channeladapters.UpsertChannelParticipant(ctx, q, channeladapters.ParticipantRef{
			TenantID: tc.TenantID, ConnectionID: in.ConnectionID, Provider: in.Provider, ExternalID: in.ExternalID,
			DisplayName: in.DisplayName, ContactID: contactPtr,
		})
		if err != nil {
			return err
		}
		role := in.Role
		if role == "" {
			role = "customer"
		}
		if err := channeladapters.LinkConversationParticipant(ctx, q, tc.TenantID, in.ConversationID, id, role); err != nil {
			return err
		}
		participant = &id
	}
	_, err = q.Exec(ctx, `
		UPDATE messages SET
		  sender_channel_participant_id = COALESCE($3, sender_channel_participant_id),
		  reply_to_external_message_id = $4,
		  reply_to_message_id = CASE WHEN $4 = '' THEN NULL ELSE (
		    SELECT m2.id FROM messages m2
		    WHERE m2.tenant_id = $1 AND m2.conversation_id = $5 AND m2.id <> $2
		      AND (m2.provider_message_id = $4 OR split_part(m2.provider_message_id, '_', 3) = $4)
		    ORDER BY m2.created_at DESC LIMIT 1) END
		WHERE tenant_id = $1 AND id = $2`,
		tc.TenantID, in.MessageID, participant, in.ReplyToExternalID, in.ConversationID)
	return err
}
