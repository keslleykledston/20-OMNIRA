package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type PostgresOutboundStore struct{ pool *pgxpool.Pool }

var _ OutboundStore = (*PostgresOutboundStore)(nil)

func NewPostgresOutboundStore(pool *pgxpool.Pool) *PostgresOutboundStore {
	return &PostgresOutboundStore{pool: pool}
}

func (s *PostgresOutboundStore) RunForMessage(ctx context.Context, messageID uuid.UUID, fn func(context.Context) error) error {
	if s == nil || s.pool == nil || messageID == uuid.Nil || fn == nil {
		return fmt.Errorf("%w: invalid message runner input", ErrPermanent)
	}
	var tenantID uuid.UUID
	err := platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(system context.Context) error {
		return platformdb.QuerierFromContext(system, s.pool).QueryRow(system,
			`SELECT tenant_id FROM messages WHERE id=$1 AND direction='outbound'`, messageID).Scan(&tenantID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: unknown message", ErrPermanent)
	}
	if err != nil {
		return err
	}
	return platformdb.WithSystemTenantSession(ctx, s.pool, tenantID, fn)
}

func tenantOf(ctx context.Context) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("channel delivery: tenant context required")
	}
	return tc.TenantID, nil
}

func (s *PostgresOutboundStore) LockOutbound(ctx context.Context, messageID uuid.UUID) (*OutboundJob, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	job := &OutboundJob{MessageID: messageID}
	var connection *uuid.UUID
	var active *bool
	var phone *string
	err = platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		SELECT m.channel_connection_id, ct.phone_e164, m.body, m.status, m.provider_message_id,
		       (cc.status = 'active')
		FROM messages m
		JOIN conversations c ON c.tenant_id = m.tenant_id AND c.id = m.conversation_id
		JOIN contacts ct ON ct.tenant_id = c.tenant_id AND ct.id = c.contact_id
		LEFT JOIN channel_connections cc ON cc.tenant_id = m.tenant_id AND cc.id = m.channel_connection_id
		WHERE m.tenant_id = $1 AND m.id = $2 AND m.direction = 'outbound'
		FOR UPDATE OF m`, tenantID, messageID).
		Scan(&connection, &phone, &job.Text, &job.Status, &job.ProviderMessageID, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("channel delivery: lock message: %w", err)
	}
	if connection != nil {
		job.ConnectionID = *connection
	}
	if phone != nil {
		job.ToE164 = *phone
	}
	job.ConnectionActive = active != nil && *active
	return job, nil
}

func (s *PostgresOutboundStore) MarkSent(ctx context.Context, messageID uuid.UUID, providerMessageID string) error {
	return s.finish(ctx, messageID, `UPDATE messages SET status='sent', provider_message_id=$3, updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status='queued'`, providerMessageID)
}

func (s *PostgresOutboundStore) MarkFailed(ctx context.Context, messageID uuid.UUID, reason string) error {
	return s.finish(ctx, messageID, `UPDATE messages SET status='failed', failure_reason=$3, updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status='queued'`, reason)
}

func (s *PostgresOutboundStore) finish(ctx context.Context, messageID uuid.UUID, sql, value string) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	tag, err := platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx, sql, tenantID, messageID, value)
	if err != nil {
		return fmt.Errorf("channel delivery: update message: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("channel delivery: message was not queued")
	}
	return nil
}
