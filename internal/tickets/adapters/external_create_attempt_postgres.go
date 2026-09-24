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
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ErrAttemptIdempotencyMismatch: the same idempotency key was already used
// with a different request (different request_hash) — mirrors
// internal/messages/application.ErrIdempotencyMismatch's semantics for this
// table. Kept as the same value as ports.ErrIdempotencyMismatch (PRODUCT.6-K2)
// so callers can match on either identifier via errors.Is, while
// internal/tickets/application only ever needs to know about the
// ports-level one.
var ErrAttemptIdempotencyMismatch = ports.ErrIdempotencyMismatch

// ErrAttemptInvalidTransition: a state transition was attempted from a
// state that does not permit it (e.g. confirming success on a row that is
// already confirmed_failure). Guarded UPDATEs make this structurally rare,
// but callers must be able to detect and refuse to proceed rather than
// silently no-op into an unsafe state.
var ErrAttemptInvalidTransition = errors.New("tickets: invalid external create attempt state transition")

// ErrAttemptExternalIdentityMismatch: a confirmed_success row already
// carries a different external_ticket_id than the one just reported. This
// must never happen for a correct caller (one attempt row maps to exactly
// one provider write) and is refused rather than overwritten.
var ErrAttemptExternalIdentityMismatch = errors.New("tickets: external ticket identity cannot be replaced on an existing attempt")

// AttemptStore is the durable safety foundation for external ticket
// creation (PRODUCT.6-K1). It runs inside the caller's tenant session (SET
// LOCAL + RLS), like PostgresOutboundStore — it never opens its own
// transaction.
type AttemptStore struct{ pool *pgxpool.Pool }

func NewAttemptStore(pool *pgxpool.Pool) *AttemptStore {
	return &AttemptStore{pool: pool}
}

func attemptTenantOf(ctx context.Context) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("tickets: tenant context required")
	}
	return tc.TenantID, nil
}

const attemptColumns = `id, tenant_id, conversation_id, actor_user_id, idempotency_key, request_hash,
	state, provider, external_ticket_id, local_ticket_id, projection_synced_at, created_at, updated_at`

func scanAttempt(row pgx.Row) (*domain.ExternalCreateAttempt, error) {
	a := &domain.ExternalCreateAttempt{}
	err := row.Scan(&a.ID, &a.TenantID, &a.ConversationID, &a.ActorUserID, &a.IdempotencyKey, &a.RequestHash,
		&a.State, &a.Provider, &a.ExternalTicketID, &a.LocalTicketID, &a.ProjectionSyncedAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// Acquire atomically claims an idempotency key for a new external create
// attempt, or returns the existing attempt if the key was already used.
//
//   - new key: inserts state=in_flight, returns (attempt, acquired=true).
//     Only the caller that acquired=true owns the right to perform the
//     provider POST.
//   - same key + same requestHash: returns the existing durable attempt,
//     acquired=false. The caller must NOT POST again — it must instead act
//     on the existing attempt's State (replay confirmed_success, surface
//     confirmed_failure, or report reconciliation-required for in_flight/
//     outcome_unknown).
//   - same key + different requestHash: ErrAttemptIdempotencyMismatch.
//
// Concurrency: two simultaneous Acquire calls with the same key rely on the
// database's UNIQUE(tenant_id, idempotency_key) constraint, never a process
// mutex — exactly one caller receives acquired=true.
func (s *AttemptStore) Acquire(ctx context.Context, conversationID, actorUserID uuid.UUID, idempotencyKey, requestHash string) (*domain.ExternalCreateAttempt, bool, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, false, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	id := uuid.New()
	row := q.QueryRow(ctx, `
		INSERT INTO ticket_external_create_attempts
		  (id, tenant_id, conversation_id, actor_user_id, idempotency_key, request_hash, state)
		VALUES ($1, $2, $3, $4, $5, $6, 'in_flight')
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
		RETURNING `+attemptColumns,
		id, tenantID, conversationID, actorUserID, idempotencyKey, requestHash)
	attempt, err := scanAttempt(row)
	if err == nil {
		return attempt, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("tickets: acquire external create attempt: %w", err)
	}

	existing, err := scanAttempt(q.QueryRow(ctx, `
		SELECT `+attemptColumns+`
		FROM ticket_external_create_attempts
		WHERE tenant_id = $1 AND idempotency_key = $2`, tenantID, idempotencyKey))
	if err != nil {
		return nil, false, fmt.Errorf("tickets: load existing external create attempt: %w", err)
	}
	if existing.RequestHash != requestHash {
		return nil, false, ErrAttemptIdempotencyMismatch
	}
	return existing, false, nil
}

// MarkConfirmedSuccess transitions an in_flight attempt to
// confirmed_success and durably records the provider's external identity.
// Replay-safe: if the attempt is already confirmed_success with the SAME
// external identity, it is returned unchanged (no-op). A different external
// identity on an already-confirmed row is refused
// (ErrAttemptExternalIdentityMismatch) rather than overwritten. Any other
// existing state (confirmed_failure, outcome_unknown) is refused
// (ErrAttemptInvalidTransition) — those states must never silently become a
// success.
func (s *AttemptStore) MarkConfirmedSuccess(ctx context.Context, attemptID uuid.UUID, provider, externalTicketID string) (*domain.ExternalCreateAttempt, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	updated, err := scanAttempt(q.QueryRow(ctx, `
		UPDATE ticket_external_create_attempts
		SET state = 'confirmed_success', provider = $3, external_ticket_id = $4, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND state = 'in_flight'
		RETURNING `+attemptColumns,
		tenantID, attemptID, provider, externalTicketID))
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tickets: mark confirmed success: %w", err)
	}
	return s.reconcileNoOpTransition(ctx, tenantID, attemptID, domain.AttemptConfirmedSuccess, provider, externalTicketID)
}

// MarkConfirmedFailure transitions an in_flight attempt to
// confirmed_failure. Idempotent no-op if already confirmed_failure;
// refused from any other existing state.
func (s *AttemptStore) MarkConfirmedFailure(ctx context.Context, attemptID uuid.UUID) (*domain.ExternalCreateAttempt, error) {
	return s.markTerminal(ctx, attemptID, domain.AttemptConfirmedFailure)
}

// MarkOutcomeUnknown transitions an in_flight attempt to outcome_unknown.
// Idempotent no-op if already outcome_unknown; refused from any other
// existing state. There is deliberately no transition back to in_flight —
// that would be an automatic retry path, which PRODUCT.6-K1 forbids.
func (s *AttemptStore) MarkOutcomeUnknown(ctx context.Context, attemptID uuid.UUID) (*domain.ExternalCreateAttempt, error) {
	return s.markTerminal(ctx, attemptID, domain.AttemptOutcomeUnknown)
}

func (s *AttemptStore) markTerminal(ctx context.Context, attemptID uuid.UUID, target domain.AttemptState) (*domain.ExternalCreateAttempt, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	updated, err := scanAttempt(q.QueryRow(ctx, `
		UPDATE ticket_external_create_attempts
		SET state = $3, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND state = 'in_flight'
		RETURNING `+attemptColumns,
		tenantID, attemptID, string(target)))
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tickets: mark %s: %w", target, err)
	}
	return s.reconcileNoOpTransition(ctx, tenantID, attemptID, target, "", "")
}

// reconcileNoOpTransition is reached only when the guarded UPDATE affected
// zero rows: either the row does not exist for this tenant, or it is
// already in a terminal state. It loads the row and decides which of those
// two happened, returning a no-op success only when the row is already in
// exactly the target state (and, for confirmed_success, the same external
// identity) — every other case is ErrAttemptInvalidTransition.
func (s *AttemptStore) reconcileNoOpTransition(ctx context.Context, tenantID, attemptID uuid.UUID, target domain.AttemptState, provider, externalTicketID string) (*domain.ExternalCreateAttempt, error) {
	q := platformdb.QuerierFromContext(ctx, s.pool)
	current, err := scanAttempt(q.QueryRow(ctx, `
		SELECT `+attemptColumns+`
		FROM ticket_external_create_attempts
		WHERE tenant_id = $1 AND id = $2`, tenantID, attemptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tickets: external create attempt not found")
	}
	if err != nil {
		return nil, fmt.Errorf("tickets: reconcile external create attempt: %w", err)
	}
	if current.State != target {
		return nil, ErrAttemptInvalidTransition
	}
	if target == domain.AttemptConfirmedSuccess {
		if current.Provider == nil || current.ExternalTicketID == nil || *current.Provider != provider || *current.ExternalTicketID != externalTicketID {
			return nil, ErrAttemptExternalIdentityMismatch
		}
	}
	return current, nil
}

// MarkProjectionSynced records that the local ticket projection was
// enriched for a confirmed_success attempt. Idempotent: if
// ProjectionSyncedAt is already set, the row is returned unchanged (the
// local_ticket_id is never replaced). Refused (ErrAttemptInvalidTransition)
// if the attempt is not confirmed_success — projection completion is only
// meaningful once provider success is durable.
func (s *AttemptStore) MarkProjectionSynced(ctx context.Context, attemptID, localTicketID uuid.UUID) (*domain.ExternalCreateAttempt, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	updated, err := scanAttempt(q.QueryRow(ctx, `
		UPDATE ticket_external_create_attempts
		SET local_ticket_id = $3, projection_synced_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND state = 'confirmed_success' AND projection_synced_at IS NULL
		RETURNING `+attemptColumns,
		tenantID, attemptID, localTicketID))
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tickets: mark projection synced: %w", err)
	}
	current, loadErr := scanAttempt(q.QueryRow(ctx, `
		SELECT `+attemptColumns+`
		FROM ticket_external_create_attempts
		WHERE tenant_id = $1 AND id = $2`, tenantID, attemptID))
	if errors.Is(loadErr, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tickets: external create attempt not found")
	}
	if loadErr != nil {
		return nil, fmt.Errorf("tickets: reconcile projection sync: %w", loadErr)
	}
	if current.State != domain.AttemptConfirmedSuccess || current.ProjectionSyncedAt == nil {
		return nil, ErrAttemptInvalidTransition
	}
	return current, nil
}

// GetByID loads an attempt by its own ID, tenant-scoped.
func (s *AttemptStore) GetByID(ctx context.Context, attemptID uuid.UUID) (*domain.ExternalCreateAttempt, error) {
	tenantID, err := attemptTenantOf(ctx)
	if err != nil {
		return nil, err
	}
	q := platformdb.QuerierFromContext(ctx, s.pool)
	attempt, err := scanAttempt(q.QueryRow(ctx, `
		SELECT `+attemptColumns+`
		FROM ticket_external_create_attempts
		WHERE tenant_id = $1 AND id = $2`, tenantID, attemptID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tickets: get external create attempt: %w", err)
	}
	return attempt, nil
}
