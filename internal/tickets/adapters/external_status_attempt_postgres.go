package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/tickets/domain"
	"github.com/omnira/omnira/internal/tickets/ports"

	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// ErrStatusAttemptInvalidTransition: a state transition was attempted from
// a state that does not permit it (e.g. confirming success on a row that
// is already confirmed_failure). Guarded UPDATEs make this structurally
// rare, but callers must be able to detect and refuse to proceed rather
// than silently no-op into an unsafe state. Mirrors
// ErrAttemptInvalidTransition's role for ticket_external_create_attempts.
var ErrStatusAttemptInvalidTransition = errors.New("tickets: invalid external status attempt state transition")

// StatusMutationAttemptStore is the durable safety foundation for external
// ticket STATUS MUTATION (PRODUCT.6-O2B1). Like AttemptStore, it runs
// inside the caller's tenant session (SET LOCAL + RLS) and never opens its
// own transaction.
type StatusMutationAttemptStore struct{ pool *pgxpool.Pool }

func NewStatusMutationAttemptStore(pool *pgxpool.Pool) *StatusMutationAttemptStore {
	return &StatusMutationAttemptStore{pool: pool}
}

const statusAttemptColumns = `id, tenant_id, local_ticket_id, conversation_id, actor_user_id, idempotency_key,
	request_hash, provider, external_ticket_id, target_status, state,
	confirmed_external_status, confirmed_external_status_label, projection_synced_at, created_at, updated_at`

func scanStatusAttempt(row pgx.Row) (*domain.ExternalStatusAttempt, error) {
	a := &domain.ExternalStatusAttempt{}
	err := row.Scan(&a.ID, &a.TenantID, &a.LocalTicketID, &a.ConversationID, &a.ActorUserID, &a.IdempotencyKey,
		&a.RequestHash, &a.Provider, &a.ExternalTicketID, &a.TargetStatus, &a.State,
		&a.ConfirmedExternalStatus, &a.ConfirmedExternalStatusLabel, &a.ProjectionSyncedAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// Acquire implements ports.StatusMutationAttemptStore.Acquire.
//
// Concurrency: two simultaneous Acquire calls with the same idempotency
// key rely on UNIQUE(tenant_id, idempotency_key); two simultaneous calls
// with DIFFERENT keys for the SAME local ticket rely on
// ticket_external_status_attempts_blocking_local_ticket_uq (migration
// 000050, WHERE state IN ('in_flight','outcome_unknown')) — never a
// process mutex. Exactly one caller ever receives AcquireAcquired for a
// given local ticket's unresolved-operation slot at a time.
func (s *StatusMutationAttemptStore) Acquire(ctx context.Context, cmd ports.AcquireStatusMutationAttemptCommand) (*domain.ExternalStatusAttempt, ports.AcquireOutcome, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, "", err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	id := uuid.New()
	row := q.QueryRow(ctx, `
		INSERT INTO ticket_external_status_attempts
		  (id, tenant_id, local_ticket_id, conversation_id, actor_user_id, idempotency_key, request_hash,
		   provider, external_ticket_id, target_status, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'in_flight')
		ON CONFLICT DO NOTHING
		RETURNING `+statusAttemptColumns,
		id, tenantID, cmd.LocalTicketID, cmd.ConversationID, cmd.ActorUserID, cmd.IdempotencyKey, cmd.RequestHash,
		cmd.Provider, cmd.ExternalTicketID, cmd.TargetStatus)
	attempt, err := scanStatusAttempt(row)
	if err == nil {
		return attempt, ports.AcquireAcquired, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, "", fmt.Errorf("tickets: acquire external status attempt: %w", err)
	}

	// The unqualified ON CONFLICT DO NOTHING above fires for EITHER unique
	// constraint (idempotency_key or the blocking local_ticket_id index) —
	// determine which one by checking the idempotency_key first (a real
	// same-key replay takes priority over blocking classification).
	existing, loadErr := scanStatusAttempt(q.QueryRow(ctx, `
		SELECT `+statusAttemptColumns+`
		FROM ticket_external_status_attempts
		WHERE tenant_id = $1 AND idempotency_key = $2`, tenantID, cmd.IdempotencyKey))
	if loadErr == nil {
		if existing.RequestHash != cmd.RequestHash {
			return nil, ports.AcquireIdempotencyMismatch, nil
		}
		return existing, ports.AcquireExistingSameIntent, nil
	}
	if !errors.Is(loadErr, pgx.ErrNoRows) {
		return nil, "", fmt.Errorf("tickets: load existing external status attempt: %w", loadErr)
	}

	// Not a same-key conflict — must be the blocking local_ticket_id index.
	blocking, blockErr := scanStatusAttempt(q.QueryRow(ctx, `
		SELECT `+statusAttemptColumns+`
		FROM ticket_external_status_attempts
		WHERE tenant_id = $1 AND local_ticket_id = $2 AND state IN ('in_flight', 'outcome_unknown')`,
		tenantID, cmd.LocalTicketID))
	if errors.Is(blockErr, pgx.ErrNoRows) {
		return nil, "", fmt.Errorf("tickets: acquire status attempt conflicted but no matching idempotency-key or blocking local-ticket row was found")
	}
	if blockErr != nil {
		return nil, "", fmt.Errorf("tickets: load blocking external status attempt: %w", blockErr)
	}
	return blocking, ports.AcquireBlockedByUnresolvedOperation, nil
}

// MarkConfirmedSuccess transitions an in_flight attempt to
// confirmed_success, recording the reconciled provider snapshot.
// Replay-safe: a no-op if already confirmed_success with the SAME
// confirmed status; refused (ErrStatusAttemptInvalidTransition) for any
// other existing state or a different confirmed status.
func (s *StatusMutationAttemptStore) MarkConfirmedSuccess(ctx context.Context, attemptID uuid.UUID, confirmedExternalStatus, confirmedExternalStatusLabel string) (*domain.ExternalStatusAttempt, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	updated, err := scanStatusAttempt(q.QueryRow(ctx, `
		UPDATE ticket_external_status_attempts
		SET state = 'confirmed_success', confirmed_external_status = $3, confirmed_external_status_label = $4, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND state = 'in_flight'
		RETURNING `+statusAttemptColumns,
		tenantID, attemptID, confirmedExternalStatus, confirmedExternalStatusLabel))
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tickets: mark status attempt confirmed success: %w", err)
	}
	current, loadErr := s.loadForReconcile(ctx, tenantID, attemptID)
	if loadErr != nil {
		return nil, loadErr
	}
	if current.State != domain.AttemptConfirmedSuccess {
		return nil, ErrStatusAttemptInvalidTransition
	}
	if current.ConfirmedExternalStatus == nil || *current.ConfirmedExternalStatus != confirmedExternalStatus {
		return nil, ErrStatusAttemptInvalidTransition
	}
	return current, nil
}

// MarkConfirmedFailure transitions an in_flight attempt to
// confirmed_failure. Idempotent no-op if already confirmed_failure;
// refused from any other existing state. NON-BLOCKING: releases the
// unresolved-operation barrier for this local ticket immediately
// (PRODUCT.6-O2B1 section 14).
func (s *StatusMutationAttemptStore) MarkConfirmedFailure(ctx context.Context, attemptID uuid.UUID) (*domain.ExternalStatusAttempt, error) {
	return s.markTerminal(ctx, attemptID, domain.AttemptConfirmedFailure)
}

// MarkOutcomeUnknown transitions an in_flight attempt to outcome_unknown.
// Idempotent no-op if already outcome_unknown; refused from any other
// existing state. There is deliberately no transition back to in_flight —
// that would be an automatic retry path. REMAINS BLOCKING: the partial
// unique index still covers this state (PRODUCT.6-O2B1 section 15).
func (s *StatusMutationAttemptStore) MarkOutcomeUnknown(ctx context.Context, attemptID uuid.UUID) (*domain.ExternalStatusAttempt, error) {
	return s.markTerminal(ctx, attemptID, domain.AttemptOutcomeUnknown)
}

func (s *StatusMutationAttemptStore) markTerminal(ctx context.Context, attemptID uuid.UUID, target domain.AttemptState) (*domain.ExternalStatusAttempt, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	updated, err := scanStatusAttempt(q.QueryRow(ctx, `
		UPDATE ticket_external_status_attempts
		SET state = $3, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND state = 'in_flight'
		RETURNING `+statusAttemptColumns,
		tenantID, attemptID, string(target)))
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tickets: mark status attempt %s: %w", target, err)
	}
	current, loadErr := s.loadForReconcile(ctx, tenantID, attemptID)
	if loadErr != nil {
		return nil, loadErr
	}
	if current.State != target {
		return nil, ErrStatusAttemptInvalidTransition
	}
	return current, nil
}

func (s *StatusMutationAttemptStore) loadForReconcile(ctx context.Context, tenantID, attemptID uuid.UUID) (*domain.ExternalStatusAttempt, error) {
	q := platformdb.QuerierFromContext(ctx, s.pool)
	current, err := scanStatusAttempt(q.QueryRow(ctx, `
		SELECT `+statusAttemptColumns+`
		FROM ticket_external_status_attempts
		WHERE tenant_id = $1 AND id = $2`, tenantID, attemptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tickets: external status attempt not found")
	}
	if err != nil {
		return nil, fmt.Errorf("tickets: reconcile external status attempt: %w", err)
	}
	return current, nil
}

// MarkProjectionSynced records that the local ticket's external projection
// fields were updated from this attempt's reconciled snapshot. Idempotent:
// a no-op if ProjectionSyncedAt is already set. Refused
// (ErrStatusAttemptInvalidTransition) if the attempt is not
// confirmed_success — projection completion is only meaningful once the
// provider mutation is durably confirmed/reconciled.
func (s *StatusMutationAttemptStore) MarkProjectionSynced(ctx context.Context, attemptID uuid.UUID) (*domain.ExternalStatusAttempt, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	updated, err := scanStatusAttempt(q.QueryRow(ctx, `
		UPDATE ticket_external_status_attempts
		SET projection_synced_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND state = 'confirmed_success' AND projection_synced_at IS NULL
		RETURNING `+statusAttemptColumns,
		tenantID, attemptID))
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tickets: mark status attempt projection synced: %w", err)
	}
	current, loadErr := s.loadForReconcile(ctx, tenantID, attemptID)
	if loadErr != nil {
		return nil, loadErr
	}
	if current.State != domain.AttemptConfirmedSuccess || current.ProjectionSyncedAt == nil {
		return nil, ErrStatusAttemptInvalidTransition
	}
	return current, nil
}

// GetByID loads an attempt by its own ID, tenant-scoped.
func (s *StatusMutationAttemptStore) GetByID(ctx context.Context, attemptID uuid.UUID) (*domain.ExternalStatusAttempt, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	attempt, err := scanStatusAttempt(q.QueryRow(ctx, `
		SELECT `+statusAttemptColumns+`
		FROM ticket_external_status_attempts
		WHERE tenant_id = $1 AND id = $2`, tenantID, attemptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tickets: get external status attempt: %w", err)
	}
	return attempt, nil
}
