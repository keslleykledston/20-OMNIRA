package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

type PostgresSummaryRepository struct{ pool *pgxpool.Pool }

var _ ports.SummaryRepository = (*PostgresSummaryRepository)(nil)

func NewPostgresSummaryRepository(pool *pgxpool.Pool) *PostgresSummaryRepository {
	return &PostgresSummaryRepository{pool: pool}
}

func (r *PostgresSummaryRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

const summaryColumns = `id, tenant_id, topic_thread_id, version, summary_text, structured_context, status, source_summary_id,
	model_provider, model_name, prompt_version, created_by_user_id, created_at, confirmed_at`

func scanSummary(row pgx.Row) (*domain.TopicSummary, error) {
	var s domain.TopicSummary
	var status string
	if err := row.Scan(&s.ID, &s.TenantID, &s.TopicThreadID, &s.Version, &s.SummaryText, &s.StructuredContext, &status, &s.SourceSummaryID,
		&s.ModelProvider, &s.ModelName, &s.PromptVersion, &s.CreatedByUserID, &s.CreatedAt, &s.ConfirmedAt); err != nil {
		return nil, err
	}
	s.Status = domain.SummaryStatus(status)
	return &s, nil
}

// Create assigns the next version inside the insert itself; two concurrent writers cannot take the same number (the
// UNIQUE constraint refuses the loser, which retries with the new maximum).
func (r *PostgresSummaryRepository) Create(ctx context.Context, s *domain.TopicSummary) (*domain.TopicSummary, error) {
	if !s.Status.Valid() {
		return nil, domain.ErrInvalidTopic
	}
	ctxJSON := s.StructuredContext
	if len(ctxJSON) == 0 {
		ctxJSON = []byte(`{}`)
	}
	for attempt := 0; attempt < 5; attempt++ {
		row := r.q(ctx).QueryRow(ctx, `
			INSERT INTO topic_summaries (tenant_id, topic_thread_id, version, summary_text, structured_context, status, source_summary_id,
			       model_provider, model_name, prompt_version, created_by_user_id, confirmed_at)
			SELECT $1, $2, COALESCE(max(version), 0) + 1, $3, $4::jsonb, $5, $6, $7, $8, $9, $10, $11
			FROM topic_summaries WHERE tenant_id = $1 AND topic_thread_id = $2
			RETURNING `+summaryColumns, s.TenantID, s.TopicThreadID, s.SummaryText, string(ctxJSON), string(s.Status), s.SourceSummaryID,
			s.ModelProvider, s.ModelName, s.PromptVersion, s.CreatedByUserID, s.ConfirmedAt)
		created, err := scanSummary(row)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			continue // lost the race for that version number; the sub-select sees the winner next time
		}
		return created, mapError(err)
	}
	return nil, errors.New("summary: could not assign a version")
}

func (r *PostgresSummaryRepository) list(ctx context.Context, where string, args ...any) ([]domain.TopicSummary, error) {
	rows, err := r.q(ctx).Query(ctx, `SELECT `+summaryColumns+` FROM topic_summaries WHERE `+where+` ORDER BY version DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TopicSummary{}
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *PostgresSummaryRepository) List(ctx context.Context, tenantID, topicID uuid.UUID) ([]domain.TopicSummary, error) {
	return r.list(ctx, `tenant_id=$1 AND topic_thread_id=$2`, tenantID, topicID)
}

func (r *PostgresSummaryRepository) one(ctx context.Context, extra string, tenantID, topicID uuid.UUID) (*domain.TopicSummary, error) {
	s, err := scanSummary(r.q(ctx).QueryRow(ctx, `SELECT `+summaryColumns+` FROM topic_summaries WHERE tenant_id=$1 AND topic_thread_id=$2 `+extra+` ORDER BY version DESC LIMIT 1`, tenantID, topicID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func (r *PostgresSummaryRepository) Latest(ctx context.Context, tenantID, topicID uuid.UUID) (*domain.TopicSummary, error) {
	return r.one(ctx, ``, tenantID, topicID)
}

func (r *PostgresSummaryRepository) LatestConfirmed(ctx context.Context, tenantID, topicID uuid.UUID) (*domain.TopicSummary, error) {
	return r.one(ctx, `AND status IN ('agent_confirmed','customer_confirmed','corrected')`, tenantID, topicID)
}

func (r *PostgresSummaryRepository) LatestInferred(ctx context.Context, tenantID, topicID uuid.UUID) (*domain.TopicSummary, error) {
	return r.one(ctx, `AND status = 'ai_inferred'`, tenantID, topicID)
}

func (r *PostgresSummaryRepository) Confirm(ctx context.Context, tenantID, summaryID uuid.UUID, status domain.SummaryStatus, by *uuid.UUID) (bool, error) {
	if status != domain.SummaryAgentConfirmed && status != domain.SummaryCustomerConfirmed {
		return false, domain.ErrInvalidTopic
	}
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE topic_summaries SET status=$3, confirmed_at=now(), created_by_user_id=COALESCE(created_by_user_id, $4)
		WHERE tenant_id=$1 AND id=$2 AND status IN ('ai_inferred','agent_confirmed','customer_confirmed')`, tenantID, summaryID, string(status), by)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *PostgresSummaryRepository) SupersedeInferredBefore(ctx context.Context, tenantID, topicID uuid.UUID, version int) error {
	_, err := r.q(ctx).Exec(ctx, `UPDATE topic_summaries SET status='superseded' WHERE tenant_id=$1 AND topic_thread_id=$2 AND status='ai_inferred' AND version < $3`, tenantID, topicID, version)
	return err
}

func (r *PostgresSummaryRepository) LockForGeneration(ctx context.Context, tenantID, topicID uuid.UUID) error {
	_, err := r.q(ctx).Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text || ':' || $2::text, 0))`, tenantID.String(), topicID.String())
	return err
}

func (r *PostgresSummaryRepository) Supersede(ctx context.Context, tenantID, summaryID uuid.UUID) error {
	_, err := r.q(ctx).Exec(ctx, `UPDATE topic_summaries SET status='superseded' WHERE tenant_id=$1 AND id=$2`, tenantID, summaryID)
	return err
}
