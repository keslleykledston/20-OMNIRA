package adapters

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	groupsapp "github.com/omnira/omnira/internal/groups/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresObserverRepository is the AI observer's view of wa_group_messages/wa_group_message_suggestions
// (ADR-0055). Every call runs in a system session (is_system_admin): the worker has no human actor, and
// Claim spans every tenant that opted a group in, which an ordinary tenant-scoped session cannot do.
type PostgresObserverRepository struct{ pool *pgxpool.Pool }

var _ groupsapp.Repository = (*PostgresObserverRepository)(nil)

func NewPostgresObserverRepository(pool *pgxpool.Pool) *PostgresObserverRepository {
	return &PostgresObserverRepository{pool: pool}
}

func (r *PostgresObserverRepository) system(ctx context.Context, fn func(ctx context.Context, q platformdb.Querier) error) error {
	return platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		return fn(sctx, platformdb.QuerierFromContext(sctx, r.pool))
	})
}

// Claim finds group messages still waiting for a suggestion: the group must be both reading (enabled) and
// opted into the observer (ai_observer_enabled), the message must be from a participant (never from_me), the
// tenant must be active, and no suggestion row may exist yet. Eligible means real text OR (ADR-0015 addendum
// 2) a completed image analysis — a text-less image message with no analysis yet (or one that never
// completes) simply waits, same as a media message always has under ADR-0055 decision 4. Oldest first, so a
// group that just opted in is worked through in the order the conversation happened.
func (r *PostgresObserverRepository) Claim(ctx context.Context, limit int) ([]groupsapp.Candidate, error) {
	if limit <= 0 {
		limit = 1
	}
	var out []groupsapp.Candidate
	err := r.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		rows, err := q.Query(ctx, `
			SELECT m.id, m.tenant_id, m.group_id, m.sent_at, m.body, COALESCE(a.body, '')
			FROM wa_group_messages m
			JOIN wa_groups g ON g.tenant_id = m.tenant_id AND g.id = m.group_id
			JOIN tenants t ON t.id = m.tenant_id AND t.status = 'active'
			LEFT JOIN wa_group_message_suggestions s ON s.tenant_id = m.tenant_id AND s.group_message_id = m.id
			LEFT JOIN wa_group_message_analysis a ON a.tenant_id = m.tenant_id AND a.group_message_id = m.id AND a.status = 'done'
			WHERE g.enabled AND g.ai_observer_enabled AND NOT m.from_me AND s.id IS NULL
			  AND (length(trim(m.body)) > 0 OR length(trim(COALESCE(a.body, ''))) > 0)
			ORDER BY m.sent_at
			LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c groupsapp.Candidate
			if err := rows.Scan(&c.ID, &c.TenantID, &c.GroupID, &c.SentAt, &c.Body, &c.ImageCaption); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// Context returns up to limit of the group's EARLIER messages (strictly before the candidate, sent_at >=
// since), newest first — the observer reverses it for a readable transcript. The author shown is the same
// "team alias wins" rule the rest of the Grupos area already uses (GroupThread/ListMessages), and an
// image-only earlier message contributes its caption instead of an empty body.
func (r *PostgresObserverRepository) Context(ctx context.Context, tenantID, groupID, excludeMessageID uuid.UUID, before, since time.Time, limit int) ([]groupsapp.ContextMessage, error) {
	if limit <= 0 {
		limit = 1
	}
	var out []groupsapp.ContextMessage
	err := r.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		rows, err := q.Query(ctx, `
			SELECT COALESCE(NULLIF(cp.alias, ''), NULLIF(ct.alias, ''), m.author_name),
			       CASE WHEN length(trim(m.body)) > 0 THEN m.body WHEN a.body <> '' THEN '[imagem] ' || a.body ELSE '' END,
			       m.sent_at
			FROM wa_group_messages m
			LEFT JOIN channel_participants cp ON cp.tenant_id = m.tenant_id AND cp.id = m.sender_channel_participant_id
			LEFT JOIN contacts ct ON ct.tenant_id = cp.tenant_id AND ct.id = cp.contact_id
			LEFT JOIN wa_group_message_analysis a ON a.tenant_id = m.tenant_id AND a.group_message_id = m.id AND a.status = 'done'
			WHERE m.tenant_id = $1 AND m.group_id = $2 AND m.id <> $3 AND m.sent_at < $4 AND m.sent_at >= $5
			ORDER BY m.sent_at DESC
			LIMIT $6`, tenantID, groupID, excludeMessageID, before, since, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c groupsapp.ContextMessage
			if err := rows.Scan(&c.AuthorName, &c.Body, &c.SentAt); err != nil {
				return err
			}
			if c.Body == "" {
				continue // a from_me line or a still-media-only message with no caption yet: nothing to show
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// Save is idempotent (ON CONFLICT (tenant_id, group_message_id) DO NOTHING): a race between two claims of the
// same message keeps only the first result.
func (r *PostgresObserverRepository) Save(ctx context.Context, c groupsapp.Candidate, s groupsapp.Suggestion, modelProvider, modelName string, latencyMS int) (bool, error) {
	var saved bool
	err := r.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		tag, err := q.Exec(ctx, `
			INSERT INTO wa_group_message_suggestions (tenant_id, group_message_id, category, summary, suggested_action, confidence, model_provider, model_name, latency_ms)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (tenant_id, group_message_id) DO NOTHING`,
			c.TenantID, c.ID, s.Category, s.Summary, s.SuggestedAction, s.Confidence, modelProvider, modelName, latencyMS)
		if err != nil {
			return err
		}
		saved = tag.RowsAffected() > 0
		return nil
	})
	return saved, err
}
