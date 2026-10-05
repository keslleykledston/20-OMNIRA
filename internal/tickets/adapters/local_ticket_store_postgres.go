package adapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/tickets/domain"

	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// ErrLocalTicketExternalIdentityMismatch: the ticket already carries a
// DIFFERENT external_ticket_id than the one this enrichment call is trying
// to write. In V1 exactly one external create attempt enriches a given
// local ticket — a second, different identity is refused rather than
// overwritten (same safety posture as AttemptStore's own guard).
var ErrLocalTicketExternalIdentityMismatch = errors.New("tickets: local ticket already projects a different external ticket id")

// LocalTicketStore implements ports.LocalTicketStore. It runs inside the
// caller's tenant session, like AttemptStore.
type LocalTicketStore struct{ pool *pgxpool.Pool }

func NewLocalTicketStore(pool *pgxpool.Pool) *LocalTicketStore {
	return &LocalTicketStore{pool: pool}
}

// FindActiveByConversation reuses, verbatim, the selection rule already
// established and shipped by internal/inbox/adapters.
// PostgresInboundStore.FindOpenByConversation (PRODUCT.6-K2 section 1
// audit): the tenant's non-closed/non-resolved ticket for this
// conversation, most recently updated first, tie-broken by id. This is the
// only deterministic "canonical active ticket" rule that exists anywhere
// in the product today. Migration 000016's tickets_active_conversation_uq
// guarantees at most one such row per (tenant_id, conversation_id), so the
// ORDER BY/LIMIT here is defensive, never load-bearing (PRODUCT.6-M4
// audit). This is the single read primitive both CreateExternalTicket
// (enrichment target) and ReadConversationTicket (PRODUCT.6-O1, read-only)
// use — one query, two callers, never duplicated SQL.
func (s *LocalTicketStore) FindActiveByConversation(ctx context.Context, conversationID uuid.UUID) (*domain.Ticket, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	row := q.QueryRow(ctx, `
		SELECT id, tenant_id, conversation_id, status, priority, subject, assigned_to, created_at, updated_at,
		       resolved_at, closed_at, provider, external_ticket_id, external_status, external_status_label,
		       sync_status, last_synced_at
		FROM tickets
		WHERE tenant_id = $1 AND conversation_id = $2 AND status IN ('open', 'in_progress', 'waiting')
		ORDER BY topic_scoped ASC, updated_at DESC, id DESC LIMIT 1`, tenantID, conversationID)
	t := &domain.Ticket{}
	err = row.Scan(&t.ID, &t.TenantID, &t.ConversationID, &t.Status, &t.Priority, &t.Subject, &t.AssignedTo, &t.CreatedAt, &t.UpdatedAt,
		&t.ResolvedAt, &t.ClosedAt, &t.Provider, &t.ExternalTicketID, &t.ExternalStatus, &t.ExternalStatusLabel, &t.SyncStatus, &t.LastSyncedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tickets: find active ticket by conversation: %w", err)
	}
	return t, nil
}

// FindEnrichmentCandidate is CreateExternalTicket's name for the same
// primitive as FindActiveByConversation — kept as a thin alias so the
// PRODUCT.6-K2 create path's own vocabulary ("the ticket I am about to
// enrich") stays intact without a second query.
func (s *LocalTicketStore) FindEnrichmentCandidate(ctx context.Context, conversationID uuid.UUID) (*domain.Ticket, error) {
	return s.FindActiveByConversation(ctx, conversationID)
}

// EnrichExternalProjection is idempotent (repeating with the same values is
// a no-op) and refuses to replace an already-recorded, different
// external_ticket_id.
func (s *LocalTicketStore) EnrichExternalProjection(ctx context.Context, ticketID uuid.UUID, provider, externalTicketID, externalStatus, externalStatusLabel string, syncedAt time.Time) error {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	tag, err := q.Exec(ctx, `
		UPDATE tickets
		SET provider = $3, external_ticket_id = $4, external_status = $5, external_status_label = $6,
		    sync_status = 'synced', last_synced_at = $7, updated_at = now()
		WHERE tenant_id = $1 AND id = $2
		  AND (external_ticket_id IS NULL OR external_ticket_id = $4)`,
		tenantID, ticketID, provider, externalTicketID, externalStatus, externalStatusLabel, syncedAt)
	if err != nil {
		return fmt.Errorf("tickets: enrich external projection: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// Zero rows: either the ticket does not exist for this tenant, or the
	// guard (external_ticket_id IS NULL OR = same value) excluded it.
	var existing *string
	loadErr := q.QueryRow(ctx, `SELECT external_ticket_id FROM tickets WHERE tenant_id = $1 AND id = $2`, tenantID, ticketID).Scan(&existing)
	if errors.Is(loadErr, pgx.ErrNoRows) {
		return fmt.Errorf("tickets: enrichment target ticket not found")
	}
	if loadErr != nil {
		return fmt.Errorf("tickets: reconcile enrichment: %w", loadErr)
	}
	return ErrLocalTicketExternalIdentityMismatch
}
