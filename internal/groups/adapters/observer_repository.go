package adapters

import (
	"context"

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
// opted into the observer (ai_observer_enabled), the message must be from a participant (never from_me) and
// have real text, the tenant must be active, and no suggestion row may exist yet. Oldest first, so a group
// that just opted in is worked through in the order the conversation happened.
func (r *PostgresObserverRepository) Claim(ctx context.Context, limit int) ([]groupsapp.Candidate, error) {
	if limit <= 0 {
		limit = 1
	}
	var out []groupsapp.Candidate
	err := r.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		rows, err := q.Query(ctx, `
			SELECT m.id, m.tenant_id, m.body
			FROM wa_group_messages m
			JOIN wa_groups g ON g.tenant_id = m.tenant_id AND g.id = m.group_id
			JOIN tenants t ON t.id = m.tenant_id AND t.status = 'active'
			LEFT JOIN wa_group_message_suggestions s ON s.tenant_id = m.tenant_id AND s.group_message_id = m.id
			WHERE g.enabled AND g.ai_observer_enabled AND NOT m.from_me AND length(trim(m.body)) > 0 AND s.id IS NULL
			ORDER BY m.sent_at
			LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c groupsapp.Candidate
			if err := rows.Scan(&c.ID, &c.TenantID, &c.Body); err != nil {
				return err
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
