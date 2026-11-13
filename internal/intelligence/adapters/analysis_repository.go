package adapters

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// LatestForConversation is CONTEXT.2C's read side: the conversation's most recent analysis row, its facts (joined
// with the topic each one was filed under) and each fact's evidence, three small queries rather than one wide join —
// easier to read, and evidence/facts are not 1:1 so a single join would duplicate fact rows per evidence message.
func (r *PostgresContextAnalysisRepository) LatestForConversation(ctx context.Context, tenantID, conversationID uuid.UUID) (*ports.ContextViewRow, error) {
	q := r.q(ctx)
	row := &ports.ContextViewRow{}
	err := q.QueryRow(ctx, `
		SELECT id, status, summary_text, created_at, analyzed_message_count, analyzed_last_message_at
		FROM context_window_analyses WHERE tenant_id=$1 AND conversation_id=$2 ORDER BY created_at DESC LIMIT 1`,
		tenantID, conversationID).Scan(&row.AnalysisID, &row.Status, &row.SummaryText, &row.CreatedAt, &row.AnalyzedMessageCount, &row.AnalyzedLastMessageAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// LEFT JOIN, not INNER: topic_threads has no Hub-delegated read policy yet (pre-existing gap, ADR-0017/migration
	// 000063 — out of scope here), so an INNER JOIN would silently drop every fact of a delegated actor's own
	// tenant the moment topic_threads' RLS hides the topic row. A LEFT JOIN still returns the fact — with an
	// empty title/status instead of vanishing — which is the correct degradation (no cross-tenant data, just a
	// missing decoration) until that upstream gap is closed.
	factRows, err := q.Query(ctx, `
		SELECT f.id, f.topic_thread_id, COALESCE(t.title, ''), COALESCE(t.status, ''), f.kind, f.text, f.truth_level, f.status, f.created_at
		FROM context_proposed_facts f LEFT JOIN topic_threads t ON t.tenant_id = f.tenant_id AND t.id = f.topic_thread_id
		WHERE f.tenant_id=$1 AND f.analysis_id=$2 ORDER BY f.created_at ASC`, tenantID, row.AnalysisID)
	if err != nil {
		return nil, err
	}
	var factIDs []uuid.UUID
	for factRows.Next() {
		var f ports.ContextViewFact
		if err := factRows.Scan(&f.ID, &f.TopicThreadID, &f.TopicTitle, &f.TopicStatus, &f.Kind, &f.Text, &f.TruthLevel, &f.Status, &f.CreatedAt); err != nil {
			factRows.Close()
			return nil, err
		}
		row.Facts = append(row.Facts, f)
		factIDs = append(factIDs, f.ID)
	}
	factRows.Close()
	if err := factRows.Err(); err != nil {
		return nil, err
	}
	if len(factIDs) == 0 {
		return row, nil
	}
	// Built only now that row.Facts has stopped growing: appending above can reallocate the backing array, which
	// would invalidate pointers taken during the loop.
	byID := make(map[uuid.UUID]*ports.ContextViewFact, len(row.Facts))
	for i := range row.Facts {
		byID[row.Facts[i].ID] = &row.Facts[i]
	}
	evRows, err := q.Query(ctx, `SELECT fact_id, message_id FROM context_proposed_fact_evidence WHERE tenant_id=$1 AND fact_id = ANY($2::uuid[])`, tenantID, factIDs)
	if err != nil {
		return nil, err
	}
	for evRows.Next() {
		var factID, msgID uuid.UUID
		if err := evRows.Scan(&factID, &msgID); err != nil {
			evRows.Close()
			return nil, err
		}
		if f, ok := byID[factID]; ok {
			f.EvidenceMessageIDs = append(f.EvidenceMessageIDs, msgID)
		}
	}
	evRows.Close()
	if err := evRows.Err(); err != nil {
		return nil, err
	}
	return row, nil
}

// ConversationCoverage reads the conversation's current inbound traffic directly from messages, never from a
// window/analysis row — so "stale" can be answered even when no analysis exists yet, or the latest one is old.
func (r *PostgresContextAnalysisRepository) ConversationCoverage(ctx context.Context, tenantID, conversationID uuid.UUID) (int, *time.Time, error) {
	var total int
	var lastAt *time.Time
	err := r.q(ctx).QueryRow(ctx, `
		SELECT count(*), max(created_at) FROM messages WHERE tenant_id=$1 AND conversation_id=$2 AND direction='inbound'`,
		tenantID, conversationID).Scan(&total, &lastAt)
	return total, lastAt, err
}

func (r *PostgresContextAnalysisRepository) ExistsForWindow(ctx context.Context, tenantID, windowID uuid.UUID) (bool, error) {
	var exists bool
	err := r.q(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM context_window_analyses WHERE tenant_id=$1 AND window_id=$2)`,
		tenantID, windowID).Scan(&exists)
	return exists, err
}

// IsConversationFullyAnalyzed: every inbound message either has no window yet (vanishingly rare — Touch commits
// window membership and the message in the same transaction) or belongs to a window with an analysis receipt. A
// message belongs to exactly one window ever (migration 000176's PRIMARY KEY (tenant_id, message_id) on
// context_analysis_window_messages), so this is exact — no timestamp comparison, no ambiguity from two messages
// sharing a created_at or an older message landing in a later window.
func (r *PostgresContextAnalysisRepository) IsConversationFullyAnalyzed(ctx context.Context, tenantID, conversationID uuid.UUID) (bool, error) {
	var ok bool
	err := r.q(ctx).QueryRow(ctx, `
		SELECT NOT EXISTS (
		  SELECT 1 FROM messages m
		  WHERE m.tenant_id=$1 AND m.conversation_id=$2 AND m.direction='inbound'
		    AND NOT EXISTS (
		      SELECT 1 FROM context_analysis_window_messages wm
		      JOIN context_window_analyses a ON a.tenant_id = wm.tenant_id AND a.window_id = wm.window_id
		      WHERE wm.tenant_id = m.tenant_id AND wm.message_id = m.id
		    )
		)`, tenantID, conversationID).Scan(&ok)
	return ok, err
}
