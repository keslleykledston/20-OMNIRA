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

// conversationContactReader is the seam PRODUCT.7B2B's post-success
// evidence hook uses to derive the canonical contact_id for a conversation
// — server-side, under TenantContext/RLS, never from a browser-supplied
// contact_id. Deliberately its own narrow type: PRODUCT.7B1B's
// activityConversationReader answers a different, richer question
// (assignment + crm_contact_id) for a different feature (CreateActivity)
// — reusing it here merely because its SQL happens to be similar would
// couple two unrelated concerns to one interface for no real benefit.
type conversationContactReader interface {
	ContactIDFor(ctx context.Context, conversationID uuid.UUID) (uuid.UUID, bool, error)
}

// PostgresConversationContacts implements conversationContactReader by
// reading conversations.contact_id directly, tenant-scoped — the same
// conversations row internal/tickets/adapters.ConversationAuthorizer and
// PostgresActivityConversations already read for their own narrow needs.
type PostgresConversationContacts struct{ pool *pgxpool.Pool }

func NewPostgresConversationContacts(pool *pgxpool.Pool) *PostgresConversationContacts {
	return &PostgresConversationContacts{pool: pool}
}

func (r *PostgresConversationContacts) ContactIDFor(ctx context.Context, conversationID uuid.UUID) (uuid.UUID, bool, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, false, errors.New("inbox: tenant context required")
	}
	var contactID uuid.UUID
	err = platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		SELECT contact_id FROM conversations WHERE tenant_id = $1 AND id = $2`,
		tc.TenantID, conversationID).Scan(&contactID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return contactID, true, nil
}

var _ conversationContactReader = (*PostgresConversationContacts)(nil)
