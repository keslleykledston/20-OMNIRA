package adapters

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresContextAnalysisRepository runs inside the caller's tenant session (the window runner's per-window
// WithSystemTenantSession) — see migration 000177.
type PostgresContextAnalysisRepository struct{ pool *pgxpool.Pool }

var _ ports.ContextAnalysisRepository = (*PostgresContextAnalysisRepository)(nil)

func NewPostgresContextAnalysisRepository(pool *pgxpool.Pool) *PostgresContextAnalysisRepository {
	return &PostgresContextAnalysisRepository{pool: pool}
}

func (r *PostgresContextAnalysisRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

func (r *PostgresContextAnalysisRepository) WindowMessages(ctx context.Context, tenantID, conversationID uuid.UUID, messageIDs []uuid.UUID) ([]ports.WindowMessageRow, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	rows, err := r.q(ctx).Query(ctx, `
		SELECT m.id, m.direction = 'inbound', NULLIF(trim(m.body), ''), m.created_at,
		       (SELECT a.body FROM message_media_analysis a WHERE a.tenant_id = m.tenant_id AND a.message_id = m.id AND a.status = 'done' LIMIT 1)
		FROM messages m
		WHERE m.tenant_id = $1 AND m.conversation_id = $2 AND m.id = ANY($3::uuid[])`, tenantID, conversationID, messageIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[uuid.UUID]ports.WindowMessageRow, len(messageIDs))
	for rows.Next() {
		var row ports.WindowMessageRow
		var body, media *string
		if err := rows.Scan(&row.ID, &row.Inbound, &body, &row.At, &media); err != nil {
			return nil, err
		}
		switch {
		case body != nil:
			row.Text = *body
		case media != nil:
			row.Text, row.FromMedia = *media, true
		}
		byID[row.ID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ports.WindowMessageRow, 0, len(messageIDs))
	for _, id := range messageIDs {
		if row, ok := byID[id]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func (r *PostgresContextAnalysisRepository) Save(ctx context.Context, rec ports.AnalysisRecord) (uuid.UUID, error) {
	q := r.q(ctx)
	var firstAt, lastAt any
	if len(rec.AnalyzedMessageIDs) > 0 {
		// The ordered ids ARE the cursor; their own created_at is read back for the audit columns rather than
		// trusted from the caller, so a caller bug can never record a window's bounds that disagree with storage.
		if err := q.QueryRow(ctx, `
			SELECT min(created_at), max(created_at) FROM messages WHERE tenant_id=$1 AND id = ANY($2::uuid[])`,
			rec.TenantID, rec.AnalyzedMessageIDs).Scan(&firstAt, &lastAt); err != nil {
			return uuid.Nil, err
		}
	}
	analysisID := uuid.New()
	if _, err := q.Exec(ctx, `
		INSERT INTO context_window_analyses (id, tenant_id, conversation_id, window_id, previous_analysis_id, status, summary_text,
		                                      model_provider, model_name, prompt_version, input_tokens, output_tokens, latency_ms,
		                                      analyzed_message_count, analyzed_first_message_at, analyzed_last_message_at, error_class)
		SELECT $1, $2, $3, $4,
		       (SELECT id FROM context_window_analyses WHERE tenant_id=$2 AND conversation_id=$3 ORDER BY created_at DESC LIMIT 1),
		       $5, $6, NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), NULLIF($10,0), NULLIF($11,0), NULLIF($12,0), $13, $14, $15, NULLIF($16,'')`,
		analysisID, rec.TenantID, rec.ConversationID, rec.WindowID, rec.Status, rec.SummaryText,
		rec.ModelProvider, rec.ModelName, rec.PromptVersion, rec.InputTokens, rec.OutputTokens, rec.LatencyMS,
		len(rec.AnalyzedMessageIDs), firstAt, lastAt, rec.ErrorClass); err != nil {
		return uuid.Nil, err
	}
	for _, f := range rec.Facts {
		factID := uuid.New()
		if _, err := q.Exec(ctx, `
			INSERT INTO context_proposed_facts (id, tenant_id, analysis_id, topic_thread_id, kind, text)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			factID, rec.TenantID, analysisID, f.TopicThreadID, f.Kind, f.Text); err != nil {
			return uuid.Nil, err
		}
		for _, mid := range f.EvidenceMessageIDs {
			if _, err := q.Exec(ctx, `
				INSERT INTO context_proposed_fact_evidence (tenant_id, fact_id, message_id) VALUES ($1, $2, $3)
				ON CONFLICT (tenant_id, fact_id, message_id) DO NOTHING`, rec.TenantID, factID, mid); err != nil {
				return uuid.Nil, err
			}
		}
	}
	return analysisID, nil
}
