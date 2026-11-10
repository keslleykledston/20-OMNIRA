package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	groupsapp "github.com/omnira/omnira/internal/groups/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresAlertChannelRepository resolves a conversation's real tenant from the database (never trusting a
// caller-supplied tenant id) and, for an active tenant, its single alert-channel group (ADR-0058) if any.
type PostgresAlertChannelRepository struct{ pool *pgxpool.Pool }

var _ groupsapp.AlertChannelRepository = (*PostgresAlertChannelRepository)(nil)

func NewPostgresAlertChannelRepository(pool *pgxpool.Pool) *PostgresAlertChannelRepository {
	return &PostgresAlertChannelRepository{pool: pool}
}

func (r *PostgresAlertChannelRepository) AlertChannelForConversation(ctx context.Context, conversationID uuid.UUID) (*groupsapp.AlertChannel, error) {
	var tenantID uuid.UUID
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		return platformdb.QuerierFromContext(sctx, r.pool).QueryRow(sctx,
			`SELECT tenant_id FROM conversations WHERE id = $1`, conversationID).Scan(&tenantID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out *groupsapp.AlertChannel
	err = platformdb.WithSystemTenantSession(ctx, r.pool, tenantID, func(sctx context.Context) error {
		q := platformdb.QuerierFromContext(sctx, r.pool)
		active, err := platformdb.LockTenantActive(sctx, q, tenantID)
		if err != nil || !active {
			return err // ADR-0038: a suspended tenant's conversations never trigger anything, including this
		}
		var ch groupsapp.AlertChannel
		err = q.QueryRow(sctx, `
			SELECT g.id, g.channel_connection_id, g.provider_group_id
			FROM wa_groups g
			JOIN tenants t ON t.id = g.tenant_id AND t.status = 'active'
			WHERE g.tenant_id = $1 AND g.is_alert_channel AND g.enabled`, tenantID).
			Scan(&ch.GroupID, &ch.ConnectionID, &ch.ProviderGroupID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // no alert channel configured for this tenant
		}
		if err != nil {
			return err
		}
		out = &ch
		return nil
	})
	return out, err
}
