package adapters

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type auditor struct{ pool *pgxpool.Pool }

// NewAuditor writes the module's actions to the append-only audit log inside the request's transaction. Metadata is ids, counts and enums only.
//
// It writes the row itself instead of going through audit.Store: that path refuses an event without an actor, which silently drops everything a SYSTEM session does
// (a flow that closes a session, the finalization on behalf of a Hub agent). Here the actor is the signed-in person; failing that, the person the system session
// acts on behalf of (meta "acting_user_id"); failing that, nobody, and the row says so ("actor_kind": "system"). The audit row is best effort by contract: it must
// not fail the operation it describes (the transaction already makes the data change and this row atomic when it succeeds).
func NewAuditor(pool *pgxpool.Pool) ports.Auditor { return &auditor{pool: pool} }

func (a *auditor) Record(ctx context.Context, action, resourceType string, resourceID uuid.UUID, meta map[string]any) {
	if a == nil || a.pool == nil {
		return
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return
	}
	var actor *uuid.UUID
	kind := "user"
	switch {
	case tc.ActorID != uuid.Nil:
		id := tc.ActorID
		actor = &id
	default:
		kind = "system"
		if raw, ok := meta["acting_user_id"].(string); ok {
			if id, err := uuid.Parse(raw); err == nil {
				actor = &id
			}
		}
	}
	m := map[string]any{"actor_kind": kind}
	if tc.Source == tenancydomain.AccessSourceHubServe && tc.HubID != nil {
		// the trail of a delegated action says who acted, through which hub, contract and grant (ADR-0040)
		m["via"], m["acting_as"], m["hub_id"] = "hub", tc.ActingAs(), tc.HubID.String()
		if tc.ServiceContractID != nil {
			m["contract_id"] = tc.ServiceContractID.String()
		}
		if tc.EffectiveGrantID != nil {
			m["grant_id"] = tc.EffectiveGrantID.String()
		}
	}
	for k, v := range meta {
		m[k] = v
	}
	body, err := json.Marshal(m)
	if err != nil {
		return
	}
	_, _ = platformdb.QuerierFromContext(ctx, a.pool).Exec(ctx, `
		INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'success', $7, $8, now())`,
		uuid.New(), tc.TenantID, actor, action, resourceType, resourceID.String(), nonEmpty(tc.CorrelationID, uuid.NewString()), body)
}
