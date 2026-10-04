package adapters

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

type PostgresHandoffRepository struct{ pool *pgxpool.Pool }

var _ ports.HandoffRepository = (*PostgresHandoffRepository)(nil)

func NewPostgresHandoffRepository(pool *pgxpool.Pool) *PostgresHandoffRepository {
	return &PostgresHandoffRepository{pool: pool}
}

func (r *PostgresHandoffRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

const handoffColumns = `id, tenant_id, topic_thread_id, source_group_id, status, expires_at, created_by_user_id, created_at, redeemed_at, redeemed_conversation_id, revoked_at`

func scanHandoff(row pgx.Row) (*domain.TopicHandoff, error) {
	var h domain.TopicHandoff
	var status string
	if err := row.Scan(&h.ID, &h.TenantID, &h.TopicThreadID, &h.SourceGroupID, &status, &h.ExpiresAt, &h.CreatedByUserID, &h.CreatedAt, &h.RedeemedAt, &h.RedeemedConversationID, &h.RevokedAt); err != nil {
		return nil, err
	}
	h.Status = domain.HandoffStatus(status)
	return &h, nil
}

func (r *PostgresHandoffRepository) Create(ctx context.Context, h *domain.TopicHandoff, tokenHash string, now time.Time) error {
	var status string
	// the topic row is locked so concurrent creations cannot exceed the pending cap
	err := r.q(ctx).QueryRow(ctx, `SELECT status FROM topic_threads WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, h.TenantID, h.TopicThreadID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrTopicNotFound
	}
	if err != nil {
		return err
	}
	if status != "open" {
		return domain.ErrInvalidTransition
	}
	var n int
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(*) FROM topic_handoffs WHERE tenant_id=$1 AND topic_thread_id=$2 AND status='pending' AND expires_at > $3`, h.TenantID, h.TopicThreadID, now).Scan(&n); err != nil {
		return err
	}
	if n >= domain.MaxPendingHandoffs {
		return domain.ErrTooManyHandoffs
	}
	created, err := scanHandoff(r.q(ctx).QueryRow(ctx, `
		INSERT INTO topic_handoffs (tenant_id, topic_thread_id, source_group_id, token_hash, expires_at, created_by_user_id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING `+handoffColumns, h.TenantID, h.TopicThreadID, h.SourceGroupID, tokenHash, h.ExpiresAt, h.CreatedByUserID, now))
	if err != nil {
		return mapError(err)
	}
	*h = *created
	return nil
}

func (r *PostgresHandoffRepository) List(ctx context.Context, tenantID, topicID uuid.UUID) ([]domain.TopicHandoff, error) {
	rows, err := r.q(ctx).Query(ctx, `SELECT `+handoffColumns+` FROM topic_handoffs WHERE tenant_id=$1 AND topic_thread_id=$2 ORDER BY created_at DESC LIMIT 50`, tenantID, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TopicHandoff{}
	for rows.Next() {
		h, err := scanHandoff(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

func (r *PostgresHandoffRepository) Revoke(ctx context.Context, tenantID, topicID, handoffID uuid.UUID, now time.Time) (bool, error) {
	tag, err := r.q(ctx).Exec(ctx, `UPDATE topic_handoffs SET status='revoked', revoked_at=$4 WHERE tenant_id=$1 AND topic_thread_id=$2 AND id=$3 AND status='pending'`, tenantID, topicID, handoffID, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *PostgresHandoffRepository) Redeem(ctx context.Context, tenantID uuid.UUID, tokenHash string, messageID, conversationID uuid.UUID, now time.Time) (*domain.TopicHandoff, error) {
	h, err := scanHandoff(r.q(ctx).QueryRow(ctx, `
		UPDATE topic_handoffs h SET status='redeemed', redeemed_at=$3, redeemed_message_id=$4, redeemed_conversation_id=$5
		WHERE h.tenant_id=$1 AND h.token_hash=$2 AND h.status='pending' AND h.expires_at > $3
		  AND EXISTS (SELECT 1 FROM topic_threads t WHERE t.tenant_id=h.tenant_id AND t.id=h.topic_thread_id AND t.status='open')
		RETURNING h.id, h.tenant_id, h.topic_thread_id, h.source_group_id, h.status, h.expires_at, h.created_by_user_id, h.created_at, h.redeemed_at, h.redeemed_conversation_id, h.revoked_at`,
		tenantID, tokenHash, now, messageID, conversationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

func (r *PostgresHandoffRepository) SourceGroup(ctx context.Context, tenantID, topicID uuid.UUID) (*uuid.UUID, error) {
	rows, err := r.q(ctx).Query(ctx, `SELECT group_id FROM topic_group_links WHERE tenant_id=$1 AND topic_thread_id=$2 LIMIT 2`, tenantID, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) == 1 {
		return &ids[0], rows.Err()
	}
	return nil, rows.Err()
}
