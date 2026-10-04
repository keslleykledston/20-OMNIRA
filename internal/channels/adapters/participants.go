package adapters

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// ParticipantRef identifies who wrote a message on a channel, in the provider's own terms (ADR-0017 Wave 2). It is NOT a
// conversation_participant (that is an OMNIRA agent). The id is provider- and connection-qualified, so the same id on
// another connection or tenant is another participant.
type ParticipantRef struct {
	TenantID     uuid.UUID
	ConnectionID uuid.UUID
	Provider     string
	ExternalID   string
	DisplayName  string
	ContactID    *uuid.UUID
}

// UpsertChannelParticipant is idempotent: a replayed webhook finds the same row. It never rebinds an existing Contact
// binding (the first one wins) and never blanks a known display name.
func UpsertChannelParticipant(ctx context.Context, q platformdb.Querier, ref ParticipantRef) (uuid.UUID, error) {
	if ref.TenantID == uuid.Nil || ref.ConnectionID == uuid.Nil {
		return uuid.Nil, errors.New("channel participant: tenant and connection are required")
	}
	ref.ExternalID = strings.TrimSpace(ref.ExternalID)
	if ref.ExternalID == "" || ref.Provider == "" {
		return uuid.Nil, errors.New("channel participant: provider and external id are required")
	}
	var id uuid.UUID
	err := q.QueryRow(ctx, `
		INSERT INTO channel_participants (tenant_id, channel_connection_id, provider, external_participant_id, display_name, contact_id)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id, channel_connection_id, provider, external_participant_id) DO UPDATE SET
		  display_name = CASE WHEN EXCLUDED.display_name <> '' THEN EXCLUDED.display_name ELSE channel_participants.display_name END,
		  contact_id   = COALESCE(channel_participants.contact_id, EXCLUDED.contact_id),
		  last_seen_at = now(), updated_at = now()
		RETURNING id`,
		ref.TenantID, ref.ConnectionID, ref.Provider, ref.ExternalID, truncateRunes(ref.DisplayName, 200), ref.ContactID).Scan(&id)
	return id, err
}

// LinkConversationParticipant records that a participant took part in a conversation (idempotent).
func LinkConversationParticipant(ctx context.Context, q platformdb.Querier, tenantID, conversationID, participantID uuid.UUID, role string) error {
	var r *string
	if role != "" {
		r = &role
	}
	_, err := q.Exec(ctx, `
		INSERT INTO conversation_channel_participants (tenant_id, conversation_id, channel_participant_id, role)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id, conversation_id, channel_participant_id) DO UPDATE SET last_seen_at = now()`,
		tenantID, conversationID, participantID, r)
	return err
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
