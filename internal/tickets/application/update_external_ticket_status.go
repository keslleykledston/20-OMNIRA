// Package application implements PRODUCT.6-O2B3: the provider-neutral
// EXTERNAL STATUS MUTATION use case — a real write against the tenant's
// ERP (K3G confirmed real: PUT /api/support/tickets/{id}/status,
// PRODUCT.6-O2A3/O2B2). It reuses the exact same conversation-ownership
// authorization, active-local-ticket lookup, and existing-link
// preconditions already frozen by CreateExternalTicket (PRODUCT.6-K2) and
// RefreshTicketProjection (PRODUCT.6-O1R) — ErrForbidden,
// ErrConversationNotFound, ErrUnassigned, ErrNotAssignedToYou,
// ErrNoActiveTicket, ErrInconsistentExternalLink, ErrTicketNotLinked and
// ErrProviderMismatch are all defined in sibling files of this same
// package and are never redefined here.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
	"github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// PermissionTicketUpdate mirrors PermissionTicketCreate/PermissionConversationManage's
// convention (permission keys checked through the role-permission matrix,
// never a role-name branch). Distinct from both ticket.create ("create a
// ticket from my own conversation") and ticket.read (tenant-wide) —
// PRODUCT.6-O2A section 7/13, migration 000049.
const PermissionTicketUpdate = "ticket.update"

var (
	ErrInvalidStatusCommand      = errors.New("tickets: tenant, conversation, actor, target status and idempotency key are required")
	ErrStatusIdempotencyMismatch = errors.New("tickets: Idempotency-Key was already used with a different request")
)

// StatusOutcome is the provider-neutral result of an
// UpdateExternalTicketStatus call. Mirrors CreateExternalTicket's Outcome
// convention: every branch that reaches a durable state returns a
// populated Result with err == nil; a Go error is reserved for failures
// that abort BEFORE any durable state could be established, or for the
// narrow crash windows where a provider-confirmed fact could not itself be
// made durable.
type StatusOutcome string

const (
	// OutcomeStatusUpdated: this call performed the provider PUT, it
	// succeeded, its returned identity/status were validated, and the
	// local projection was enriched and marked synced.
	OutcomeStatusUpdated StatusOutcome = "updated"
	// OutcomeStatusReplaySuccess: a prior call already confirmed this exact
	// (tenant, idempotency key) mutation; this call made no provider call.
	// Covers both an already-fully-synced replay and a projection-only
	// recovery from the attempt's own durable confirmed snapshot.
	OutcomeStatusReplaySuccess StatusOutcome = "replay_success"
	// OutcomeStatusReconciledSuccess: the mutation's outcome was ambiguous
	// (transport failure, 5xx/429, malformed 2xx, or a returned identity/
	// status mismatch), and exactly one GetTicket confirmed the provider's
	// current status matches the originally requested target.
	OutcomeStatusReconciledSuccess StatusOutcome = "reconciled_success"
	// OutcomeStatusDefinitiveFailure: the provider definitively rejected
	// the mutation (validation/unauthorized) — this call's own PUT, or a
	// prior confirmed_failure attempt being replayed. No lifecycle change
	// occurred.
	OutcomeStatusDefinitiveFailure StatusOutcome = "definitive_failure"
	// OutcomeStatusReconciliationRequired: the write outcome is not safely
	// resolvable automatically — covers an in-flight replay, an ambiguous
	// write whose reconciliation GetTicket disagreed or failed, a
	// cross-key block by another unresolved operation, and the narrow
	// crash windows where a provider-confirmed fact or a local write could
	// not be made durable. No automatic provider retry ever happens here.
	OutcomeStatusReconciliationRequired StatusOutcome = "reconciliation_required"
)

// UpdateExternalTicketStatusCommand is the provider-neutral V1 status
// mutation intent. TargetStatus is an OPAQUE provider-neutral code
// (PRODUCT.6-O2A3/O2B2's ExternalStatusTarget.Code) — this service never
// encodes K3G-specific "1".."6" validation; the connector/adapter owns
// that.
type UpdateExternalTicketStatusCommand struct {
	TenantID       uuid.UUID
	ConversationID uuid.UUID
	ActorUserID    uuid.UUID
	TargetStatus   string
	IdempotencyKey string
}

// StatusResult is returned for every non-error outcome.
type StatusResult struct {
	Outcome             StatusOutcome
	AttemptID           uuid.UUID
	AttemptState        ticketsdomain.AttemptState
	LocalTicketID       uuid.UUID
	Provider            string
	ExternalTicketID    string
	ExternalStatus      string
	ExternalStatusLabel string
	SyncStatus          string
	LastSyncedAt        *time.Time
	// Replayed is true for every outcome except a fresh OutcomeStatusUpdated
	// — mirrors CreateExternalTicket's Result.Replayed intent (did THIS
	// call's own PUT produce this result, or did it recover a prior one).
	Replayed bool
	// Reconciled is true only when the success was recovered via the
	// ambiguous-write GetTicket path (OutcomeStatusReconciledSuccess).
	Reconciled bool
	// FailureCode is set only for OutcomeStatusDefinitiveFailure, taken
	// from connectors.TicketingErrorCodeOf — never fabricated.
	FailureCode connectors.TicketingErrorCode
	// Severe marks the narrow crash window where a provider-confirmed fact
	// (success or ambiguity) could not itself be made durable — mirrors
	// CreateExternalTicket's Result.Severe.
	Severe bool
}

// UpdateExternalTicketStatusService implements PRODUCT.6-O2B3. It never
// imports a concrete adapter or K3G wire type — only internal/tickets/ports
// and internal/tool/connectors' provider-neutral ticketing boundary.
type UpdateExternalTicketStatusService struct {
	perms        ports.PermissionChecker
	conversation ports.ConversationAuthorizer
	attempts     ports.StatusMutationAttemptStore
	localTickets ports.LocalTicketStore
	runtime      ports.TicketingRuntimeResolver
}

func NewUpdateExternalTicketStatusService(perms ports.PermissionChecker, conversation ports.ConversationAuthorizer, attempts ports.StatusMutationAttemptStore, localTickets ports.LocalTicketStore, runtime ports.TicketingRuntimeResolver) *UpdateExternalTicketStatusService {
	return &UpdateExternalTicketStatusService{perms: perms, conversation: conversation, attempts: attempts, localTickets: localTickets, runtime: runtime}
}

func validateStatusCommand(cmd UpdateExternalTicketStatusCommand) error {
	if cmd.TenantID == uuid.Nil || cmd.ConversationID == uuid.Nil || cmd.ActorUserID == uuid.Nil {
		return ErrInvalidStatusCommand
	}
	if strings.TrimSpace(cmd.TargetStatus) == "" {
		return ErrInvalidStatusCommand
	}
	if !idempotencyKeyPattern.MatchString(cmd.IdempotencyKey) {
		return ErrInvalidIdempotencyKey
	}
	return nil
}

// statusRequestHash fingerprints the effective status-mutation intent
// (PRODUCT.6-O2B3 section 7): tenant, local ticket, conversation, provider,
// external ticket id, and the requested target — the durable identity the
// mutation is scoped to, never actor/timestamps/random values. ActorUserID
// is deliberately EXCLUDED for the same reason requestHash (CreateExternalTicket)
// excludes it: a shift handoff or a different agent retrying the identical
// intent must be recognized as the SAME operation, not a mismatch.
func statusRequestHash(tenantID, localTicketID, conversationID uuid.UUID, provider, externalTicketID, targetStatus string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		tenantID.String(), localTicketID.String(), conversationID.String(), provider, externalTicketID, targetStatus,
	}, "\n")))
	return hex.EncodeToString(sum[:])
}

// UpdateExternalTicketStatus implements PRODUCT.6-O2B3 sections 3-21 in
// order: validate -> authorize (ticket.update AND conversation ownership)
// -> load the active local ticket -> require a CONSISTENT existing link
// (never linked=false, never partial) -> resolve the tenant's ticketing
// runtime -> require the resolved provider to match the ticket's linked
// provider -> acquire the durable status-mutation attempt -> either replay
// a prior outcome or perform exactly one provider PUT and durably record
// its outcome before touching local projection.
func (s *UpdateExternalTicketStatusService) UpdateExternalTicketStatus(ctx context.Context, cmd UpdateExternalTicketStatusCommand) (*StatusResult, error) {
	if err := validateStatusCommand(cmd); err != nil {
		return nil, err
	}

	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.TenantID != cmd.TenantID || tc.ActorID != cmd.ActorUserID || tc.Source != tenancydomain.AccessSourceDirect {
		return nil, ErrForbidden
	}

	// Authorization BEFORE any provider-facing or durable-attempt work —
	// same ordering CreateExternalTicket already uses.
	canUpdate, err := s.perms.HasPermission(ctx, cmd.ActorUserID, PermissionTicketUpdate)
	if err != nil {
		return nil, err
	}
	if !canUpdate {
		return nil, ErrForbidden
	}

	assignedTo, found, err := s.conversation.LoadAssignment(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrConversationNotFound
	}
	if assignedTo == nil {
		return nil, ErrUnassigned
	}
	if *assignedTo != cmd.ActorUserID {
		canManage, err := s.perms.HasPermission(ctx, cmd.ActorUserID, PermissionConversationManage)
		if err != nil {
			return nil, err
		}
		if !canManage {
			return nil, ErrNotAssignedToYou
		}
	}

	ticket, err := s.localTickets.FindActiveByConversation(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	if ticket == nil {
		return nil, ErrNoActiveTicket
	}

	hasProvider := ticket.Provider != nil && strings.TrimSpace(*ticket.Provider) != ""
	hasExternalID := ticket.ExternalTicketID != nil && strings.TrimSpace(*ticket.ExternalTicketID) != ""
	if hasProvider != hasExternalID {
		return nil, ErrInconsistentExternalLink
	}
	if !hasProvider {
		return nil, ErrTicketNotLinked
	}

	// PRODUCT.6-L: resolve THIS tenant's TicketingConnector fresh for this
	// call, after authorization, exactly like CreateExternalTicket/
	// RefreshTicketProjection.
	rt, err := s.runtime.Resolve(ctx, cmd.TenantID)
	if err != nil {
		return nil, err
	}
	if rt.TicketingConnector.Name() != *ticket.Provider {
		return nil, ErrProviderMismatch
	}

	hash := statusRequestHash(cmd.TenantID, ticket.ID, cmd.ConversationID, *ticket.Provider, *ticket.ExternalTicketID, cmd.TargetStatus)
	attempt, outcome, err := s.attempts.Acquire(ctx, ports.AcquireStatusMutationAttemptCommand{
		LocalTicketID: ticket.ID, ConversationID: cmd.ConversationID, ActorUserID: cmd.ActorUserID,
		IdempotencyKey: cmd.IdempotencyKey, RequestHash: hash,
		Provider: *ticket.Provider, ExternalTicketID: *ticket.ExternalTicketID, TargetStatus: cmd.TargetStatus,
	})
	if err != nil {
		return nil, err
	}

	switch outcome {
	case ports.AcquireIdempotencyMismatch:
		return nil, ErrStatusIdempotencyMismatch
	case ports.AcquireBlockedByUnresolvedOperation:
		// A DIFFERENT key already owns an unresolved operation for this
		// local ticket. Never hijack/reconcile another operation's key
		// from this new intent (PRODUCT.6-O2B3 section 12).
		return &StatusResult{Outcome: OutcomeStatusReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
			LocalTicketID: ticket.ID, Provider: attempt.Provider, ExternalTicketID: attempt.ExternalTicketID}, nil
	case ports.AcquireAcquired:
		return s.mutateAndRecord(ctx, attempt, ticket, rt.TicketingConnector)
	case ports.AcquireExistingSameIntent:
		return s.replay(ctx, attempt, ticket, rt.TicketingConnector)
	default:
		return nil, fmt.Errorf("tickets: unknown acquire outcome %q", outcome)
	}
}

// replay implements PRODUCT.6-O2B3 section 10: EXISTING_SAME_INTENT
// behavior depends entirely on the durable attempt's current state. Never
// issues a PUT.
func (s *UpdateExternalTicketStatusService) replay(ctx context.Context, attempt *ticketsdomain.ExternalStatusAttempt, ticket *ticketsdomain.Ticket, connector connectors.TicketingConnector) (*StatusResult, error) {
	switch attempt.State {
	case ticketsdomain.AttemptConfirmedSuccess:
		if attempt.ProjectionSyncedAt != nil {
			// Already fully synced — no PUT, no GetTicket, no projection write.
			return &StatusResult{
				Outcome: OutcomeStatusReplaySuccess, AttemptID: attempt.ID, AttemptState: attempt.State, LocalTicketID: ticket.ID,
				Provider: attempt.Provider, ExternalTicketID: attempt.ExternalTicketID,
				ExternalStatus: derefOr(attempt.ConfirmedExternalStatus, ""), ExternalStatusLabel: derefOr(attempt.ConfirmedExternalStatusLabel, ""),
				SyncStatus: "synced", LastSyncedAt: attempt.ProjectionSyncedAt, Replayed: true,
			}, nil
		}
		// Projection-only recovery from the attempt's OWN durable confirmed
		// snapshot (PRODUCT.6-O2B3 section 16) — never a PUT, never a GetTicket.
		return s.syncProjection(ctx, attempt, ticket, OutcomeStatusReplaySuccess)
	case ticketsdomain.AttemptConfirmedFailure:
		// No fabricated FailureCode on replay — same documented decision as
		// CreateExternalTicket's OutcomeDefinitiveFailure.
		return &StatusResult{Outcome: OutcomeStatusDefinitiveFailure, AttemptID: attempt.ID, AttemptState: attempt.State, LocalTicketID: ticket.ID}, nil
	case ticketsdomain.AttemptInFlight:
		// Never auto-reconcile an in_flight row — the original request may
		// still be executing. No stale-timeout unlock (section 10 IMPORTANT).
		return &StatusResult{Outcome: OutcomeStatusReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
			LocalTicketID: ticket.ID, Provider: attempt.Provider, ExternalTicketID: attempt.ExternalTicketID}, nil
	case ticketsdomain.AttemptOutcomeUnknown:
		return s.reconcileAmbiguous(ctx, attempt, ticket, connector)
	default:
		return nil, fmt.Errorf("tickets: unknown status attempt state %q", attempt.State)
	}
}

// isDefinitiveStatusRejection classifies a TicketingConnector.UpdateTicketStatus
// error as a DEFINITIVE provider rejection (PRODUCT.6-O2B3 section 18) —
// every other code (WRITE_OUTCOME_UNKNOWN, NOT_FOUND, NOT_MIGRATED,
// PROVIDER_UNAVAILABLE, UNKNOWN_PROVIDER_ERROR) is treated as ambiguous
// (section 19/26): OMNIRA's durable link is historical evidence and must
// never be treated as "the provider rejected this mutation" merely because
// the ticket could not be confirmed right now.
func isDefinitiveStatusRejection(code connectors.TicketingErrorCode) bool {
	return code == connectors.TicketingValidationError || code == connectors.TicketingUnauthorized
}

// mutateAndRecord implements PRODUCT.6-O2B3 sections 13-21: exactly one
// provider UpdateTicketStatus call, durable recording of its outcome
// BEFORE any local projection write, and — only after that durable record
// succeeds — enrichment of the local ticket.
func (s *UpdateExternalTicketStatusService) mutateAndRecord(ctx context.Context, attempt *ticketsdomain.ExternalStatusAttempt, ticket *ticketsdomain.Ticket, connector connectors.TicketingConnector) (*StatusResult, error) {
	result, err := connector.UpdateTicketStatus(ctx, attempt.ExternalTicketID, connectors.ExternalStatusTarget{Code: attempt.TargetStatus})
	if err != nil {
		code := connectors.TicketingErrorCodeOf(err)
		if !isDefinitiveStatusRejection(code) {
			// Section 19: ambiguous write — durably record BEFORE any
			// reconciliation read.
			marked, markErr := s.attempts.MarkOutcomeUnknown(ctx, attempt.ID)
			if markErr != nil {
				return &StatusResult{Outcome: OutcomeStatusReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
					LocalTicketID: ticket.ID, Provider: attempt.Provider, ExternalTicketID: attempt.ExternalTicketID}, nil
			}
			return s.reconcileAmbiguous(ctx, marked, ticket, connector)
		}
		// Section 18: definitive provider rejection.
		confirmed, markErr := s.attempts.MarkConfirmedFailure(ctx, attempt.ID)
		if markErr != nil {
			// Durable operation state is still unresolved — never present a
			// clean definitive outcome in that case.
			return &StatusResult{Outcome: OutcomeStatusReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
				LocalTicketID: ticket.ID, Provider: attempt.Provider, ExternalTicketID: attempt.ExternalTicketID}, nil
		}
		return &StatusResult{Outcome: OutcomeStatusDefinitiveFailure, AttemptID: confirmed.ID, AttemptState: confirmed.State,
			LocalTicketID: ticket.ID, FailureCode: code}, nil
	}

	// Section 13/21: a syntactically successful result is not trusted
	// blindly. Durable identity must match; a foreign returned ID is never
	// adopted, and reconciliation (if any) always targets the DURABLE
	// external_ticket_id, never the foreign one.
	if result.ExternalID != attempt.ExternalTicketID || result.ExternalStatus != attempt.TargetStatus {
		marked, markErr := s.attempts.MarkOutcomeUnknown(ctx, attempt.ID)
		if markErr != nil {
			return &StatusResult{Outcome: OutcomeStatusReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
				LocalTicketID: ticket.ID, Provider: attempt.Provider, ExternalTicketID: attempt.ExternalTicketID}, nil
		}
		return s.reconcileAmbiguous(ctx, marked, ticket, connector)
	}

	// Section 14: durable success BEFORE local projection.
	confirmed, err := s.attempts.MarkConfirmedSuccess(ctx, attempt.ID, result.ExternalStatus, result.ExternalStatusLabel)
	if err != nil {
		// Section 15: provider success is real, but OMNIRA could not
		// durably record it. Never PUT again; never project first.
		return &StatusResult{Outcome: OutcomeStatusReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
				LocalTicketID: ticket.ID, Provider: attempt.Provider, ExternalTicketID: attempt.ExternalTicketID, Severe: true},
			fmt.Errorf("tickets: status mutation succeeded but could not be durably recorded: %w", err)
	}

	return s.syncProjection(ctx, confirmed, ticket, OutcomeStatusUpdated)
}

// reconcileAmbiguous implements PRODUCT.6-O2B3 section 20: exactly ONE
// GetTicket against the durable external_ticket_id, comparing the
// provider's current identity/status against what this operation durably
// owns. Never issues a second PUT. Never marks confirmed_failure — the
// provider's true state remains unknown, not rejected.
func (s *UpdateExternalTicketStatusService) reconcileAmbiguous(ctx context.Context, attempt *ticketsdomain.ExternalStatusAttempt, ticket *ticketsdomain.Ticket, connector connectors.TicketingConnector) (*StatusResult, error) {
	unresolved := func() *StatusResult {
		return &StatusResult{Outcome: OutcomeStatusReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
			LocalTicketID: ticket.ID, Provider: attempt.Provider, ExternalTicketID: attempt.ExternalTicketID}
	}

	snapshot, err := connector.GetTicket(ctx, attempt.ExternalTicketID)
	if err != nil {
		// Case C: GetTicket itself fails (includes NOT_FOUND/NOT_MIGRATED —
		// the durable link is historical evidence, never reinterpreted as
		// "no ticket"). Leave outcome_unknown.
		return unresolved(), nil
	}
	if snapshot.ExternalID != attempt.ExternalTicketID {
		// Case D: never adopt a different identity.
		return unresolved(), nil
	}
	if snapshot.ExternalStatus != attempt.TargetStatus {
		// Case B: same identity, different status — the provider may have
		// changed status later through another actor/system. Never mark
		// confirmed_failure; never retry.
		return unresolved(), nil
	}

	// Case A: reconciled success.
	confirmed, err := s.attempts.MarkConfirmedSuccess(ctx, attempt.ID, snapshot.ExternalStatus, snapshot.ExternalStatusLabel)
	if err != nil {
		r := unresolved()
		r.Severe = true
		return r, fmt.Errorf("tickets: status mutation reconciled but could not be durably recorded: %w", err)
	}
	return s.syncProjection(ctx, confirmed, ticket, OutcomeStatusReconciledSuccess)
}

// syncProjection enriches the local ticket from a confirmed_success
// attempt's durable snapshot and marks the attempt's projection synced.
// Idempotent and safe to call repeatedly. outcome fixes the caller's
// intended StatusResult.Outcome; Replayed/Reconciled are derived from it.
func (s *UpdateExternalTicketStatusService) syncProjection(ctx context.Context, confirmed *ticketsdomain.ExternalStatusAttempt, ticket *ticketsdomain.Ticket, outcome StatusOutcome) (*StatusResult, error) {
	externalStatus := derefOr(confirmed.ConfirmedExternalStatus, "")
	externalStatusLabel := derefOr(confirmed.ConfirmedExternalStatusLabel, "")
	now := time.Now().UTC()
	if err := s.localTickets.EnrichExternalProjection(ctx, ticket.ID, confirmed.Provider, confirmed.ExternalTicketID, externalStatus, externalStatusLabel, now); err != nil {
		// Section 16/17: provider success remains durable on the attempt
		// row even though local enrichment failed. Never PUT again; a
		// later replay retries only this projection step.
		return &StatusResult{Outcome: OutcomeStatusReconciliationRequired, AttemptID: confirmed.ID, AttemptState: confirmed.State,
			LocalTicketID: ticket.ID, Provider: confirmed.Provider, ExternalTicketID: confirmed.ExternalTicketID}, nil
	}
	synced, err := s.attempts.MarkProjectionSynced(ctx, confirmed.ID)
	if err != nil {
		return &StatusResult{Outcome: OutcomeStatusReconciliationRequired, AttemptID: confirmed.ID, AttemptState: confirmed.State,
			LocalTicketID: ticket.ID, Provider: confirmed.Provider, ExternalTicketID: confirmed.ExternalTicketID}, nil
	}
	return &StatusResult{
		Outcome: outcome, AttemptID: synced.ID, AttemptState: synced.State, LocalTicketID: ticket.ID,
		Provider: synced.Provider, ExternalTicketID: synced.ExternalTicketID,
		ExternalStatus: externalStatus, ExternalStatusLabel: externalStatusLabel, SyncStatus: "synced", LastSyncedAt: &now,
		Replayed: outcome != OutcomeStatusUpdated, Reconciled: outcome == OutcomeStatusReconciledSuccess,
	}, nil
}
