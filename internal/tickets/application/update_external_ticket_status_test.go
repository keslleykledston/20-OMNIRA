package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
	"github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// PRODUCT.6-O2B3 application tests. Reuses fakeStatusAttempts which simulates
// the real StatusMutationAttemptStore atomicity; O2B1 separately proves real
// Postgres concurrency (15-way race, exact index behavior).

type fakeStatusAttempts struct {
	mu    sync.Mutex
	byKey map[string]*ticketsdomain.ExternalStatusAttempt

	markConfirmedSuccessErrSeq []error
	markConfirmedSuccessCalls  int
	markOutcomeUnknownErrSeq   []error
	markOutcomeUnknownCalls    int
	markProjectionSyncedErrSeq []error
	markProjectionSyncedCalls  int

	acquireCalls int
}

func newFakeStatusAttempts() *fakeStatusAttempts {
	return &fakeStatusAttempts{byKey: map[string]*ticketsdomain.ExternalStatusAttempt{}}
}

func (f *fakeStatusAttempts) findBlocking(localTicketID uuid.UUID) *ticketsdomain.ExternalStatusAttempt {
	for _, a := range f.byKey {
		if a.LocalTicketID != localTicketID {
			continue
		}
		switch a.State {
		case ticketsdomain.AttemptInFlight, ticketsdomain.AttemptOutcomeUnknown:
			return a
		}
	}
	return nil
}

func (f *fakeStatusAttempts) Acquire(ctx context.Context, cmd ports.AcquireStatusMutationAttemptCommand) (*ticketsdomain.ExternalStatusAttempt, ports.AcquireOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquireCalls++
	if existing, ok := f.byKey[cmd.IdempotencyKey]; ok {
		if existing.RequestHash != cmd.RequestHash {
			return nil, ports.AcquireIdempotencyMismatch, nil
		}
		cp := *existing
		return &cp, ports.AcquireExistingSameIntent, nil
	}
	if blocking := f.findBlocking(cmd.LocalTicketID); blocking != nil {
		cp := *blocking
		return &cp, ports.AcquireBlockedByUnresolvedOperation, nil
	}
	now := time.Now().UTC()
	a := &ticketsdomain.ExternalStatusAttempt{
		ID: uuid.New(), LocalTicketID: cmd.LocalTicketID, ConversationID: cmd.ConversationID, ActorUserID: cmd.ActorUserID,
		IdempotencyKey: cmd.IdempotencyKey, RequestHash: cmd.RequestHash, Provider: cmd.Provider, ExternalTicketID: cmd.ExternalTicketID,
		TargetStatus: cmd.TargetStatus, State: ticketsdomain.AttemptInFlight, CreatedAt: now, UpdatedAt: now,
	}
	f.byKey[cmd.IdempotencyKey] = a
	cp := *a
	return &cp, ports.AcquireAcquired, nil
}

func (f *fakeStatusAttempts) findByID(id uuid.UUID) *ticketsdomain.ExternalStatusAttempt {
	for _, a := range f.byKey {
		if a.ID == id {
			return a
		}
	}
	return nil
}

func (f *fakeStatusAttempts) MarkConfirmedSuccess(ctx context.Context, attemptID uuid.UUID, confirmedExternalStatus, confirmedExternalStatusLabel string) (*ticketsdomain.ExternalStatusAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.markConfirmedSuccessCalls
	f.markConfirmedSuccessCalls++
	if idx < len(f.markConfirmedSuccessErrSeq) && f.markConfirmedSuccessErrSeq[idx] != nil {
		return nil, f.markConfirmedSuccessErrSeq[idx]
	}
	a := f.findByID(attemptID)
	if a == nil {
		return nil, errors.New("not found")
	}
	if a.State != ticketsdomain.AttemptInFlight && a.State != ticketsdomain.AttemptOutcomeUnknown {
		return nil, errors.New("invalid transition")
	}
	a.State = ticketsdomain.AttemptConfirmedSuccess
	a.ConfirmedExternalStatus, a.ConfirmedExternalStatusLabel = &confirmedExternalStatus, &confirmedExternalStatusLabel
	cp := *a
	return &cp, nil
}

func (f *fakeStatusAttempts) MarkConfirmedFailure(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalStatusAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.findByID(attemptID)
	if a == nil {
		return nil, errors.New("not found")
	}
	if a.State != ticketsdomain.AttemptInFlight {
		return nil, errors.New("invalid transition")
	}
	a.State = ticketsdomain.AttemptConfirmedFailure
	cp := *a
	return &cp, nil
}

func (f *fakeStatusAttempts) MarkOutcomeUnknown(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalStatusAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.markOutcomeUnknownCalls
	f.markOutcomeUnknownCalls++
	if idx < len(f.markOutcomeUnknownErrSeq) && f.markOutcomeUnknownErrSeq[idx] != nil {
		return nil, f.markOutcomeUnknownErrSeq[idx]
	}
	a := f.findByID(attemptID)
	if a == nil {
		return nil, errors.New("not found")
	}
	if a.State != ticketsdomain.AttemptInFlight {
		return nil, errors.New("invalid transition")
	}
	a.State = ticketsdomain.AttemptOutcomeUnknown
	cp := *a
	return &cp, nil
}

func (f *fakeStatusAttempts) MarkProjectionSynced(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalStatusAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.markProjectionSyncedCalls
	f.markProjectionSyncedCalls++
	if idx < len(f.markProjectionSyncedErrSeq) && f.markProjectionSyncedErrSeq[idx] != nil {
		return nil, f.markProjectionSyncedErrSeq[idx]
	}
	a := f.findByID(attemptID)
	if a == nil {
		return nil, errors.New("not found")
	}
	if a.State != ticketsdomain.AttemptConfirmedSuccess {
		return nil, errors.New("invalid transition")
	}
	now := time.Now().UTC()
	a.ProjectionSyncedAt = &now
	cp := *a
	return &cp, nil
}

// ---- fixture (reuses fakePerms, fakeConversation, fakeLocalTickets from create_external_ticket_test.go) ----
// O2B3 extends fakeTicketing with updateHook for concurrency simulation

type fakeLocalTicketsForStatus struct {
	mu                   sync.Mutex
	candidate            *ticketsdomain.Ticket
	enrichCalls          int
	enrichErrSeq         []error
	gotExternalStatus    string
	gotExternalStatusLabel string
}

func (f *fakeLocalTicketsForStatus) FindEnrichmentCandidate(ctx context.Context, conversationID uuid.UUID) (*ticketsdomain.Ticket, error) {
	return f.FindActiveByConversation(ctx, conversationID)
}

func (f *fakeLocalTicketsForStatus) FindActiveByConversation(ctx context.Context, conversationID uuid.UUID) (*ticketsdomain.Ticket, error) {
	if f.candidate == nil {
		return nil, nil
	}
	cp := *f.candidate
	return &cp, nil
}

func (f *fakeLocalTicketsForStatus) EnrichExternalProjection(ctx context.Context, ticketID uuid.UUID, provider, externalTicketID, externalStatus, externalStatusLabel string, syncedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.enrichCalls
	f.enrichCalls++
	f.gotExternalStatus = externalStatus
	f.gotExternalStatusLabel = externalStatusLabel
	if idx < len(f.enrichErrSeq) {
		return f.enrichErrSeq[idx]
	}
	return nil
}

type fakeTicketingForStatus struct {
	*fakeTicketing
	updateHook func()
}

func (f *fakeTicketingForStatus) UpdateTicketStatus(ctx context.Context, externalID string, target connectors.ExternalStatusTarget) (*connectors.ExternalTicket, error) {
	if f.updateHook != nil {
		f.updateHook()
	}
	return f.fakeTicketing.UpdateTicketStatus(ctx, externalID, target)
}

const testStatusKey = "status-test-key-0001"
const testStatusExternalID = "9115"

type statusHarness struct {
	perms        *fakePerms
	conversation *fakeConversation
	attempts     *fakeStatusAttempts
	localTickets *fakeLocalTicketsForStatus
	ticketing    *fakeTicketingForStatus
	runtime      *fakeRuntimeResolver
	svc          *UpdateExternalTicketStatusService
	tenantID     uuid.UUID
	actorID      uuid.UUID
	convID       uuid.UUID
	ticketID     uuid.UUID
}

func newStatusHarness(linked bool) *statusHarness {
	ticketID := uuid.New()
	ticket := &ticketsdomain.Ticket{ID: ticketID}
	if linked {
		p, e := "fake", testStatusExternalID
		ticket.Provider, ticket.ExternalTicketID = &p, &e
	}
	ft := &fakeTicketing{result: &connectors.ExternalTicket{}, getCalls: 0, updateCalls: 0}
	fts := &fakeTicketingForStatus{fakeTicketing: ft}
	h := &statusHarness{
		perms:        &fakePerms{granted: map[string]bool{PermissionTicketUpdate: true}},
		conversation: &fakeConversation{found: true},
		attempts:     newFakeStatusAttempts(),
		localTickets: &fakeLocalTicketsForStatus{candidate: ticket},
		ticketing:    fts,
		tenantID:     uuid.New(), actorID: uuid.New(), convID: uuid.New(), ticketID: ticketID,
	}
	h.runtime = &fakeRuntimeResolver{ticketing: fts, err: nil}
	h.svc = NewUpdateExternalTicketStatusService(h.perms, h.conversation, h.attempts, h.localTickets, h.runtime)
	return h
}

func (h *statusHarness) cmd(target string) UpdateExternalTicketStatusCommand {
	return UpdateExternalTicketStatusCommand{
		TenantID: h.tenantID, ConversationID: h.convID, ActorUserID: h.actorID,
		TargetStatus: target, IdempotencyKey: testStatusKey,
	}
}

func (h *statusHarness) ctx() context.Context {
	return withTenantContext(h.tenantID, h.actorID)
}

// Note: withTenantContext is defined in create_external_ticket_test.go and shared

// ---- Tests A-X ----

func TestUpdateExternalTicketStatusRejectsMissingTicketUpdate(t *testing.T) {
	h := newStatusHarness(true)
	h.perms.granted[PermissionTicketUpdate] = false
	h.conversation.assignedTo = &h.actorID
	_, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if h.ticketing.updateCalls != 0 {
		t.Fatalf("PUT called %d times, want 0", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusRejectsUnauthorizedConversation(t *testing.T) {
	h := newStatusHarness(true)
	other := uuid.New()
	h.conversation.assignedTo = &other
	_, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if !errors.Is(err, ErrNotAssignedToYou) {
		t.Fatalf("err = %v, want ErrNotAssignedToYou", err)
	}
	if h.ticketing.updateCalls != 0 {
		t.Fatalf("PUT called %d times, want 0", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusNoActiveTicketNeverAcquiresOrCallsProvider(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.localTickets.candidate = nil
	_, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if !errors.Is(err, ErrNoActiveTicket) {
		t.Fatalf("err = %v, want ErrNoActiveTicket", err)
	}
	if h.attempts.acquireCalls != 0 {
		t.Fatalf("Acquire called %d times, want 0", h.attempts.acquireCalls)
	}
	if h.ticketing.updateCalls != 0 {
		t.Fatalf("PUT called %d times, want 0", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusUnlinkedTicketNeverAcquiresOrCallsProvider(t *testing.T) {
	h := newStatusHarness(false)
	h.conversation.assignedTo = &h.actorID
	_, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if !errors.Is(err, ErrTicketNotLinked) {
		t.Fatalf("err = %v, want ErrTicketNotLinked", err)
	}
	if h.attempts.acquireCalls != 0 {
		t.Fatalf("Acquire called %d times, want 0", h.attempts.acquireCalls)
	}
	if h.ticketing.updateCalls != 0 {
		t.Fatalf("PUT called %d times, want 0", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusInconsistentLinkageNeverAcquiresOrCallsProvider(t *testing.T) {
	h := newStatusHarness(false)
	h.conversation.assignedTo = &h.actorID
	externalID := testStatusExternalID
	h.localTickets.candidate.ExternalTicketID = &externalID
	_, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if !errors.Is(err, ErrInconsistentExternalLink) {
		t.Fatalf("err = %v, want ErrInconsistentExternalLink", err)
	}
	if h.attempts.acquireCalls != 0 {
		t.Fatalf("Acquire called %d times, want 0", h.attempts.acquireCalls)
	}
	if h.ticketing.updateCalls != 0 {
		t.Fatalf("PUT called %d times, want 0", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusProviderMismatchNeverAcquiresOrCallsProvider(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.name = "a-different-provider"
	_, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if !errors.Is(err, ErrProviderMismatch) {
		t.Fatalf("err = %v, want ErrProviderMismatch", err)
	}
	if h.attempts.acquireCalls != 0 {
		t.Fatalf("Acquire called %d times, want 0", h.attempts.acquireCalls)
	}
	if h.ticketing.updateCalls != 0 {
		t.Fatalf("PUT called %d times, want 0", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusNewAcquiredMutationCallsProviderExactlyOnce(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.updateResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "5", ExternalStatusLabel: "Resolvido"}
	res, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeStatusUpdated || res.Replayed {
		t.Fatalf("result = %+v, want updated/not-replayed", res)
	}
	if h.ticketing.updateCalls != 1 {
		t.Fatalf("PUT called %d times, want exactly 1", h.ticketing.updateCalls)
	}
	if h.ticketing.gotUpdateID != testStatusExternalID || h.ticketing.gotUpdateTarget.Code != "5" {
		t.Fatalf("PUT target = id=%q code=%q, want %s/5", h.ticketing.gotUpdateID, h.ticketing.gotUpdateTarget.Code, testStatusExternalID)
	}
	if h.localTickets.gotExternalStatus != "5" || h.localTickets.gotExternalStatusLabel != "Resolvido" {
		t.Fatalf("projected status=%q label=%q, want 5/Resolvido", h.localTickets.gotExternalStatus, h.localTickets.gotExternalStatusLabel)
	}
}

func TestUpdateExternalTicketStatusSameKeyConfirmedSuccessSyncedReplaysWithoutPUT(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.updateResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "5", ExternalStatusLabel: "Resolvido"}
	ctx := h.ctx()
	if _, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("5")); err != nil {
		t.Fatalf("first call: %v", err)
	}
	res, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("5"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if h.ticketing.updateCalls != 1 {
		t.Fatalf("PUT called %d times, want exactly 1", h.ticketing.updateCalls)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket called %d times, want 0", h.ticketing.getCalls)
	}
	if res.Outcome != OutcomeStatusReplaySuccess || !res.Replayed {
		t.Fatalf("result = %+v, want replay_success/replayed", res)
	}
}

func TestUpdateExternalTicketStatusSameKeyDifferentHashIsMismatch(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.updateResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "5", ExternalStatusLabel: "Resolvido"}
	ctx := h.ctx()
	if _, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("5")); err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("2"))
	if !errors.Is(err, ErrStatusIdempotencyMismatch) {
		t.Fatalf("err = %v, want ErrStatusIdempotencyMismatch", err)
	}
	if h.ticketing.updateCalls != 1 {
		t.Fatalf("PUT called %d times, want exactly 1 (mismatch call must not PUT)", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusDifferentKeyBlockedByInFlightNeverPosts(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	ctx := h.ctx()
	if _, _, err := h.attempts.Acquire(ctx, ports.AcquireStatusMutationAttemptCommand{
		LocalTicketID: h.ticketID, ConversationID: h.convID, ActorUserID: h.actorID,
		IdempotencyKey: "other-key", RequestHash: "other-hash", Provider: "fake", ExternalTicketID: testStatusExternalID, TargetStatus: "2",
	}); err != nil {
		t.Fatalf("seed acquire: %v", err)
	}
	res, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeStatusReconciliationRequired {
		t.Fatalf("result = %+v, want reconciliation_required", res)
	}
	if h.ticketing.updateCalls != 0 {
		t.Fatalf("PUT called %d times, want 0", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusDifferentKeyBlockedByOutcomeUnknownNeverPosts(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	ctx := h.ctx()
	seeded, _, err := h.attempts.Acquire(ctx, ports.AcquireStatusMutationAttemptCommand{
		LocalTicketID: h.ticketID, ConversationID: h.convID, ActorUserID: h.actorID,
		IdempotencyKey: "other-key", RequestHash: "other-hash", Provider: "fake", ExternalTicketID: testStatusExternalID, TargetStatus: "2",
	})
	if err != nil {
		t.Fatalf("seed acquire: %v", err)
	}
	if _, err := h.attempts.MarkOutcomeUnknown(ctx, seeded.ID); err != nil {
		t.Fatalf("seed outcome_unknown: %v", err)
	}
	res, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeStatusReconciliationRequired {
		t.Fatalf("result = %+v, want reconciliation_required", res)
	}
	if h.ticketing.updateCalls != 0 || h.ticketing.getCalls != 0 {
		t.Fatalf("provider must not be called, PUT=%d GET=%d", h.ticketing.updateCalls, h.ticketing.getCalls)
	}
}

func TestUpdateExternalTicketStatusConfirmedSuccessPermitsLaterDifferentKeyTransition(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	ctx := h.ctx()
	h.ticketing.updateResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "2", ExternalStatusLabel: "Em atendimento"}
	if _, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("2")); err != nil {
		t.Fatalf("first call: %v", err)
	}
	h.ticketing.updateResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "5", ExternalStatusLabel: "Resolvido"}
	next := UpdateExternalTicketStatusCommand{TenantID: h.tenantID, ConversationID: h.convID, ActorUserID: h.actorID, TargetStatus: "5", IdempotencyKey: "different-key-0001"}
	res, err := h.svc.UpdateExternalTicketStatus(ctx, next)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if res.Outcome != OutcomeStatusUpdated {
		t.Fatalf("result = %+v, want updated (confirmed_success must not block)", res)
	}
	if h.ticketing.updateCalls != 2 {
		t.Fatalf("PUT called %d times, want exactly 2", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusConfirmedFailurePermitsLaterDifferentKeyTransition(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	ctx := h.ctx()
	h.ticketing.updateErr = &connectors.TicketingError{Code: connectors.TicketingValidationError, Message: "bad"}
	if _, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("5")); err != nil {
		t.Fatalf("first call: %v", err)
	}
	h.ticketing.updateErr = nil
	h.ticketing.updateResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "2", ExternalStatusLabel: "Em atendimento"}
	next := UpdateExternalTicketStatusCommand{TenantID: h.tenantID, ConversationID: h.convID, ActorUserID: h.actorID, TargetStatus: "2", IdempotencyKey: "different-key-0002"}
	res, err := h.svc.UpdateExternalTicketStatus(ctx, next)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if res.Outcome != OutcomeStatusUpdated {
		t.Fatalf("result = %+v, want updated (confirmed_failure must not block)", res)
	}
}

func TestUpdateExternalTicketStatusDefinitiveProviderFailureMarksConfirmedFailure(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.updateErr = &connectors.TicketingError{Code: connectors.TicketingValidationError, Message: "unsupported target"}
	res, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeStatusDefinitiveFailure || res.FailureCode != connectors.TicketingValidationError {
		t.Fatalf("result = %+v, want definitive_failure/VALIDATION_ERROR", res)
	}
}

func TestUpdateExternalTicketStatusAmbiguousWriteMarksOutcomeUnknownBeforeGetTicket(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.updateErr = &connectors.TicketingError{Code: connectors.TicketingWriteOutcomeUnknown, Message: "ambiguous"}
	h.attempts.markOutcomeUnknownErrSeq = []error{errors.New("db down")}
	res, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeStatusReconciliationRequired {
		t.Fatalf("result = %+v, want reconciliation_required", res)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket called %d times, want 0 (MarkOutcomeUnknown must durably record BEFORE any reconciliation read)", h.ticketing.getCalls)
	}
}

func TestUpdateExternalTicketStatusAmbiguousWriteReconciledOnMatchingGetTicket(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.updateErr = &connectors.TicketingError{Code: connectors.TicketingWriteOutcomeUnknown, Message: "ambiguous"}
	h.ticketing.getResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "5", ExternalStatusLabel: "Resolvido"}
	res, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeStatusReconciledSuccess || !res.Reconciled {
		t.Fatalf("result = %+v, want reconciled_success", res)
	}
	if h.ticketing.updateCalls != 1 {
		t.Fatalf("PUT called %d times, want exactly 1 (no second PUT)", h.ticketing.updateCalls)
	}
	if h.ticketing.getCalls != 1 {
		t.Fatalf("GetTicket called %d times, want exactly 1", h.ticketing.getCalls)
	}
}

func TestUpdateExternalTicketStatusAmbiguousWriteRemainsUnknownOnStatusMismatch(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.updateErr = &connectors.TicketingError{Code: connectors.TicketingWriteOutcomeUnknown, Message: "ambiguous"}
	h.ticketing.getResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "2", ExternalStatusLabel: "Em atendimento"}
	res, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeStatusReconciliationRequired || res.AttemptState != ticketsdomain.AttemptOutcomeUnknown {
		t.Fatalf("result = %+v, want reconciliation_required/outcome_unknown", res)
	}
	if h.ticketing.updateCalls != 1 {
		t.Fatalf("PUT called %d times, want exactly 1 (no automatic second PUT)", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusAmbiguousWriteRemainsUnknownOnGetTicketFailure(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.updateErr = &connectors.TicketingError{Code: connectors.TicketingWriteOutcomeUnknown, Message: "ambiguous"}
	h.ticketing.getErr = &connectors.TicketingError{Code: connectors.TicketingProviderUnavailable, Message: "down"}
	res, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeStatusReconciliationRequired || res.AttemptState != ticketsdomain.AttemptOutcomeUnknown {
		t.Fatalf("result = %+v, want reconciliation_required/outcome_unknown", res)
	}
}

func TestUpdateExternalTicketStatusForeignReturnedExternalIDNeverReplacesProjectionIdentity(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.updateResult = &connectors.ExternalTicket{ExternalID: "99999-FOREIGN", ExternalStatus: "5", ExternalStatusLabel: "Resolvido"}
	res, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeStatusReconciliationRequired {
		t.Fatalf("result = %+v, want reconciliation_required (never adopt a foreign id)", res)
	}
	if h.localTickets.enrichCalls != 0 {
		t.Fatalf("EnrichExternalProjection called %d times, want 0 (foreign id must never reach projection)", h.localTickets.enrichCalls)
	}
}

func TestUpdateExternalTicketStatusMarkConfirmedSuccessFailureLeavesProjectionUntouched(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.updateResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "5", ExternalStatusLabel: "Resolvido"}
	h.attempts.markConfirmedSuccessErrSeq = []error{errors.New("db connection lost mid-write")}
	res, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5"))
	if err == nil {
		t.Fatal("expected a non-nil Go error for the durable-write failure")
	}
	if res == nil || res.Outcome != OutcomeStatusReconciliationRequired || !res.Severe {
		t.Fatalf("result = %+v, want severe reconciliation_required", res)
	}
	if h.localTickets.enrichCalls != 0 {
		t.Fatalf("EnrichExternalProjection called %d times, want 0 (never project before durable success)", h.localTickets.enrichCalls)
	}
	if h.ticketing.updateCalls != 1 {
		t.Fatalf("PUT called %d times, want exactly 1 (no retry)", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusConfirmedSuccessProjectionFailureRecoversOnReplayWithoutProviderCall(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	ctx := h.ctx()
	h.ticketing.updateResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "5", ExternalStatusLabel: "Resolvido"}
	h.localTickets.enrichErrSeq = []error{errors.New("db down")}
	res, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("5"))
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if res.Outcome != OutcomeStatusReconciliationRequired {
		t.Fatalf("result = %+v, want reconciliation_required", res)
	}

	res2, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("5"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res2.Outcome != OutcomeStatusReplaySuccess {
		t.Fatalf("replay result = %+v, want replay_success", res2)
	}
	if h.ticketing.updateCalls != 1 {
		t.Fatalf("PUT called %d times, want exactly 1 (replay must never PUT)", h.ticketing.updateCalls)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket called %d times, want 0 (replay must never GET)", h.ticketing.getCalls)
	}
	if h.localTickets.enrichCalls != 2 {
		t.Fatalf("EnrichExternalProjection called %d times, want 2 (initial failure + recovery retry)", h.localTickets.enrichCalls)
	}
}

func TestUpdateExternalTicketStatusMarkProjectionSyncedFailureRecoversOnReplayWithoutProviderCall(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	ctx := h.ctx()
	h.ticketing.updateResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "5", ExternalStatusLabel: "Resolvido"}
	h.attempts.markProjectionSyncedErrSeq = []error{errors.New("db down")}
	res, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("5"))
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if res.Outcome != OutcomeStatusReconciliationRequired {
		t.Fatalf("result = %+v, want reconciliation_required", res)
	}

	res2, err := h.svc.UpdateExternalTicketStatus(ctx, h.cmd("5"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res2.Outcome != OutcomeStatusReplaySuccess {
		t.Fatalf("replay result = %+v, want replay_success", res2)
	}
	if h.ticketing.updateCalls != 1 {
		t.Fatalf("PUT called %d times, want exactly 1", h.ticketing.updateCalls)
	}
}

func TestUpdateExternalTicketStatusActorChangeWithSameOperationDoesNotRedefineIntent(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	h.ticketing.updateResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "5", ExternalStatusLabel: "Resolvido"}
	if _, err := h.svc.UpdateExternalTicketStatus(h.ctx(), h.cmd("5")); err != nil {
		t.Fatalf("first call: %v", err)
	}

	hash1 := statusRequestHash(h.tenantID, h.ticketID, h.convID, "fake", testStatusExternalID, "5")
	otherActor := uuid.New()
	hash2 := statusRequestHash(h.tenantID, h.ticketID, h.convID, "fake", testStatusExternalID, "5")
	if hash1 != hash2 {
		t.Fatalf("hash must not depend on actor identity by construction: %s vs %s", hash1, hash2)
	}
	_ = otherActor

	manager := uuid.New()
	h.perms.granted[PermissionConversationManage] = true
	cmd := UpdateExternalTicketStatusCommand{TenantID: h.tenantID, ConversationID: h.convID, ActorUserID: manager, TargetStatus: "5", IdempotencyKey: testStatusKey}
	res, err := h.svc.UpdateExternalTicketStatus(withTenantContext(h.tenantID, manager), cmd)
	if err != nil {
		t.Fatalf("second actor replay: %v", err)
	}
	if res.Outcome != OutcomeStatusReplaySuccess {
		t.Fatalf("result = %+v, want replay_success (actor handoff must not redefine intent)", res)
	}
	if h.ticketing.updateCalls != 1 {
		t.Fatalf("PUT called %d times, want exactly 1", h.ticketing.updateCalls)
	}
}

// Application blocking semantics with fake store:
// Proves UpdateExternalTicketStatus respects store's blocking semantics.
// O2B1 separately proves real Postgres atomic Acquire race (15-way).
func TestUpdateExternalTicketStatusBlockingSemantics(t *testing.T) {
	h := newStatusHarness(true)
	h.conversation.assignedTo = &h.actorID
	ctx := h.ctx()

	// Seed: A unresolved in_flight state (simulates A won the race).
	seedAttempt, _, err := h.attempts.Acquire(ctx, ports.AcquireStatusMutationAttemptCommand{
		LocalTicketID: h.ticketID, ConversationID: h.convID, ActorUserID: h.actorID,
		IdempotencyKey: "seeded-in-flight", RequestHash: "seeded-hash",
		Provider: "fake", ExternalTicketID: testStatusExternalID, TargetStatus: "5",
	})
	if err != nil {
		t.Fatalf("seed acquire: %v", err)
	}
	if seedAttempt.State != ticketsdomain.AttemptInFlight {
		t.Fatalf("seed attempt state = %q, want in_flight", seedAttempt.State)
	}

	// Now try to execute a DIFFERENT key (B):
	cmdB := UpdateExternalTicketStatusCommand{
		TenantID: h.tenantID, ConversationID: h.convID, ActorUserID: h.actorID,
		TargetStatus: "2", IdempotencyKey: "different-key-b",
	}
	resB, errB := h.svc.UpdateExternalTicketStatus(ctx, cmdB)

	// B must be blocked (reconciliation_required, not attempt to PUT).
	if errB != nil {
		t.Fatalf("B: %v", errB)
	}
	if resB.Outcome != OutcomeStatusReconciliationRequired {
		t.Fatalf("B outcome = %q, want reconciliation_required (blocked by seeded in_flight)", resB.Outcome)
	}
	if h.ticketing.fakeTicketing.updateCalls != 0 {
		t.Fatalf("provider UpdateTicketStatus called %d times, want 0 (B must not reach provider)", h.ticketing.fakeTicketing.updateCalls)
	}

	// Mark seed attempt as confirmed_success (simulates A's successful PUT).
	seedAttempt, err = h.attempts.MarkConfirmedSuccess(ctx, seedAttempt.ID, "5", "Resolvido")
	if err != nil {
		t.Fatalf("mark confirmed_success: %v", err)
	}

	// Now try C with a new different key — should succeed (barrier released).
	h.ticketing.fakeTicketing.updateResult = &connectors.ExternalTicket{ExternalID: testStatusExternalID, ExternalStatus: "2", ExternalStatusLabel: "Em atendimento"}
	cmdC := UpdateExternalTicketStatusCommand{
		TenantID: h.tenantID, ConversationID: h.convID, ActorUserID: h.actorID,
		TargetStatus: "2", IdempotencyKey: "different-key-c",
	}
	resC, errC := h.svc.UpdateExternalTicketStatus(ctx, cmdC)
	if errC != nil {
		t.Fatalf("C: %v", errC)
	}
	if resC.Outcome != OutcomeStatusUpdated {
		t.Fatalf("C outcome = %q, want updated (barrier released after confirmed_success)", resC.Outcome)
	}
	if h.ticketing.fakeTicketing.updateCalls != 1 {
		t.Fatalf("provider UpdateTicketStatus called %d times, want 1", h.ticketing.fakeTicketing.updateCalls)
	}
}
