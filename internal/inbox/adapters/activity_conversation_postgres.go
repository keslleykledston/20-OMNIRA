package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// PostgresActivityConversations implements activityConversationReader by
// reading conversations.assigned_to_user_id and conversations.crm_contact_id
// directly, tenant-scoped — the same conversations row
// internal/tickets/adapters.ConversationAuthorizer.LoadAssignment and
// internal/messages/adapters.PostgresOutboundStore.LoadSendContext already
// read for the identical "assignee or conversation.manage" authorization
// semantics, extended with the one additional column CreateActivity needs
// (PRODUCT.7B1B): the conversation's own persisted CRM contact identity.
type PostgresActivityConversations struct{ pool *pgxpool.Pool }

func NewPostgresActivityConversations(pool *pgxpool.Pool) *PostgresActivityConversations {
	return &PostgresActivityConversations{pool: pool}
}

func (r *PostgresActivityConversations) LoadForActivity(ctx context.Context, conversationID uuid.UUID) (*activityConversation, bool, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, false, errors.New("inbox: tenant context required")
	}
	var conv activityConversation
	err = platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		SELECT assigned_to_user_id, crm_contact_id FROM conversations WHERE tenant_id = $1 AND id = $2`,
		tc.TenantID, conversationID).Scan(&conv.AssignedToUserID, &conv.CRMContactID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &conv, true, nil
}

var _ activityConversationReader = (*PostgresActivityConversations)(nil)
