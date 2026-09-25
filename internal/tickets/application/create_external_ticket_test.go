package application

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
	"github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// ---- fakes ------------------------------------------------------------

type fakePerms struct {
	granted map[string]bool // permission -> granted, same actor for all test cases
}

func (f *fakePerms) HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error) {
	return f.granted[permission], nil
}

type fakeConversation struct {
	found      bool
	assignedTo *uuid.UUID
	err        error
}

func (f *fakeConversation) LoadAssignment(ctx context.Context, conversationID uuid.UUID) (*uuid.UUID, bool, error) {
	return f.assignedTo, f.found, f.err
}

type fakeCompanies struct {
	companies []ports.Company
	err       error
}

func (f *fakeCompanies) ListCompanies(ctx context.Context) ([]ports.Company, error) {
	return f.companies, f.err
}

// fakeAttempts mirrors the guarded-transition semantics of
// internal/tickets/adapters.AttemptStore closely enough to drive
// application-level orchestration tests. Real DB concurrency/RLS proof
// already lives in PRODUCT.6-K1's real-Postgres tests; this fake proves the
// SERVICE's orchestration is correct given those documented semantics.
type fakeAttempts struct {
	mu    sync.Mutex
	byKey map[string]*ticketsdomain.ExternalCreateAttempt
	// markConfirmedSuccessErrSeq lets a test inject a durable-write
	// failure on MarkConfirmedSuccess without touching production code —
	// consumed in call order, then nil forever.
	markConfirmedSuccessErrSeq []error
	markConfirmedSuccessCalls  int
}

func newFakeAttempts() *fakeAttempts {
	return &fakeAttempts{byKey: map[string]*ticketsdomain.ExternalCreateAttempt{}}
}

func (f *fakeAttempts) Acquire(ctx context.Context, conversationID, localTicketID, actorUserID uuid.UUID, idempotencyKey, requestHash string) (*ticketsdomain.ExternalCreateAttempt, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.byKey[idempotencyKey]; ok {
		if existing.RequestHash != requestHash {
			return nil, false, ports.ErrIdempotencyMismatch
		}
		cp := *existing
		return &cp, false, nil
	}
	if blocking := f.findBlockingByLocalTicket(localTicketID); blocking != nil {
		cp := *blocking
		return &cp, false, ports.ErrLocalTicketBlocked
	}
	a := &ticketsdomain.ExternalCreateAttempt{
		ID: uuid.New(), ConversationID: conversationID, ActorUserID: actorUserID, LocalTicketID: &localTicketID,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash, State: ticketsdomain.AttemptInFlight,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	f.byKey[idempotencyKey] = a
	cp := *a
	return &cp, true, nil
}

// findBlockingByLocalTicket mirrors the real AttemptStore's partial unique
// index (migration 000048, PRODUCT.6-M5): at most one attempt in a
// blocking state may exist per local ticket, across ALL idempotency keys.
func (f *fakeAttempts) findBlockingByLocalTicket(localTicketID uuid.UUID) *ticketsdomain.ExternalCreateAttempt {
	for _, a := range f.byKey {
		if a.LocalTicketID == nil || *a.LocalTicketID != localTicketID {
			continue
		}
		switch a.State {
		case ticketsdomain.AttemptInFlight, ticketsdomain.AttemptConfirmedSuccess, ticketsdomain.AttemptOutcomeUnknown:
			return a
		}
	}
	return nil
}

func (f *fakeAttempts) MarkConfirmedSuccess(ctx context.Context, attemptID uuid.UUID, provider, externalTicketID string) (*ticketsdomain.ExternalCreateAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.markConfirmedSuccessCalls
	f.markConfirmedSuccessCalls++
	if idx < len(f.markConfirmedSuccessErrSeq) && f.markConfirmedSuccessErrSeq[idx] != nil {
		// Mirrors the real durable-write failure mode: the guarded UPDATE
		// itself failed, so the row is left exactly as it was (in_flight)
		// — never partially transitioned.
		return nil, f.markConfirmedSuccessErrSeq[idx]
	}
	a := f.findByID(attemptID)
	if a == nil {
		return nil, errors.New("not found")
	}
	if a.State != ticketsdomain.AttemptInFlight {
		return nil, errors.New("invalid transition")
	}
	a.State = ticketsdomain.AttemptConfirmedSuccess
	a.Provider, a.ExternalTicketID = &provider, &externalTicketID
	cp := *a
	return &cp, nil
}

func (f *fakeAttempts) MarkConfirmedFailure(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalCreateAttempt, error) {
	return f.markTerminal(attemptID, ticketsdomain.AttemptConfirmedFailure)
}

func (f *fakeAttempts) MarkOutcomeUnknown(ctx context.Context, attemptID uuid.UUID) (*ticketsdomain.ExternalCreateAttempt, error) {
	return f.markTerminal(attemptID, ticketsdomain.AttemptOutcomeUnknown)
}

func (f *fakeAttempts) markTerminal(attemptID uuid.UUID, target ticketsdomain.AttemptState) (*ticketsdomain.ExternalCreateAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.findByID(attemptID)
	if a == nil {
		return nil, errors.New("not found")
	}
	if a.State != ticketsdomain.AttemptInFlight {
		return nil, errors.New("invalid transition")
	}
	a.State = target
	cp := *a
	return &cp, nil
}

func (f *fakeAttempts) MarkProjectionSynced(ctx context.Context, attemptID, localTicketID uuid.UUID) (*ticketsdomain.ExternalCreateAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.findByID(attemptID)
	if a == nil {
		return nil, errors.New("not found")
	}
	if a.State != ticketsdomain.AttemptConfirmedSuccess {
		return nil, errors.New("invalid transition")
	}
	now := time.Now().UTC()
	a.LocalTicketID, a.ProjectionSyncedAt = &localTicketID, &now
	cp := *a
	return &cp, nil
}

func (f *fakeAttempts) findByID(id uuid.UUID) *ticketsdomain.ExternalCreateAttempt {
	for _, a := range f.byKey {
		if a.ID == id {
			return a
		}
	}
	return nil
}

type fakeLocalTickets struct {
	mu           sync.Mutex
	candidate    *ticketsdomain.Ticket
	enrichCalls  int32
	enrichErrSeq []error // consumed in order, then nil forever

	gotProvider, gotExternalTicketID, gotExternalStatus, gotExternalStatusLabel string
}

func (f *fakeLocalTickets) FindEnrichmentCandidate(ctx context.Context, conversationID uuid.UUID) (*ticketsdomain.Ticket, error) {
	if f.candidate == nil {
		return nil, nil
	}
	cp := *f.candidate
	return &cp, nil
}

func (f *fakeLocalTickets) FindActiveByConversation(ctx context.Context, conversationID uuid.UUID) (*ticketsdomain.Ticket, error) {
	return f.FindEnrichmentCandidate(ctx, conversationID)
}

func (f *fakeLocalTickets) EnrichExternalProjection(ctx context.Context, ticketID uuid.UUID, provider, externalTicketID, externalStatus, externalStatusLabel string, syncedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := int(atomic.AddInt32(&f.enrichCalls, 1)) - 1
	f.gotProvider, f.gotExternalTicketID, f.gotExternalStatus, f.gotExternalStatusLabel = provider, externalTicketID, externalStatus, externalStatusLabel
	if idx < len(f.enrichErrSeq) {
		return f.enrichErrSeq[idx]
	}
	return nil
}

type fakeTicketing struct {
	name string // defaults to "fake" (see Name()) when empty

	calls  int32
	result *connectors.ExternalTicket
	err    error
	gotReq connectors.CreateTicketRequest

	getCalls  int32
	getResult *connectors.ExternalTicket
	getErr    error
	gotGetID  string

	updateCalls     int32
	updateResult    *connectors.ExternalTicket
	updateErr       error
	gotUpdateID     string
	gotUpdateTarget connectors.ExternalStatusTarget
}

// fakeRuntimeResolver implements ports.TicketingRuntimeResolver
// (PRODUCT.6-L), returning the harness's fakeCompanies/fakeTicketing on
// every call — proves the service resolves per-call rather than caching a
// fixed dependency, and lets tests inject resolution failures.
type fakeRuntimeResolver struct {
	companies ports.CompanyDirectory
	ticketing connectors.TicketingConnector
	err       error
	calls     int32
}

func (f *fakeRuntimeResolver) Resolve(ctx context.Context, tenantID uuid.UUID) (*ports.TicketingRuntime, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.err != nil {
		return nil, f.err
	}
	return &ports.TicketingRuntime{CompanyDirectory: f.companies, TicketingConnector: f.ticketing}, nil
}

func (f *fakeTicketing) Name() string {
	if f.name == "" {
		return "fake"
	}
	return f.name
}
func (f *fakeTicketing) GetTicket(ctx context.Context, externalTicketID string) (*connectors.ExternalTicket, error) {
	atomic.AddInt32(&f.getCalls, 1)
	f.gotGetID = externalTicketID
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getResult != nil {
		return f.getResult, nil
	}
	return nil, errors.New("fakeTicketing: GetTicket not configured for this test")
}
func (f *fakeTicketing) CreateTicket(ctx context.Context, req connectors.CreateTicketRequest) (*connectors.ExternalTicket, error) {
	atomic.AddInt32(&f.calls, 1)
	f.gotReq = req
	return f.result, f.err
}
func (f *fakeTicketing) UpdateTicketStatus(ctx context.Context, externalID string, target connectors.ExternalStatusTarget) (*connectors.ExternalTicket, error) {
	atomic.AddInt32(&f.updateCalls, 1)
	f.gotUpdateID, f.gotUpdateTarget = externalID, target
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if f.updateResult != nil {
		return f.updateResult, nil
	}
	return nil, errors.New("fakeTicketing: UpdateTicketStatus not configured for this test")
}

// ---- test fixture -------------------------------------------------------

const (
	testCompanyID = "d38e7970-635d-490b-a119-749ee6f1fe23"
	testKey       = "test-key-0001"
)

func testCommand(overrides func(*CreateExternalTicketCommand)) CreateExternalTicketCommand {
	cmd := CreateExternalTicketCommand{
		TenantID: uuid.New(), ConversationID: uuid.New(), ActorUserID: uuid.New(),
		SelectedCustomerExternalID: testCompanyID, Subject: "s", Description: "d", IdempotencyKey: testKey,
	}
	if overrides != nil {
		overrides(&cmd)
	}
	return cmd
}

type harness struct {
	perms        *fakePerms
	conversation *fakeConversation
	companies    *fakeCompanies
	attempts     *fakeAttempts
	localTickets *fakeLocalTickets
	ticketing    *fakeTicketing
	runtime      *fakeRuntimeResolver
	svc          *Service
}

func newHarness(actorAssigned bool, actorID uuid.UUID) *harness {
	h := &harness{
		perms:        &fakePerms{granted: map[string]bool{PermissionTicketCreate: true}},
		conversation: &fakeConversation{found: true},
		companies:    &fakeCompanies{companies: []ports.Company{{ExternalID: testCompanyID, Active: true}}},
		attempts:     newFakeAttempts(),
		localTickets: &fakeLocalTickets{candidate: &ticketsdomain.Ticket{ID: uuid.New()}},
		ticketing:    &fakeTicketing{result: &connectors.ExternalTicket{ExternalID: "28180", ExternalStatus: "1", ExternalStatusLabel: "Novo"}},
	}
	if actorAssigned {
		h.conversation.assignedTo = &actorID
	}
	h.runtime = &fakeRuntimeResolver{companies: h.companies, ticketing: h.ticketing}
	h.svc = NewService(h.perms, h.conversation, h.attempts, h.localTickets, h.runtime)
	return h
}

func withTenantContext(tenantID, actorID uuid.UUID) context.Context {
	tc, err := tenancydomain.NewTenantContext(tenantID, actorID, tenancydomain.AccessSourceDirect)
	if err != nil {
		panic(err)
	}
	return tenancydomain.WithTenantContext(context.Background(), tc)
}

// ---- A-E: authorization -------------------------------------------------

func TestCreateExternalTicketRejectsMissingTicketCreate(t *testing.T) {
	h := newHarness(true, uuid.Nil)
	h.perms.granted[PermissionTicketCreate] = false
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	_, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d calls", h.ticketing.calls)
	}
}

func TestCreateExternalTicketRejectsUnassignedConversation(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	// conversation exists but has no assignee
	_, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if !errors.Is(err, ErrUnassigned) {
		t.Fatalf("err = %v, want ErrUnassigned", err)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
}

func TestCreateExternalTicketAllowsOwnAssignment(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeCreated {
		t.Fatalf("outcome = %q, want created", res.Outcome)
	}
}

func TestCreateExternalTicketRejectsOtherAssignmentWithoutManage(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	other := uuid.New()
	h.conversation.assignedTo = &other
	_, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if !errors.Is(err, ErrNotAssignedToYou) {
		t.Fatalf("err = %v, want ErrNotAssignedToYou", err)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
}

func TestCreateExternalTicketAllowsConversationManage(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.perms.granted[PermissionConversationManage] = true
	cmd := testCommand(nil)
	other := uuid.New()
	h.conversation.assignedTo = &other
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeCreated {
		t.Fatalf("outcome = %q, want created", res.Outcome)
	}
}

// PRODUCT.6-L: runtime resolution failure (e.g. NO_CONFIGURATION) must
// propagate before company validation or any provider work, and the
// service must resolve fresh per call (never cache/share a connector).
func TestCreateExternalTicketPropagatesRuntimeResolutionFailure(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.runtime.err = &ports.ResolutionError{Code: ports.ResolutionNoConfiguration, Message: "no K3G connection configured"}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	_, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	var resErr *ports.ResolutionError
	if !errors.As(err, &resErr) || resErr.Code != ports.ResolutionNoConfiguration {
		t.Fatalf("err = %v, want *ports.ResolutionError{Code: NO_CONFIGURATION}", err)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
	if h.runtime.calls != 1 {
		t.Fatalf("runtime resolver called %d times, want 1", h.runtime.calls)
	}
}

// ---- F-J: company validation ---------------------------------------------

func TestCreateExternalTicketRejectsArbitraryCompany(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(func(c *CreateExternalTicketCommand) { c.SelectedCustomerExternalID = "not-a-real-company" })
	h.conversation.assignedTo = &cmd.ActorUserID
	_, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if !errors.Is(err, ErrInvalidCompany) {
		t.Fatalf("err = %v, want ErrInvalidCompany", err)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
}

func TestCreateExternalTicketRejectsInactiveCompany(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.companies.companies = []ports.Company{{ExternalID: testCompanyID, Active: false}}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	_, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if !errors.Is(err, ErrInvalidCompany) {
		t.Fatalf("err = %v, want ErrInvalidCompany", err)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
}

func TestCreateExternalTicketAcceptsExactActiveCompanyAndForwardsExactID(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeCreated {
		t.Fatalf("outcome = %q, want created", res.Outcome)
	}
	if h.ticketing.gotReq.CustomerExternalID != testCompanyID {
		t.Fatalf("provider received CustomerExternalID=%q, want %q", h.ticketing.gotReq.CustomerExternalID, testCompanyID)
	}
}

func TestCreateExternalTicketRejectsDuplicateCompanyIDsAsInconsistent(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.companies.companies = []ports.Company{{ExternalID: testCompanyID, Active: true}, {ExternalID: testCompanyID, Active: true}}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	_, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if !errors.Is(err, ErrInvalidCompany) {
		t.Fatalf("err = %v, want ErrInvalidCompany for a duplicate/inconsistent provider result", err)
	}
}

// ---- K: provider identity -------------------------------------------------

func TestCreateExternalTicketActorNeverEntersProviderRequest(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	if _, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := h.ticketing.gotReq
	if req.CustomerExternalID == cmd.ActorUserID.String() || req.Subject == cmd.ActorUserID.String() || req.Description == cmd.ActorUserID.String() {
		t.Fatalf("ActorUserID leaked into provider request: %+v", req)
	}
	// Structural: CreateTicketRequest has exactly these three fields, none
	// of which is named/assignable from an actor/user identity concept.
	_ = connectors.CreateTicketRequest{CustomerExternalID: "x", Subject: "y", Description: "z"}
}

// ---- idempotency ----------------------------------------------------------

func TestCreateExternalTicketSameKeySameCommandReplaysWithoutSecondPOST(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	first, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if h.ticketing.calls != 1 {
		t.Fatalf("provider called %d times, want exactly 1", h.ticketing.calls)
	}
	if second.Outcome != OutcomeReplaySuccess || second.ExternalTicketID != first.ExternalTicketID {
		t.Fatalf("replay result mismatch: first=%+v second=%+v", first, second)
	}
	// PRODUCT.6-M4 section 16 (M3 regression): an already-synced
	// confirmed_success replay must never call GetTicket either.
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket called %d times on an already-synced replay, want 0", h.ticketing.getCalls)
	}
	if h.localTickets.enrichCalls != 1 {
		t.Fatalf("EnrichExternalProjection called %d times, want exactly 1 (replay must not rewrite projection)", h.localTickets.enrichCalls)
	}
}

func TestCreateExternalTicketDifferentHashIsMismatch(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	if _, err := h.svc.CreateExternalTicket(ctx, cmd); err != nil {
		t.Fatalf("first call: %v", err)
	}
	cmd2 := cmd
	cmd2.Subject = "a completely different subject"
	_, err := h.svc.CreateExternalTicket(ctx, cmd2)
	if !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("err = %v, want ErrIdempotencyMismatch", err)
	}
}

func TestCreateExternalTicketConcurrentSameKeyCallsProviderExactlyOnce(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = h.svc.CreateExternalTicket(ctx, cmd)
		}()
	}
	wg.Wait()
	if h.ticketing.calls != 1 {
		t.Fatalf("provider called %d times, want exactly 1", h.ticketing.calls)
	}
}

func TestCreateExternalTicketInFlightReplayNeverPosts(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	hash := requestHash(cmd.TenantID, cmd.ConversationID, testCompanyID, cmd.Subject, cmd.Description)
	if _, _, err := h.attempts.Acquire(ctx, cmd.ConversationID, h.localTickets.candidate.ID, cmd.ActorUserID, cmd.IdempotencyKey, hash); err != nil {
		t.Fatalf("seed acquire: %v", err)
	}
	res, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeReconciliationRequired || res.AttemptState != ticketsdomain.AttemptInFlight {
		t.Fatalf("result = %+v, want reconciliation_required/in_flight", res)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called on in_flight replay, got %d", h.ticketing.calls)
	}
}

func TestCreateExternalTicketOutcomeUnknownReplayNeverPosts(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	hash := requestHash(cmd.TenantID, cmd.ConversationID, testCompanyID, cmd.Subject, cmd.Description)
	attempt, _, err := h.attempts.Acquire(ctx, cmd.ConversationID, h.localTickets.candidate.ID, cmd.ActorUserID, cmd.IdempotencyKey, hash)
	if err != nil {
		t.Fatalf("seed acquire: %v", err)
	}
	if _, err := h.attempts.MarkOutcomeUnknown(ctx, attempt.ID); err != nil {
		t.Fatalf("seed outcome_unknown: %v", err)
	}
	res, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeReconciliationRequired || res.AttemptState != ticketsdomain.AttemptOutcomeUnknown {
		t.Fatalf("result = %+v, want reconciliation_required/outcome_unknown", res)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called on outcome_unknown replay, got %d", h.ticketing.calls)
	}
}

func TestCreateExternalTicketConfirmedFailureReplayNeverPosts(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	hash := requestHash(cmd.TenantID, cmd.ConversationID, testCompanyID, cmd.Subject, cmd.Description)
	attempt, _, err := h.attempts.Acquire(ctx, cmd.ConversationID, h.localTickets.candidate.ID, cmd.ActorUserID, cmd.IdempotencyKey, hash)
	if err != nil {
		t.Fatalf("seed acquire: %v", err)
	}
	if _, err := h.attempts.MarkConfirmedFailure(ctx, attempt.ID); err != nil {
		t.Fatalf("seed confirmed_failure: %v", err)
	}
	res, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeDefinitiveFailure {
		t.Fatalf("result = %+v, want definitive_failure", res)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called on confirmed_failure replay, got %d", h.ticketing.calls)
	}
}

// ---- provider outcomes ------------------------------------------------

func TestCreateExternalTicketDefinitiveProviderRejectionMarksConfirmedFailure(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.ticketing.result, h.ticketing.err = nil, &connectors.TicketingError{Code: connectors.TicketingValidationError, Message: "bad"}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeDefinitiveFailure || res.FailureCode != connectors.TicketingValidationError {
		t.Fatalf("result = %+v, want definitive_failure/VALIDATION_ERROR", res)
	}
}

func TestCreateExternalTicketWriteOutcomeUnknownMarksOutcomeUnknown(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.ticketing.result, h.ticketing.err = nil, &connectors.TicketingError{Code: connectors.TicketingWriteOutcomeUnknown, Message: "ambiguous"}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeReconciliationRequired || res.AttemptState != ticketsdomain.AttemptOutcomeUnknown {
		t.Fatalf("result = %+v, want reconciliation_required/outcome_unknown", res)
	}
}

func TestCreateExternalTicketSuccessMarksConfirmedSuccess(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeCreated || res.ExternalTicketID != "28180" {
		t.Fatalf("result = %+v, want created/28180", res)
	}
	// PRODUCT.6-M4: the provider's raw status/label must reach the local
	// projection unchanged — never a hardcoded empty placeholder.
	if h.localTickets.gotExternalStatus != "1" || h.localTickets.gotExternalStatusLabel != "Novo" {
		t.Fatalf("projected external_status=%q external_status_label=%q, want \"1\"/\"Novo\"",
			h.localTickets.gotExternalStatus, h.localTickets.gotExternalStatusLabel)
	}
	if h.localTickets.gotProvider != "fake" || h.localTickets.gotExternalTicketID != "28180" {
		t.Fatalf("projected provider=%q external_ticket_id=%q, want fake/28180", h.localTickets.gotProvider, h.localTickets.gotExternalTicketID)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket called %d times on a normal create, want 0", h.ticketing.getCalls)
	}
}

// PRODUCT.6-M4 section 13: confirmed_success + projection not synced +
// GetTicket failure during recovery must never fall back to CreateTicket.
func TestCreateExternalTicketRecoveryGetTicketFailureNeverRetriesCreate(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.localTickets.enrichErrSeq = []error{errors.New("db down")}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	if _, err := h.svc.CreateExternalTicket(ctx, cmd); err != nil {
		t.Fatalf("first call: %v", err)
	}
	h.ticketing.getErr = &connectors.TicketingError{Code: connectors.TicketingProviderUnavailable, Message: "down"}
	res, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err != nil {
		t.Fatalf("replay error: %v", err)
	}
	if res.Outcome != OutcomeReconciliationRequired {
		t.Fatalf("result = %+v, want reconciliation_required", res)
	}
	if h.ticketing.calls != 1 {
		t.Fatalf("CreateTicket called %d times, want exactly 1 (never retried after GetTicket failure)", h.ticketing.calls)
	}
	if h.ticketing.getCalls != 1 {
		t.Fatalf("GetTicket called %d times, want exactly 1", h.ticketing.getCalls)
	}
	if h.localTickets.enrichCalls != 1 {
		t.Fatalf("EnrichExternalProjection called %d times, want 1 (recovery must not attempt projection after a failed GetTicket)", h.localTickets.enrichCalls)
	}
}

// PRODUCT.6-M4 section 14: the durable attempt's provider must match the
// currently resolved connector's Name() before recovery may query it — no
// silent provider substitution.
func TestCreateExternalTicketRecoveryProviderMismatchIsReconciliationRequired(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.localTickets.enrichErrSeq = []error{errors.New("db down")}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	if _, err := h.svc.CreateExternalTicket(ctx, cmd); err != nil {
		t.Fatalf("first call: %v", err)
	}
	h.ticketing.name = "a-different-provider"
	res, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err != nil {
		t.Fatalf("replay error: %v", err)
	}
	if res.Outcome != OutcomeReconciliationRequired {
		t.Fatalf("result = %+v, want reconciliation_required", res)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket called %d times, want 0 (provider mismatch must never be queried)", h.ticketing.getCalls)
	}
	if h.ticketing.calls != 1 {
		t.Fatalf("CreateTicket called %d times, want exactly 1", h.ticketing.calls)
	}
	if h.localTickets.enrichCalls != 1 {
		t.Fatalf("EnrichExternalProjection called %d times, want 1 (no recovery attempt on provider mismatch)", h.localTickets.enrichCalls)
	}
}

// PRODUCT.6-M4 section 15: if GetTicket unexpectedly returns a DIFFERENT
// external ID than the one durably owned by the attempt, never overwrite
// the projection or the attempt's identity.
func TestCreateExternalTicketRecoveryExternalIDMismatchIsReconciliationRequired(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.localTickets.enrichErrSeq = []error{errors.New("db down")}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	if _, err := h.svc.CreateExternalTicket(ctx, cmd); err != nil {
		t.Fatalf("first call: %v", err)
	}
	h.ticketing.getResult = &connectors.ExternalTicket{ExternalID: "99999", ExternalStatus: "1", ExternalStatusLabel: "Novo"}
	res, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err != nil {
		t.Fatalf("replay error: %v", err)
	}
	if res.Outcome != OutcomeReconciliationRequired {
		t.Fatalf("result = %+v, want reconciliation_required", res)
	}
	if h.ticketing.calls != 1 {
		t.Fatalf("CreateTicket called %d times, want exactly 1", h.ticketing.calls)
	}
	// EnrichExternalProjection must NOT be called again with the mismatched ID.
	if h.localTickets.enrichCalls != 1 {
		t.Fatalf("EnrichExternalProjection called %d times, want 1 (mismatch must not reach projection)", h.localTickets.enrichCalls)
	}
}

// ---- PRODUCT.6-M5: existing external link guard --------------------------

// Section 14: an active local ticket already consistently linked
// (Provider + ExternalTicketID both set) must never reach AttemptStore or
// the provider merely because this call uses a brand-new Idempotency-Key.
func TestCreateExternalTicketAlreadyLinkedNeverAcquiresOrCallsProvider(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	provider, externalID := "k3g", "28182"
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: uuid.New(), Provider: &provider, ExternalTicketID: &externalID}
	cmd := testCommand(nil) // fresh IdempotencyKey, unrelated to any prior attempt
	h.conversation.assignedTo = &cmd.ActorUserID

	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeAlreadyLinked {
		t.Fatalf("result = %+v, want already_linked", res)
	}
	if res.ExternalTicketID != externalID || res.Provider != provider {
		t.Fatalf("result identity = %+v, want %s/%s", res, provider, externalID)
	}
	if len(h.attempts.byKey) != 0 {
		t.Fatalf("AttemptStore.Acquire must not be called, but %d attempt(s) exist", len(h.attempts.byKey))
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("CreateTicket called %d times, want 0", h.ticketing.calls)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket called %d times, want 0", h.ticketing.getCalls)
	}
	if h.localTickets.enrichCalls != 0 {
		t.Fatalf("EnrichExternalProjection called %d times, want 0", h.localTickets.enrichCalls)
	}
}

// Section 15: inconsistent linkage (one of Provider/ExternalTicketID set,
// the other not) is a data-integrity state, never "repaired" by creating
// another ERP ticket.
func TestCreateExternalTicketInconsistentLinkIsReconciliationRequired(t *testing.T) {
	externalID := "28182"
	cases := []struct {
		name string
		set  func(*ticketsdomain.Ticket)
	}{
		{"external_ticket_id without provider", func(tk *ticketsdomain.Ticket) { tk.ExternalTicketID = &externalID }},
		{"provider without external_ticket_id", func(tk *ticketsdomain.Ticket) { p := "k3g"; tk.Provider = &p }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(false, uuid.Nil)
			ticket := &ticketsdomain.Ticket{ID: uuid.New()}
			c.set(ticket)
			h.localTickets.candidate = ticket
			cmd := testCommand(nil)
			h.conversation.assignedTo = &cmd.ActorUserID

			res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Outcome != OutcomeReconciliationRequired || !res.Severe {
				t.Fatalf("result = %+v, want severe reconciliation_required", res)
			}
			if len(h.attempts.byKey) != 0 {
				t.Fatalf("AttemptStore.Acquire must not be called, but %d attempt(s) exist", len(h.attempts.byKey))
			}
			if h.ticketing.calls != 0 || h.ticketing.getCalls != 0 {
				t.Fatalf("provider must not be called, CreateTicket=%d GetTicket=%d", h.ticketing.calls, h.ticketing.getCalls)
			}
			if h.localTickets.enrichCalls != 0 {
				t.Fatalf("EnrichExternalProjection called %d times, want 0", h.localTickets.enrichCalls)
			}
		})
	}
}

// Cross-key blocking at the application layer (fake proves orchestration;
// real-Postgres atomic proof lives in
// internal/tickets/adapters/external_create_attempt_postgres_test.go).
func TestCreateExternalTicketDifferentKeyBlockedByInFlightAttemptNeverPosts(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	// Seed an in_flight attempt under a DIFFERENT key for the same local ticket.
	otherHash := requestHash(cmd.TenantID, cmd.ConversationID, testCompanyID, "different subject", cmd.Description)
	if _, _, err := h.attempts.Acquire(ctx, cmd.ConversationID, h.localTickets.candidate.ID, cmd.ActorUserID, "other-key-0001", otherHash); err != nil {
		t.Fatalf("seed acquire: %v", err)
	}

	res, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeReconciliationRequired || res.AttemptState != ticketsdomain.AttemptInFlight {
		t.Fatalf("result = %+v, want reconciliation_required/in_flight", res)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
	if len(h.attempts.byKey) != 1 {
		t.Fatalf("a blocked different-key call must not create a second attempt row, got %d", len(h.attempts.byKey))
	}
}

// ---- projection ---------------------------------------------------------

func TestCreateExternalTicketNoLocalTicketToEnrichIsRejected(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.localTickets.candidate = nil
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	_, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if !errors.Is(err, ErrNoLocalTicketToEnrich) {
		t.Fatalf("err = %v, want ErrNoLocalTicketToEnrich", err)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.calls)
	}
}

func TestCreateExternalTicketEnrichesExistingTicketNoNewOne(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	existing := uuid.New()
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: existing}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.LocalTicketID != existing {
		t.Fatalf("LocalTicketID = %s, want the pre-existing candidate %s (no new ticket)", res.LocalTicketID, existing)
	}
	if h.localTickets.enrichCalls != 1 {
		t.Fatalf("EnrichExternalProjection called %d times, want 1", h.localTickets.enrichCalls)
	}
}

// PRODUCT.6-K2 proof-completion item 1: provider success + durable
// CONFIRMED_SUCCESS write failure (section 9's narrow crash/storage-failure
// window). Proves the already-implemented ordering: local projection is
// NEVER attempted before the durable write succeeds, the outcome is a
// Severe reconciliation-required with a non-nil Go error, and — critically
// — a replay with the same key/hash NEVER issues a second provider POST,
// because the durable state was left in_flight (no age/TTL heuristic ever
// makes it retryable).
func TestCreateExternalTicketDurableSuccessWriteFailureNeverRetriesProvider(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.attempts.markConfirmedSuccessErrSeq = []error{errors.New("db connection lost mid-write")}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	res, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err == nil {
		t.Fatal("expected a non-nil Go error for the durable-write failure")
	}
	if res == nil {
		t.Fatal("expected a non-nil Result alongside the error")
	}
	if res.Outcome != OutcomeReconciliationRequired {
		t.Fatalf("Outcome = %q, want reconciliation_required", res.Outcome)
	}
	if !res.Severe {
		t.Fatal("Severe = false, want true for a provider-success/durable-write-failure outcome")
	}
	if res.AttemptState != ticketsdomain.AttemptInFlight {
		t.Fatalf("AttemptState = %q, want in_flight (the guarded write never transitioned it)", res.AttemptState)
	}
	if res.ExternalTicketID != "28180" {
		t.Fatalf("ExternalTicketID = %q, want the provider's real returned id (28180) even though it wasn't durably recorded", res.ExternalTicketID)
	}
	if h.ticketing.calls != 1 {
		t.Fatalf("provider called %d times on first invocation, want exactly 1", h.ticketing.calls)
	}
	if h.localTickets.enrichCalls != 0 {
		t.Fatalf("local projection must NOT be attempted before the durable success write succeeds, got %d enrich calls", h.localTickets.enrichCalls)
	}

	// Replay with the identical command/idempotency key.
	res2, err2 := h.svc.CreateExternalTicket(ctx, cmd)
	if err2 != nil {
		t.Fatalf("replay must not itself error: %v", err2)
	}
	if res2.Outcome != OutcomeReconciliationRequired || res2.AttemptState != ticketsdomain.AttemptInFlight {
		t.Fatalf("replay result = %+v, want reconciliation_required/in_flight", res2)
	}
	if h.ticketing.calls != 1 {
		t.Fatalf("provider called %d times total across both invocations, want exactly 1 (no automatic retry)", h.ticketing.calls)
	}
}

func TestCreateExternalTicketProjectionFailureKeepsConfirmedSuccessNoSecondPOST(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	h.localTickets.enrichErrSeq = []error{errors.New("db down")}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	ctx := withTenantContext(cmd.TenantID, cmd.ActorUserID)

	res, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeReconciliationRequired {
		t.Fatalf("result = %+v, want reconciliation_required", res)
	}
	// attempt row must remain confirmed_success durably.
	hash := requestHash(cmd.TenantID, cmd.ConversationID, testCompanyID, cmd.Subject, cmd.Description)
	attempt, acquired, err := h.attempts.Acquire(ctx, cmd.ConversationID, h.localTickets.candidate.ID, cmd.ActorUserID, cmd.IdempotencyKey, hash)
	if err != nil || acquired {
		t.Fatalf("Acquire replay: attempt=%+v acquired=%v err=%v", attempt, acquired, err)
	}
	if attempt.State != ticketsdomain.AttemptConfirmedSuccess {
		t.Fatalf("attempt state = %q, want confirmed_success", attempt.State)
	}

	// Replay: retries ONLY the projection via a GetTicket read-back
	// (PRODUCT.6-M4), never a second CreateTicket POST.
	h.ticketing.getResult = &connectors.ExternalTicket{ExternalID: "28180", ExternalStatus: "1", ExternalStatusLabel: "Novo"}
	res2, err := h.svc.CreateExternalTicket(ctx, cmd)
	if err != nil {
		t.Fatalf("replay error: %v", err)
	}
	if res2.Outcome != OutcomeReplaySuccess && res2.Outcome != OutcomeCreated {
		t.Fatalf("replay after projection recovery = %+v, want a success outcome", res2)
	}
	if h.ticketing.calls != 1 {
		t.Fatalf("provider CreateTicket called %d times across both attempts, want exactly 1", h.ticketing.calls)
	}
	if h.ticketing.getCalls != 1 {
		t.Fatalf("provider GetTicket called %d times, want exactly 1", h.ticketing.getCalls)
	}
	if h.localTickets.enrichCalls != 2 {
		t.Fatalf("EnrichExternalProjection called %d times, want 2 (initial failure + recovery retry)", h.localTickets.enrichCalls)
	}
}
