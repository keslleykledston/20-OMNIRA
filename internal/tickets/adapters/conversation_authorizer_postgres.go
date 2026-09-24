package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// ConversationAuthorizer implements ports.ConversationAuthorizer by reading
// conversations.assigned_to_user_id directly — the same column
// internal/messages/adapters.PostgresOutboundStore.LoadSendContext reads
// for the identical "assignee or conversation.manage" authorization
// semantics (internal/messages/application.Sender.Send). No new
// authorization concept is introduced.
type ConversationAuthorizer struct{ pool *pgxpool.Pool }

func NewConversationAuthorizer(pool *pgxpool.Pool) *ConversationAuthorizer {
	return &ConversationAuthorizer{pool: pool}
}

func (a *ConversationAuthorizer) LoadAssignment(ctx context.Context, conversationID uuid.UUID) (*uuid.UUID, bool, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, false, err
	}
	var assignedTo *uuid.UUID
	err = platformdb.QuerierFromContext(ctx, a.pool).QueryRow(ctx, `
		SELECT assigned_to_user_id FROM conversations WHERE tenant_id = $1 AND id = $2`,
		tenantID, conversationID).Scan(&assignedTo)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("tickets: load conversation assignment: %w", err)
	}
	return assignedTo, true, nil
}
