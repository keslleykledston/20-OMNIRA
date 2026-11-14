package adapters

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	intelligenceapp "github.com/omnira/omnira/internal/intelligence/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresContextIntelligenceV2FeatureFlagRepository backs the CONTEXT.2 operator kill switch
// (context_intelligence_v2_feature, migration 000204): a single row, platform-wide, no tenant_id.
// Not tenant data, so it never goes through a tenant RLS session — the same tenant=uuid.Nil,
// isSystemAdmin=true shape used by internal/hub/provisioning.Service.tx for other platform-wide
// state (same pattern as groupsadapters.PostgresSLAAlertFeatureFlagRepository).
type PostgresContextIntelligenceV2FeatureFlagRepository struct{ pool *pgxpool.Pool }

var _ intelligenceapp.ContextIntelligenceV2FeatureFlagRepository = (*PostgresContextIntelligenceV2FeatureFlagRepository)(nil)

func NewPostgresContextIntelligenceV2FeatureFlagRepository(pool *pgxpool.Pool) *PostgresContextIntelligenceV2FeatureFlagRepository {
	return &PostgresContextIntelligenceV2FeatureFlagRepository{pool: pool}
}

// Enabled is checked on EVERY ContextWindowRunner tick (ContextWindowRunner.WithLiveGate) — never
// only once at worker startup — so an operator flipping the switch in the admin console takes
// effect on the very next tick.
func (r *PostgresContextIntelligenceV2FeatureFlagRepository) Enabled(ctx context.Context) (bool, error) {
	var enabled bool
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		return platformdb.QuerierFromContext(sctx, r.pool).QueryRow(sctx,
			`SELECT enabled FROM context_intelligence_v2_feature WHERE singleton`).Scan(&enabled)
	})
	return enabled, err
}

// Get reads the full state for the admin console (GET /api/v1/platform/context-intelligence-v2).
func (r *PostgresContextIntelligenceV2FeatureFlagRepository) Get(ctx context.Context) (intelligenceapp.ContextIntelligenceV2FeatureFlagState, error) {
	var out intelligenceapp.ContextIntelligenceV2FeatureFlagState
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		return platformdb.QuerierFromContext(sctx, r.pool).QueryRow(sctx,
			`SELECT enabled, updated_by, updated_at FROM context_intelligence_v2_feature WHERE singleton`).
			Scan(&out.Enabled, &out.UpdatedBy, &out.UpdatedAt)
	})
	return out, err
}

// Set is the only write path (PUT /api/v1/platform/context-intelligence-v2), always by a platform
// operator whose identity the HTTP handler has already confirmed in the CALLER's own session — this
// re-applies the same check inside the system session, never trusting that the caller did (defense
// in depth, same pattern as internal/platformrelease/updatemgr.authorize).
func (r *PostgresContextIntelligenceV2FeatureFlagRepository) Set(ctx context.Context, enabled bool, operator uuid.UUID) error {
	return platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		q := platformdb.QuerierFromContext(sctx, r.pool)
		var isOp bool
		if err := q.QueryRow(sctx, `SELECT is_platform_operator($1)`, operator).Scan(&isOp); err != nil {
			return err
		}
		if !isOp {
			return intelligenceapp.ErrNotPlatformOperator
		}
		if _, err := q.Exec(sctx, `UPDATE context_intelligence_v2_feature SET enabled = $1, updated_by = $2, updated_at = now() WHERE singleton`, enabled, operator); err != nil {
			return err
		}
		action := "context_intelligence_v2_feature.disabled"
		if enabled {
			action = "context_intelligence_v2_feature.enabled"
		}
		// Platform-wide event (tenant_id NULL), same direct-insert shape as
		// internal/platformrelease/updatemgr's audit() helper and
		// groupsadapters.PostgresSLAAlertFeatureFlagRepository.Set — the typed, tenant-scoped audit
		// system (internal/audit) rejects a nil tenant_id by construction.
		_, err := q.Exec(sctx, `INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
		                        VALUES ($1, NULL, $2, $3, 'platform', $4, 'success', $5, '{}'::jsonb)`,
			uuid.New(), operator, action, operator.String(), uuid.NewString())
		return err
	})
}
