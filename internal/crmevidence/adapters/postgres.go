// Package adapters implements internal/crmevidence/ports against Postgres
// (PRODUCT.7B2B).
package adapters

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/omnira/omnira/internal/platform/db"

	"github.com/omnira/omnira/internal/crmevidence/ports"
)

// PostgresEvidenceStore implements ports.EvidenceStore. It never opens its
// own transaction/tenant session — it runs inside whatever tenant-scoped
// querier the caller's context already carries (the same RLS session
// established by tenantSession for the HTTP request), exactly like
// internal/tickets/adapters.ConversationAuthorizer and
// internal/inbox/adapters.PostgresActivityConversations.
type PostgresEvidenceStore struct{ pool *pgxpool.Pool }

func NewPostgresEvidenceStore(pool *pgxpool.Pool) *PostgresEvidenceStore {
	return &PostgresEvidenceStore{pool: pool}
}

// RecordTicketSelection performs the idempotent upsert proven against a
// real PostgreSQL 16 database before this adapter was written: an INSERT
// with an ON CONFLICT target on the partial unique index
// crm_contact_company_evidence_active_uq (WHERE revoked_at IS NULL) — a
// plain named UNIQUE CONSTRAINT is NOT assumed, because none exists; the
// conflict target must include the same partial predicate as the index.
//
// A repeated observation of an already-active fact only advances
// last_verified_at (and updated_at) — first_verified_at and the
// FIRST-confirmation provenance columns (actor_user_id, origin_ticket_id,
// origin_conversation_id) are never rewritten by a later observation.
func (s *PostgresEvidenceStore) RecordTicketSelection(ctx context.Context, in ports.RecordTicketSelectionInput) error {
	now := time.Now().UTC()
	_, err := platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx, `
		INSERT INTO crm_contact_company_evidence
			(tenant_id, contact_id, connection_id, external_company_id, source,
			 actor_user_id, origin_ticket_id, origin_conversation_id,
			 first_verified_at, last_verified_at)
		VALUES ($1, $2, $3, $4, 'ticket_selection', $5, $6, $7, $8, $8)
		ON CONFLICT (tenant_id, contact_id, connection_id, external_company_id) WHERE revoked_at IS NULL
		DO UPDATE SET last_verified_at = EXCLUDED.last_verified_at, updated_at = now()`,
		in.TenantID, in.ContactID, in.ConnectionID, in.ExternalCompanyID,
		in.ActorUserID, in.OriginTicketID, in.OriginConversationID, now,
	)
	return err
}

var _ ports.EvidenceStore = (*PostgresEvidenceStore)(nil)
