package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// ADR-0050: the persistence the create-or-reuse-conversation-by-phone command needs. Deliberately local to the external module rather
// than reused from internal/contacts or internal/inbox (both tiny, stable statements — a phone upsert and a conversation insert — not
// worth a new cross-module port for), so this capability never has to know about those modules' own evolving contracts.

var (
	// ErrNoChannel: no active, text-capable WhatsApp line matches (the installation's allow-list, or the whole tenant, has none).
	ErrNoChannel = errors.New("external: no active text-capable channel connection matches")
	// ErrChannelAmbiguous: more than one active, text-capable line matches and the caller named none — this command never guesses which.
	ErrChannelAmbiguous = errors.New("external: more than one channel connection matches; name channel_id")
)

// PostgresStartStore holds no state: every call takes the caller's querier, same discipline as conversationcontrol.Service.
type PostgresStartStore struct{}

var _ ConversationStarter = PostgresStartStore{}

// FindOpenConversation is the "reuse" half (ADR-0050 §2.1): the newest OPEN conversation of the contact with this phone, narrowed to
// channelIDs when non-empty (the allow-list the caller already resolved — the installation's own, or just the one channel_id it named).
// found is false when there is none; this never creates anything and never locks (the caller locks it by id right after, the same way
// every other control decision in this package does).
func (PostgresStartStore) FindOpenConversation(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID, phoneE164 string, channelIDs []uuid.UUID) (conversationID, contactID uuid.UUID, found bool, err error) {
	narrow := ""
	args := []any{tenantID, phoneE164}
	if len(channelIDs) > 0 {
		narrow = " AND c.channel_connection_id = ANY($3::uuid[])"
		args = append(args, channelIDs)
	}
	err = q.QueryRow(ctx, `
		SELECT c.id, c.contact_id
		  FROM conversations c
		  JOIN contacts ct ON ct.tenant_id = c.tenant_id AND ct.id = c.contact_id
		 WHERE c.tenant_id = $1 AND ct.phone_e164 = $2 AND c.status = 'open'`+narrow+`
		 ORDER BY c.updated_at DESC, c.id DESC LIMIT 1`, args...).Scan(&conversationID, &contactID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, false, err
	}
	return conversationID, contactID, true, nil
}

// ResolveChannel picks the channel_connection_id a NEW conversation will use: requested when the caller named one (already proved inside
// the installation's channel allow-list by the handler) — this only re-checks that it is an active, text-capable line of THIS tenant;
// otherwise the one active, text-capable connection the installation may use (ErrNoChannel for zero, ErrChannelAmbiguous for more than
// one — this command never guesses).
func (PostgresStartStore) ResolveChannel(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID, requested *uuid.UUID, allow []uuid.UUID) (uuid.UUID, error) {
	if requested != nil {
		var id uuid.UUID
		err := q.QueryRow(ctx, `
			SELECT id FROM channel_connections WHERE tenant_id = $1 AND id = $2 AND status = 'active' AND capabilities ? 'text'`,
			tenantID, *requested).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNoChannel
		}
		return id, err
	}
	narrow, args := "", []any{tenantID}
	if len(allow) > 0 {
		narrow = " AND id = ANY($2::uuid[])"
		args = append(args, allow)
	}
	rows, err := q.Query(ctx, `SELECT id FROM channel_connections WHERE tenant_id = $1 AND status = 'active' AND capabilities ? 'text'`+narrow+` ORDER BY id LIMIT 2`, args...)
	if err != nil {
		return uuid.Nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return uuid.Nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, err
	}
	switch len(ids) {
	case 0:
		return uuid.Nil, ErrNoChannel
	case 1:
		return ids[0], nil
	default:
		return uuid.Nil, ErrChannelAmbiguous
	}
}

// CreateConversation is the "create" half (ADR-0050 §2.1): finds-or-creates the contact by phone, then inserts a brand-new OPEN
// conversation on connectionID, control_kind = 'crm_application', control_holder_id = installation, control_version = 1 — directly, in
// one statement, inside the capability conversationcontrol.Service.CreateHeldByApplication grants around this call. conversation_kind is
// derived the SAME way the inbound path derives it (contact_kind_to_conversation_kind, unclassified under an open identity conflict) —
// this command never invents its own classification rule.
func (PostgresStartStore) CreateConversation(ctx context.Context, q platformdb.Querier, tenantID, conversationID, installation, connectionID uuid.UUID, phoneE164 string, queueID *uuid.UUID) (contactID uuid.UUID, err error) {
	err = q.QueryRow(ctx, `
		WITH contact AS (
		  INSERT INTO contacts (tenant_id, display_name, phone_e164, status)
		  VALUES ($1, $2, $2, 'active')
		  ON CONFLICT (tenant_id, phone_e164) DO UPDATE SET updated_at = now()
		  RETURNING id, kind
		), derived AS (
		  SELECT contact.id AS contact_id,
		         CASE WHEN EXISTS (SELECT 1 FROM identity_resolution_conflicts x WHERE x.tenant_id = $1 AND x.contact_id = contact.id AND x.status = 'open')
		              THEN 'unclassified' ELSE contact_kind_to_conversation_kind(contact.kind) END AS kind
		    FROM contact
		)
		INSERT INTO conversations (id, tenant_id, contact_id, conversation_kind, has_unclassified_participants, channel_connection_id, status, queue_id,
		                           control_kind, control_holder_id, control_version, control_since, control_origin, created_at, updated_at)
		SELECT $3, $1, derived.contact_id, derived.kind, (derived.kind = 'unclassified'), $4, 'open', $5, 'crm_application', $6, 1, now(), 'crm', now(), now()
		  FROM derived
		RETURNING contact_id`,
		tenantID, phoneE164, conversationID, connectionID, queueID, installation).Scan(&contactID)
	return contactID, err
}
