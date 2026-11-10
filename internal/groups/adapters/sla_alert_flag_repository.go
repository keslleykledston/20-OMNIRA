package adapters

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	groupsapp "github.com/omnira/omnira/internal/groups/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresSLAAlertFeatureFlagRepository backs the ADR-0058 operator kill switch (group_sla_alert_feature,
// migration 000182): a single row, platform-wide, no tenant_id. Not tenant data, so it never goes through
// a tenant RLS session — the same tenant=uuid.Nil, isSystemAdmin=true shape used by
// internal/hub/provisioning.Service.tx for other platform-wide state.
type PostgresSLAAlertFeatureFlagRepository struct{ pool *pgxpool.Pool }

var _ groupsapp.SLAAlertFeatureFlagRepository = (*PostgresSLAAlertFeatureFlagRepository)(nil)

func NewPostgresSLAAlertFeatureFlagRepository(pool *pgxpool.Pool) *PostgresSLAAlertFeatureFlagRepository {
	return &PostgresSLAAlertFeatureFlagRepository{pool: pool}
}

// Enabled is called on EVERY SLA-breach event (internal/worker/groupalert) — never only once at worker
// startup — so an operator flipping the switch in the admin console takes effect on the very next event.
func (r *PostgresSLAAlertFeatureFlagRepository) Enabled(ctx context.Context) (bool, error) {
	var enabled bool
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		return platformdb.QuerierFromContext(sctx, r.pool).QueryRow(sctx,
			`SELECT enabled FROM group_sla_alert_feature WHERE singleton`).Scan(&enabled)
	})
	return enabled, err
}

// SLAAlertFeatureFlagState is the admin console's view of the switch: who last touched it, and when.
type SLAAlertFeatureFlagState struct {
	Enabled   bool       `json:"enabled"`
	UpdatedBy *uuid.UUID `json:"updated_by,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Get reads the full state for the admin console (GET /api/v1/platform/group-sla-alert).
func (r *PostgresSLAAlertFeatureFlagRepository) Get(ctx context.Context) (SLAAlertFeatureFlagState, error) {
	var out SLAAlertFeatureFlagState
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		return platformdb.QuerierFromContext(sctx, r.pool).QueryRow(sctx,
			`SELECT enabled, updated_by, updated_at FROM group_sla_alert_feature WHERE singleton`).
			Scan(&out.Enabled, &out.UpdatedBy, &out.UpdatedAt)
	})
	return out, err
}

// Set is the only write path (PUT /api/v1/platform/group-sla-alert), always by a platform operator whose
// identity the HTTP handler has already confirmed in the CALLER's own session — this re-applies the same
// check inside the system session, never trusting that the caller did (defense in depth, same pattern as
// internal/platformrelease/updatemgr.authorize).
func (r *PostgresSLAAlertFeatureFlagRepository) Set(ctx context.Context, enabled bool, operator uuid.UUID) error {
	return platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		q := platformdb.QuerierFromContext(sctx, r.pool)
		var isOp bool
		if err := q.QueryRow(sctx, `SELECT is_platform_operator($1)`, operator).Scan(&isOp); err != nil {
			return err
		}
		if !isOp {
			return groupsapp.ErrNotPlatformOperator
		}
		if _, err := q.Exec(sctx, `UPDATE group_sla_alert_feature SET enabled = $1, updated_by = $2, updated_at = now() WHERE singleton`, enabled, operator); err != nil {
			return err
		}
		action := "group_sla_alert_feature.disabled"
		if enabled {
			action = "group_sla_alert_feature.enabled"
		}
		// Platform-wide event (tenant_id NULL), same direct-insert shape as
		// internal/platformrelease/updatemgr's audit() helper — the typed, tenant-scoped
		// audit system (internal/audit) rejects a nil tenant_id by construction.
		_, err := q.Exec(sctx, `INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
		                        VALUES ($1, NULL, $2, $3, 'platform', $4, 'success', $5, '{}'::jsonb)`,
			uuid.New(), operator, action, operator.String(), uuid.NewString())
		return err
	})
}
