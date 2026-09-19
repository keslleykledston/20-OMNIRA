package routing

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

type PostgresConversationRunner struct{ pool *pgxpool.Pool }

func NewPostgresConversationRunner(pool *pgxpool.Pool) *PostgresConversationRunner {
	return &PostgresConversationRunner{pool: pool}
}

func (r *PostgresConversationRunner) RunForConversation(ctx context.Context, conversationID uuid.UUID, fn func(context.Context) error) error {
	if r == nil || r.pool == nil || conversationID == uuid.Nil || fn == nil {
		return fmt.Errorf("%w: invalid conversation runner input", ErrPermanent)
	}
	var tenantID uuid.UUID
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(system context.Context) error {
		return platformdb.QuerierFromContext(system, r.pool).QueryRow(system,
			`SELECT tenant_id FROM conversations WHERE id=$1`, conversationID).Scan(&tenantID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: unknown conversation", ErrPermanent)
	}
	if err != nil {
		return err
	}
	return platformdb.WithSystemTenantSession(ctx, r.pool, tenantID, fn)
}
