// Package application implements PRODUCT.6-K2: the provider-neutral
// CreateExternalTicket use case. It depends only on internal/tickets/ports
// interfaces and internal/tool/connectors' provider-neutral ticketing
// boundary (ADR-0013) — never a concrete adapter, never K3G wire types.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
	"github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

const (
	// PermissionTicketCreate and PermissionConversationManage mirror
	// internal/messages/application's PermissionClaim/PermissionManage
	// constants in spirit: permission keys checked through the role-
	// permission matrix (ports.PermissionChecker), never a role-name
	// branch. ticket.create is deliberately NOT ticket.read (PRODUCT.6-F):
	// a tenant_agent has the former, not the latter.
	PermissionTicketCreate       = "ticket.create"
	PermissionConversationManage = "conversation.manage"
)

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

var (
	ErrInvalidCommand        = errors.New("tickets: tenant, conversation, actor, subject and idempotency key are required")
	ErrInvalidIdempotencyKey = errors.New("tickets: Idempotency-Key must be 8-128 chars of [A-Za-z0-9._:-]")
	ErrForbidden             = errors.New("tickets: forbidden")
	ErrConversationNotFound  = errors.New("tickets: conversation not found")
	ErrUnassigned            = errors.New("tickets: conversation must be assigned before an external ticket can be created")
	ErrNotAssignedToYou      = errors.New("tickets: conversation is assigned to another agent")
	ErrInvalidCompany        = errors.New("tickets: selected company is not a valid, active company for this tenant")
	ErrNoLocalTicketToEnrich = errors.New("tickets: conversation has no open local ticket to enrich")
	ErrIdempotencyMismatch   = errors.New("tickets: Idempotency-Key was already used with a different request")
)

// Outcome is the provider-neutral result of a CreateExternalTicket call.
// Every branch that reaches a durable state (including "do not retry")
// returns a populated Outcome with err == nil — Go errors are reserved for
// failures that abort BEFORE any durable state could be established
// (validation, authorization, company rejection, idempotency mismatch).
type Outcome string

const (
	// OutcomeCreated: this call performed the provider POST, it succeeded,
	// and the local ticket projection was enriched and marked synced.
	OutcomeCreated Outcome = "created"
	// OutcomeReplaySuccess: a prior call already created the external
	// ticket and synced the projection; this call made no provider call.
	OutcomeReplaySuccess Outcome = "replay_success"
	// OutcomeDefinitiveFailure: the provider definitively rejected the
	// create request (this call's or a prior replayed one). No external
	// ticket exists. FailureCode carries the category ONLY when this call
	// itself made the provider request — a replay of an
	// already-confirmed_failure attempt does not reproduce the original
	// TicketingErrorCode (see the package doc below on why that is an
	// explicit, non-fabricating decision, not a capability gap).
	OutcomeDefinitiveFailure Outcome = "definitive_failure"
	// OutcomeReconciliationRequired: the write outcome is not safely
	// resolvable automatically — covers WRITE_OUTCOME_UNKNOWN, an
	// in-flight attempt from a concurrent/earlier call, a confirmed
	// success whose local projection has not synced yet (this call
	// retries only the projection, never the provider write), and the
	// narrow crash window where a provider success could not even be
	// durably recorded. AttemptState and Severe distinguish these for
	// callers/ops; no automatic provider retry ever happens here.
	OutcomeReconciliationRequired Outcome = "reconciliation_required"
	// OutcomeAlreadyLinked (PRODUCT.6-M5): the active local ticket already
	// carries a consistent external link (Provider AND ExternalTicketID
	// both set) BEFORE this call ever touched AttemptStore or the
	// provider. This is deliberately distinct from OutcomeReplaySuccess:
	// the caller used a genuinely different Idempotency-Key — this is not
	// "the same request replayed", it is "a different request reaching a
	// local ticket that turned out to already be linked". No attempt is
	// acquired, no provider call is made, the existing identity is
	// returned unchanged.
	OutcomeAlreadyLinked Outcome = "already_linked"
)

// CreateExternalTicketCommand is the provider-neutral V1 create intent
// (PRODUCT.6-K2). SelectedCustomerExternalID is UNTRUSTED until this
// service validates it server-side against ports.CompanyDirectory — it is
// never forwarded to TicketingConnector as-is.
type CreateExternalTicketCommand struct {
	TenantID                   uuid.UUID
	ConversationID             uuid.UUID
	ActorUserID                uuid.UUID
	SelectedCustomerExternalID string
	Subject                    string
	Description                string
	IdempotencyKey             string
}

// Result is returned for every non-error outcome.
type Result struct {
	Outcome          Outcome
	AttemptID        uuid.UUID
	AttemptState     ticketsdomain.AttemptState
	ExternalTicketID string
	// Provider identifies which external system ExternalTicketID belongs
	// to (PRODUCT.6-M section 8, e.g. "k3g") — set whenever
	// ExternalTicketID is, taken from TicketingConnector.Name(), never a
	// hardcoded literal.
	Provider      string
	LocalTicketID uuid.UUID
	FailureCode   connectors.TicketingErrorCode
	// Severe marks the narrow PRODUCT.6-K2 section 9 crash window: the
	// provider confirmed success but persisting CONFIRMED_SUCCESS itself
	// failed. The external ticket exists; OMNIRA's durable record of that
	// fact may not. Requires human/ops reconciliation, not a retry.
	Severe bool
	// ConnectionID (PRODUCT.7B2B): additive integration metadata — the
	// exact tenant channel_connections row this call's runtime was
	// resolved from. Populated whenever a runtime was resolved, regardless
	// of Outcome; callers that persist derived evidence (e.g. Contact-
	// Company evidence) must only do so for Outcome values that prove a
	// real success (OutcomeCreated / OutcomeReplaySuccess) — this field
	// alone does not imply that. There is no separate provider field here:
	// the provider identity is read from the referenced connection
	// (channel_connections.provider) when needed, never duplicated.
	ConnectionID uuid.UUID
	// CustomerAccountID (ADR-0018): the OMNIRA account the ticket now targets, when one was resolved and stored.
	CustomerAccountID uuid.UUID
}

// Service implements CreateExternalTicket. It never imports a concrete
// adapter or K3G wire type — only internal/tickets/ports and
// internal/tool/connectors' provider-neutral TicketingConnector.
type Service struct {
	perms        ports.PermissionChecker
	conversation ports.ConversationAuthorizer
	attempts     ports.AttemptStore
	localTickets ports.LocalTicketStore
	// runtime resolves the tenant-scoped CompanyDirectory/TicketingConnector
	// pair (PRODUCT.6-L) — resolved per call, never bound at construction
	// time, so one long-lived Service instance can safely serve every
	// tenant: it never caches or reuses a connector/credential across
	// tenants (see runtime.Resolve, called once per CreateExternalTicket
	// call, right after conversation authorization).
	runtime ports.TicketingRuntimeResolver
	// accounts (ADR-0018, optional) resolves the OMNIRA customer account of the validated company; without it tickets
	// keep working exactly as before and simply carry no account.
	accounts ports.AccountResolver
}

// WithAccounts enables the customer account projection of tickets.
func (s *Service) WithAccounts(a ports.AccountResolver) *Service {
	s.accounts = a
	return s
}

func NewService(perms ports.PermissionChecker, conversation ports.ConversationAuthorizer, attempts ports.AttemptStore, localTickets ports.LocalTicketStore, runtime ports.TicketingRuntimeResolver) *Service {
	return &Service{perms: perms, conversation: conversation, attempts: attempts, localTickets: localTickets, runtime: runtime}
}

// requestHash fingerprints the effective external-create intent
// (PRODUCT.6-K2 section 5): tenant, conversation, the SERVER-VALIDATED
// customer external ID (never the raw browser selection — though for a
// valid command they are the same string, hashing the validated value
// keeps the fingerprint's meaning tied to "what was actually authorized to
// reach the provider"), subject and description.
//
// ActorUserID is deliberately EXCLUDED. Rationale (documented, not
// arbitrary): the same idempotency key being replayed by a different human
// actor is the expected shape of a shift handoff or a second agent retrying
// a request that timed out ambiguously on the first agent's client — that
// must be recognized as the SAME external-create intent, not a mismatch.
// ActorUserID is carried on the attempt row for audit (who initiated it)
// but never enters the fingerprint, and — per PRODUCT.6-K2 section 12 — is
// never forwarded to the provider.
func requestHash(tenantID, conversationID uuid.UUID, validatedCustomerExternalID, subject, description string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		tenantID.String(), conversationID.String(), validatedCustomerExternalID, subject, description,
	}, "\n")))
	return hex.EncodeToString(sum[:])
}

func (s *Service) has(ctx context.Context, actor uuid.UUID, permission string) (bool, error) {
	return s.perms.HasPermission(ctx, actor, permission)
}

// CreateExternalTicket implements PRODUCT.6-K2 sections 3-11 in order:
// validate -> authorize (ticket.create AND conversation ownership) ->
// validate the selected company against the trusted provider-backed
// source -> locate the local ticket to enrich -> acquire the durable
// idempotency attempt -> either replay a prior outcome or perform exactly
// one provider CreateTicket call and durably record its outcome before
// touching local projection.
func (s *Service) CreateExternalTicket(ctx context.Context, cmd CreateExternalTicketCommand) (*Result, error) {
	if err := validateCommand(cmd); err != nil {
		return nil, err
	}

	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.TenantID != cmd.TenantID || tc.ActorID != cmd.ActorUserID || tc.Source != tenancydomain.AccessSourceDirect {
		return nil, ErrForbidden
	}

	// Authorization BEFORE any provider-facing or durable-attempt work —
	// PRODUCT.6-K2 section 3: an unauthorized request must not acquire a
	// create attempt.
	canCreate, err := s.has(ctx, cmd.ActorUserID, PermissionTicketCreate)
	if err != nil {
		return nil, err
	}
	if !canCreate {
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
		canManage, err := s.has(ctx, cmd.ActorUserID, PermissionConversationManage)
		if err != nil {
			return nil, err
		}
		if !canManage {
			return nil, ErrNotAssignedToYou
		}
	}

	// PRODUCT.6-L: resolve THIS tenant's CompanyDirectory/TicketingConnector
	// pair fresh for this call — never a shared/global connector, never
	// another tenant's credential. Resolved after authorization (an
	// unauthorized caller never triggers a credential decrypt) and before
	// company validation (which needs CompanyDirectory).
	rt, err := s.runtime.Resolve(ctx, cmd.TenantID)
	if err != nil {
		return nil, err
	}

	validatedCustomerExternalID, validatedCompany, err := s.validateSelectedCompany(ctx, rt.CompanyDirectory, cmd.SelectedCustomerExternalID)
	if err != nil {
		return nil, err
	}

	localTicket, err := s.localTickets.FindEnrichmentCandidate(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	if localTicket == nil {
		return nil, ErrNoLocalTicketToEnrich
	}

	// PRODUCT.6-M5 Problem A: an already-linked local ticket must never
	// reach AttemptStore/the provider merely because this call uses a
	// different Idempotency-Key than whatever created the existing link.
	hasProvider := localTicket.Provider != nil && strings.TrimSpace(*localTicket.Provider) != ""
	hasExternalID := localTicket.ExternalTicketID != nil && strings.TrimSpace(*localTicket.ExternalTicketID) != ""
	if hasProvider && hasExternalID {
		return &Result{Outcome: OutcomeAlreadyLinked, LocalTicketID: localTicket.ID,
			Provider: *localTicket.Provider, ExternalTicketID: *localTicket.ExternalTicketID}, nil
	}
	if hasProvider != hasExternalID {
		// Inconsistent linkage (one set, the other not) — a data
		// integrity state, never "repaired" by creating another ERP
		// ticket. No attempt acquired, no provider call, no projection
		// write.
		return &Result{Outcome: OutcomeReconciliationRequired, LocalTicketID: localTicket.ID, Severe: true}, nil
	}

	// ADR-0018: the account this ticket targets, resolved from the company the DIRECTORY validated (never from the
	// browser). Before any provider write: a failure here leaves nothing behind but an idempotent local account.
	var accountID uuid.UUID
	if s.accounts != nil {
		accountID, err = s.accounts.ResolveForCompany(ctx, cmd.TenantID, rt.ConnectionID, validatedCompany)
		if err != nil {
			return nil, fmt.Errorf("tickets: resolve customer account: %w", err)
		}
	}

	hash := requestHash(cmd.TenantID, cmd.ConversationID, validatedCustomerExternalID, cmd.Subject, cmd.Description)
	attempt, acquired, err := s.attempts.Acquire(ctx, cmd.ConversationID, localTicket.ID, cmd.ActorUserID, cmd.IdempotencyKey, hash)
	if err != nil {
		if errors.Is(err, ports.ErrIdempotencyMismatch) {
			return nil, ErrIdempotencyMismatch
		}
		if errors.Is(err, ports.ErrLocalTicketBlocked) {
			// PRODUCT.6-M5 Problem B: a DIFFERENT key already owns a
			// blocking attempt for this exact local ticket. `attempt`
			// here is that blocker's row, not a row this call owns.
			return &Result{Outcome: OutcomeReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
				ExternalTicketID: derefOr(attempt.ExternalTicketID, ""), Provider: derefOr(attempt.Provider, ""),
				LocalTicketID: localTicket.ID}, nil
		}
		return nil, err
	}

	var result *Result
	if !acquired {
		result, err = s.replay(ctx, attempt, localTicket, rt.TicketingConnector)
	} else {
		result, err = s.createAndRecord(ctx, attempt, localTicket, validatedCustomerExternalID, cmd, rt.TicketingConnector)
	}
	if result != nil {
		// PRODUCT.7B2B: additive integration metadata only (the exact
		// connection this call's runtime was resolved from) — never read
		// by any customer-validation/idempotency/status/ERP-authority
		// logic above, never influences Outcome.
		result.ConnectionID = rt.ConnectionID
		// The projection is set only for a ticket THIS call (or a replay of the same intent) really created: an
		// already-linked ticket belongs to whatever company it was created for.
		if err == nil && accountID != uuid.Nil && (result.Outcome == OutcomeCreated || result.Outcome == OutcomeReplaySuccess) {
			if setter, ok := s.localTickets.(ports.CustomerAccountSetter); ok {
				if setErr := setter.SetCustomerAccount(ctx, localTicket.ID, accountID); setErr != nil {
					// The external ticket exists and is recorded: never fail the call over a derived projection.
					log.Printf("tickets: customer account projection failed (ticket=%s): %v", localTicket.ID, setErr)
				} else {
					result.CustomerAccountID = accountID
				}
			}
		}
	}
	return result, err
}

func validateCommand(cmd CreateExternalTicketCommand) error {
	if cmd.TenantID == uuid.Nil || cmd.ConversationID == uuid.Nil || cmd.ActorUserID == uuid.Nil {
		return ErrInvalidCommand
	}
	if strings.TrimSpace(cmd.Subject) == "" {
		return ErrInvalidCommand
	}
	if strings.TrimSpace(cmd.SelectedCustomerExternalID) == "" {
		return ErrInvalidCompany
	}
	if !idempotencyKeyPattern.MatchString(cmd.IdempotencyKey) {
		return ErrInvalidIdempotencyKey
	}
	return nil
}

// validateSelectedCompany implements PRODUCT.6-K0/6-K2 section 4: the
// browser-provided SelectedCustomerExternalID is never trusted directly. It
// must exactly match, uniquely, one ACTIVE company returned by the
// tenant's real trusted company source.
func (s *Service) validateSelectedCompany(ctx context.Context, directory ports.CompanyDirectory, selected string) (string, ports.Company, error) {
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return "", ports.Company{}, ErrInvalidCompany
	}
	companies, err := directory.ListCompanies(ctx)
	if err != nil {
		return "", ports.Company{}, err
	}
	matches := 0
	var found ports.Company
	for _, c := range companies {
		if c.ExternalID == selected {
			matches++
			found = c
		}
	}
	if matches != 1 || !found.Active {
		return "", ports.Company{}, ErrInvalidCompany
	}
	return selected, found, nil
}

// replay implements PRODUCT.6-K2 section 7 and PRODUCT.6-M4's recovery
// extension. It NEVER calls TicketingConnector.CreateTicket — a
// confirmed_success attempt whose projection never synced recovers the
// provider's current status via the READ-only GetTicket instead (section
// 4), never by re-creating.
func (s *Service) replay(ctx context.Context, attempt *ticketsdomain.ExternalCreateAttempt, localTicket *ticketsdomain.Ticket, ticketing connectors.TicketingConnector) (*Result, error) {
	switch attempt.State {
	case ticketsdomain.AttemptConfirmedSuccess:
		if attempt.ProjectionSyncedAt != nil {
			// PRODUCT.6-M3 invariant, preserved exactly: an already-synced
			// confirmed_success attempt returns immediately — no GetTicket,
			// no CreateTicket, no projection write, no attempt mutation.
			return &Result{Outcome: OutcomeReplaySuccess, AttemptID: attempt.ID, AttemptState: attempt.State,
				ExternalTicketID: derefOr(attempt.ExternalTicketID, ""), Provider: derefOr(attempt.Provider, ""),
				LocalTicketID: derefUUIDOr(attempt.LocalTicketID, localTicket.ID)}, nil
		}
		return s.recoverProjection(ctx, attempt, localTicket, ticketing)
	case ticketsdomain.AttemptConfirmedFailure:
		// Section 7: the original TicketingErrorCode is deliberately not
		// reproduced on replay — see the OutcomeDefinitiveFailure doc
		// comment. This is a documented product decision (definitive
		// failure only needs to communicate "no ticket exists, do not
		// retry"), not a fabricated value and not a capability gap: the
		// durable attempt schema has no failure-code column because V1's
		// contract never promised to replay the exact original category.
		return &Result{Outcome: OutcomeDefinitiveFailure, AttemptID: attempt.ID, AttemptState: attempt.State}, nil
	case ticketsdomain.AttemptOutcomeUnknown:
		return &Result{Outcome: OutcomeReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State}, nil
	case ticketsdomain.AttemptInFlight:
		// Never assume age makes this safe — always reconciliation-required.
		return &Result{Outcome: OutcomeReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State}, nil
	default:
		return nil, fmt.Errorf("tickets: unknown attempt state %q", attempt.State)
	}
}

// createAndRecord implements PRODUCT.6-K2 sections 8-11: exactly one
// provider CreateTicket call, durable recording of its outcome BEFORE any
// local projection write, and — only after that durable record succeeds —
// enrichment of the local ticket.
func (s *Service) createAndRecord(ctx context.Context, attempt *ticketsdomain.ExternalCreateAttempt, localTicket *ticketsdomain.Ticket, validatedCustomerExternalID string, cmd CreateExternalTicketCommand, ticketing connectors.TicketingConnector) (*Result, error) {
	ticket, err := ticketing.CreateTicket(ctx, connectors.CreateTicketRequest{
		CustomerExternalID: validatedCustomerExternalID, // cmd.ActorUserID never enters this request (section 12)
		Subject:            cmd.Subject,
		Description:        cmd.Description,
	})
	if err != nil {
		code := connectors.TicketingErrorCodeOf(err)
		if code == connectors.TicketingWriteOutcomeUnknown {
			if _, markErr := s.attempts.MarkOutcomeUnknown(ctx, attempt.ID); markErr != nil {
				return nil, fmt.Errorf("tickets: record outcome_unknown after ambiguous write: %w", markErr)
			}
			return &Result{Outcome: OutcomeReconciliationRequired, AttemptID: attempt.ID, AttemptState: ticketsdomain.AttemptOutcomeUnknown}, nil
		}
		if _, markErr := s.attempts.MarkConfirmedFailure(ctx, attempt.ID); markErr != nil {
			return nil, fmt.Errorf("tickets: record confirmed_failure after definitive provider rejection: %w", markErr)
		}
		return &Result{Outcome: OutcomeDefinitiveFailure, AttemptID: attempt.ID, AttemptState: ticketsdomain.AttemptConfirmedFailure, FailureCode: code}, nil
	}

	// Section 9: the durable attempt record is the recovery anchor and
	// MUST be written before any local projection touch.
	confirmed, err := s.attempts.MarkConfirmedSuccess(ctx, attempt.ID, ticketing.Name(), ticket.ExternalID)
	if err != nil {
		// The provider already created a real external ticket
		// (ticket.ExternalID is known) but OMNIRA could not durably
		// record that fact. Never retry the provider. This is the narrow
		// crash/storage-failure window PRODUCT.6-K2 section 9 calls out
		// explicitly — surfaced as Severe so callers/ops treat it with
		// higher urgency than an ordinary reconciliation case.
		return &Result{Outcome: OutcomeReconciliationRequired, AttemptID: attempt.ID, AttemptState: ticketsdomain.AttemptInFlight,
			ExternalTicketID: ticket.ExternalID, Provider: ticketing.Name(), Severe: true}, fmt.Errorf("tickets: external ticket %s created but not durably recorded: %w", ticket.ExternalID, err)
	}

	result, err := s.syncProjection(ctx, confirmed, localTicket, ticket)
	if err != nil {
		return nil, err
	}
	if result.Outcome == OutcomeReplaySuccess {
		result.Outcome = OutcomeCreated
	}
	return result, nil
}

// recoverProjection implements PRODUCT.6-M4 section 4-7: a confirmed_success
// attempt whose local projection never synced recovers the provider's
// current snapshot via a READ (TicketingConnector.GetTicket), never a
// second CreateTicket — the attempt store intentionally does not duplicate
// mutable ERP ticket status (section 18), so recovery re-reads it fresh.
func (s *Service) recoverProjection(ctx context.Context, attempt *ticketsdomain.ExternalCreateAttempt, localTicket *ticketsdomain.Ticket, ticketing connectors.TicketingConnector) (*Result, error) {
	if attempt.Provider == nil || attempt.ExternalTicketID == nil {
		return nil, fmt.Errorf("tickets: confirmed_success attempt %s missing provider/external_ticket_id", attempt.ID)
	}
	// Section 5: never query a potentially different provider than the one
	// that durably owns this attempt — no silent provider substitution.
	if ticketing.Name() != *attempt.Provider {
		return &Result{Outcome: OutcomeReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
			ExternalTicketID: *attempt.ExternalTicketID, Provider: *attempt.Provider}, nil
	}
	snapshot, err := ticketing.GetTicket(ctx, *attempt.ExternalTicketID)
	if err != nil {
		// Section 6: PROVIDER_UNAVAILABLE / NOT_FOUND / NOT_MIGRATED /
		// malformed response all leave the attempt exactly as it was —
		// confirmed_success, projection still unsynced — never a reason to
		// call CreateTicket.
		return &Result{Outcome: OutcomeReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
			ExternalTicketID: *attempt.ExternalTicketID, Provider: *attempt.Provider}, nil
	}
	// Section 7: the provider's answer must be about the SAME ticket this
	// attempt owns — never overwrite the durable external identity.
	if snapshot.ExternalID != *attempt.ExternalTicketID {
		return &Result{Outcome: OutcomeReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
			ExternalTicketID: *attempt.ExternalTicketID, Provider: *attempt.Provider}, nil
	}
	return s.syncProjection(ctx, attempt, localTicket, snapshot)
}

// syncProjection enriches the local ticket from a CONFIRMED_SUCCESS
// attempt's durable external identity plus a fresh provider snapshot
// (normal create: CreateTicket's own response; recovery: a GetTicket
// read-back, PRODUCT.6-M4), and marks the attempt's projection synced.
// Idempotent and safe to call repeatedly.
func (s *Service) syncProjection(ctx context.Context, attempt *ticketsdomain.ExternalCreateAttempt, localTicket *ticketsdomain.Ticket, snapshot *connectors.ExternalTicket) (*Result, error) {
	if attempt.Provider == nil || attempt.ExternalTicketID == nil {
		return nil, fmt.Errorf("tickets: confirmed_success attempt %s missing provider/external_ticket_id", attempt.ID)
	}
	if err := s.localTickets.EnrichExternalProjection(ctx, localTicket.ID, *attempt.Provider, *attempt.ExternalTicketID, snapshot.ExternalStatus, snapshot.ExternalStatusLabel, time.Now().UTC()); err != nil {
		// Section 11: provider success remains durable on the attempt row
		// even though local enrichment failed. Never retries the
		// provider; a later replay retries only this projection step.
		return &Result{Outcome: OutcomeReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
			ExternalTicketID: *attempt.ExternalTicketID, Provider: *attempt.Provider, LocalTicketID: localTicket.ID}, nil
	}
	synced, err := s.attempts.MarkProjectionSynced(ctx, attempt.ID, localTicket.ID)
	if err != nil {
		// The local ticket IS correctly enriched at this point; only the
		// attempt's own bookkeeping failed to persist. Self-healing: a
		// future replay re-runs EnrichExternalProjection (idempotent,
		// no-op) and retries MarkProjectionSynced.
		return &Result{Outcome: OutcomeReconciliationRequired, AttemptID: attempt.ID, AttemptState: attempt.State,
			ExternalTicketID: *attempt.ExternalTicketID, Provider: *attempt.Provider, LocalTicketID: localTicket.ID}, nil
	}
	return &Result{Outcome: OutcomeReplaySuccess, AttemptID: synced.ID, AttemptState: synced.State,
		ExternalTicketID: *attempt.ExternalTicketID, Provider: *attempt.Provider, LocalTicketID: localTicket.ID}, nil
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

func derefUUIDOr(u *uuid.UUID, fallback uuid.UUID) uuid.UUID {
	if u == nil {
		return fallback
	}
	return *u
}
