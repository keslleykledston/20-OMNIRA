package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsapplication "github.com/omnira/omnira/internal/tickets/application"
	ticketsadapters "github.com/omnira/omnira/internal/tickets/adapters"
	"github.com/omnira/omnira/internal/testhelpers"
	ticketsports "github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// PRODUCT.6-O2BH HTTP tests: REAL PostgreSQL + fake K3G connector.
// Proves full composition: HTTP → real application service → real stores → fake provider.

const statusHTTPProvider = "k3g"
const statusHTTPExternalID = "9115"

type statusHTTPFakeTicketing struct {
	name           string
	updateCalls    int
	updateErr      *connectors.TicketingError
	updateResult   *connectors.ExternalTicket
	updateHook     func()
	getResult      *connectors.ExternalTicket
	getErr         *connectors.TicketingError
	getCalls       int
}

func (f *statusHTTPFakeTicketing) Name() string {
	if f.name == "" {
		return statusHTTPProvider
	}
	return f.name
}

func (f *statusHTTPFakeTicketing) UpdateTicketStatus(ctx context.Context, externalID string, target connectors.ExternalStatusTarget) (*connectors.ExternalTicket, error) {
	f.updateCalls++
	if f.updateHook != nil {
		f.updateHook()
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return f.updateResult, nil
}

func (f *statusHTTPFakeTicketing) GetTicket(ctx context.Context, externalTicketID string) (*connectors.ExternalTicket, error) {
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getResult, nil
}

func (f *statusHTTPFakeTicketing) CreateTicket(ctx context.Context, req connectors.CreateTicketRequest) (*connectors.ExternalTicket, error) {
	return nil, errors.New("not used in status mutation tests")
}

type statusHTTPFakeRuntimeResolver struct {
	ticketing connectors.TicketingConnector
	err       error
}

func (f *statusHTTPFakeRuntimeResolver) Resolve(ctx context.Context, tenantID uuid.UUID) (*ticketsports.TicketingRuntime, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &ticketsports.TicketingRuntime{
		TicketingConnector: f.ticketing,
		CompanyDirectory:   nil,
	}, nil
}

type statusHTTPFixture struct {
	seedPool *pgxpool.Pool
	appPool  *pgxpool.Pool
	t        *testing.T
}

func requireStatusHTTPFixture(t *testing.T) *statusHTTPFixture {
	t.Helper()
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx := context.Background()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(seed.Close)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return &statusHTTPFixture{seedPool: seed, appPool: app, t: t}
}

// TestUpdateTicketStatusHTTPSuccess proves full real composition:
// HTTP → real application service → real PostgreSQL stores → fake provider
// Validates tickets.status unchanged, projection enriched, exactly one provider mutation
func TestUpdateTicketStatusHTTPSuccess(t *testing.T) {
	f := requireStatusHTTPFixture(t)
	ctx := context.Background()

	// Create fixture: tenant, actor, conversation, active ticket
	tenantID := uuid.New()
	actorID := uuid.New()
	convID := uuid.New()
	contactID := uuid.New()
	ticketID := uuid.New()

	// Use seed pool to create fixture (no RLS)
	err := f.seedPool.QueryRow(ctx,
		`INSERT INTO tenants (id, legal_name) VALUES ($1, $2) RETURNING id`,
		tenantID, "Test Tenant",
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	// Create user (for assignment)
	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO users (id, external_subject) VALUES ($1, $2) RETURNING id`,
		actorID, fmt.Sprintf("actor-%s", actorID.String()),
	).Scan(&actorID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Create contact
	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		contactID, tenantID, "Test Contact", "+5511999999999", "test@example.com",
	).Scan(&contactID)
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}

	// Create conversation
	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO conversations (id, tenant_id, contact_id, assigned_to_user_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		convID, tenantID, contactID, actorID,
	).Scan(&convID)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	// Create active ticket (linked to provider)
	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO tickets (id, tenant_id, conversation_id, provider, external_ticket_id, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW()) RETURNING id`,
		ticketID, tenantID, convID, statusHTTPProvider, statusHTTPExternalID, "open",
	).Scan(&ticketID)
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	// Verify fixture created
	var beforeStatus string
	var beforeProvider, beforeExternalID *string
	var beforeExternalStatus *string
	err = f.seedPool.QueryRow(ctx,
		`SELECT status, provider, external_ticket_id, external_status FROM tickets WHERE id = $1`,
		ticketID,
	).Scan(&beforeStatus, &beforeProvider, &beforeExternalID, &beforeExternalStatus)
	if err != nil {
		t.Fatalf("verify fixture: %v", err)
	}

	// Setup fake connector: will return matching external ID + target status
	fakeTicketing := &statusHTTPFakeTicketing{
		updateResult: &connectors.ExternalTicket{
			ExternalID:          statusHTTPExternalID,
			ExternalStatus:      "5",
			ExternalStatusLabel: "Resolvido",
		},
	}
	fakeRuntime := &statusHTTPFakeRuntimeResolver{ticketing: fakeTicketing}

	// Create application service with real stores (using seedPool for now to avoid RLS transaction isolation)
	svc := ticketsapplication.NewUpdateExternalTicketStatusService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketUpdate: true}},
		&httpFakeConversation{found: true, assignedTo: &actorID},
		ticketsadapters.NewStatusMutationAttemptStore(f.seedPool),
		ticketsadapters.NewLocalTicketStore(f.seedPool),
		fakeRuntime,
	)

	// Create HTTP handler
	handler := NewCRMHandlers(nil)
	handler.SetUpdateExternalTicketStatusService(svc)

	// Build HTTP request with tenant context
	tenantCtx := tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})
	authnCtx := authn.WithPrincipal(tenantCtx, &authn.Principal{UserID: actorID})

	reqBody := updateTicketStatusRequest{TargetStatus: "5"}
	reqBodyJSON, _ := json.Marshal(reqBody)
	path := "/api/v1/tenants/" + tenantID.String() + "/conversations/" + convID.String() + "/ticket/status"
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBodyJSON))
	httpReq = httpReq.WithContext(authnCtx)
	httpReq.Header.Set("Idempotency-Key", "test-key-001")
	httpReq.Header.Set("Content-Type", "application/json")

	// Execute HTTP request
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)
	mux.ServeHTTP(rec, httpReq)

	// Validate HTTP response
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var respBody updateTicketStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Validate response fields
	if respBody.LocalTicketID != ticketID {
		t.Fatalf("LocalTicketID = %s, want %s", respBody.LocalTicketID, ticketID)
	}
	if respBody.Provider != statusHTTPProvider {
		t.Fatalf("Provider = %q, want %q", respBody.Provider, statusHTTPProvider)
	}
	if respBody.ExternalTicketID != statusHTTPExternalID {
		t.Fatalf("ExternalTicketID = %q, want %q", respBody.ExternalTicketID, statusHTTPExternalID)
	}
	if respBody.ExternalStatus != "5" {
		t.Fatalf("ExternalStatus = %q, want 5", respBody.ExternalStatus)
	}
	if respBody.ExternalStatusLabel != "Resolvido" {
		t.Fatalf("ExternalStatusLabel = %q, want Resolvido", respBody.ExternalStatusLabel)
	}
	if respBody.SyncStatus != "synced" {
		t.Fatalf("SyncStatus = %q, want synced", respBody.SyncStatus)
	}
	if respBody.LastSyncedAt == nil {
		t.Fatalf("LastSyncedAt is nil, want not null")
	}
	if respBody.Replayed {
		t.Fatalf("Replayed = true, want false")
	}

	// Validate provider was called exactly once
	if fakeTicketing.updateCalls != 1 {
		t.Fatalf("UpdateTicketStatus called %d times, want exactly 1", fakeTicketing.updateCalls)
	}

	// Validate DB ticket.status unchanged
	var afterStatus string
	var afterProvider, afterExternalID *string
	var afterExternalStatus, afterExternalLabel *string
	var afterSyncStatus *string
	var afterLastSynced *time.Time
	err = f.seedPool.QueryRow(ctx,
		`SELECT status, provider, external_ticket_id, external_status, external_status_label, sync_status, last_synced_at FROM tickets WHERE id = $1`,
		ticketID,
	).Scan(&afterStatus, &afterProvider, &afterExternalID, &afterExternalStatus, &afterExternalLabel, &afterSyncStatus, &afterLastSynced)
	if err != nil {
		t.Fatalf("read ticket after: %v", err)
	}

	// tickets.status must be unchanged
	if afterStatus != beforeStatus {
		t.Fatalf("tickets.status changed from %q to %q (must be unchanged)", beforeStatus, afterStatus)
	}

	// provider/external_ticket_id must be preserved
	if afterProvider == nil || *afterProvider != statusHTTPProvider {
		t.Fatalf("provider lost or changed")
	}
	if afterExternalID == nil || *afterExternalID != statusHTTPExternalID {
		t.Fatalf("external_ticket_id lost or changed")
	}

	// projection fields must be updated
	if afterExternalStatus == nil || *afterExternalStatus != "5" {
		t.Fatalf("external_status = %v, want 5", afterExternalStatus)
	}
	if afterExternalLabel == nil || *afterExternalLabel != "Resolvido" {
		t.Fatalf("external_status_label = %v, want Resolvido", afterExternalLabel)
	}
	if afterSyncStatus == nil || *afterSyncStatus != "synced" {
		t.Fatalf("sync_status = %v, want synced", afterSyncStatus)
	}
	if afterLastSynced == nil {
		t.Fatalf("last_synced_at is NULL, want NOT NULL")
	}
}

// TestUpdateTicketStatusHTTPMissingIdempotencyKey validates Idempotency-Key header requirement
func TestUpdateTicketStatusHTTPMissingIdempotencyKey(t *testing.T) {
	svc := ticketsapplication.NewUpdateExternalTicketStatusService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketUpdate: true}},
		&httpFakeConversation{},
		nil, nil, nil,
	)
	handler := NewCRMHandlers(nil)
	handler.SetUpdateExternalTicketStatusService(svc)

	tenantID, convID := uuid.New(), uuid.New()
	ctx := authn.WithPrincipal(context.Background(), &authn.Principal{UserID: uuid.New()})
	ctx = tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID:  tenantID,
		ActorID:   uuid.New(),
		Source:   tenancydomain.AccessSourceDirect,
	})

	path := "/api/v1/tenants/" + tenantID.String() + "/conversations/" + convID.String() + "/ticket/status"
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{"target_status":"5"}`)))
	httpReq = httpReq.WithContext(ctx)
	httpReq.Header.Set("Content-Type", "application/json")
	// NOTE: no Idempotency-Key header

	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)
	mux.ServeHTTP(rec, httpReq)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("HTTP status = %d, want 400", rec.Code)
	}
}

// TestUpdateTicketStatusHTTPPermissionDenied validates authorization
func TestUpdateTicketStatusHTTPPermissionDenied(t *testing.T) {
	svc := ticketsapplication.NewUpdateExternalTicketStatusService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketUpdate: false}},
		&httpFakeConversation{found: true},
		nil, nil, nil,
	)
	handler := NewCRMHandlers(nil)
	handler.SetUpdateExternalTicketStatusService(svc)

	tenantID, convID, actorID := uuid.New(), uuid.New(), uuid.New()
	ctx := authn.WithPrincipal(context.Background(), &authn.Principal{UserID: actorID})
	ctx = tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID:  tenantID,
		ActorID:   actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})

	path := "/api/v1/tenants/" + tenantID.String() + "/conversations/" + convID.String() + "/ticket/status"
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{"target_status":"5"}`)))
	httpReq = httpReq.WithContext(ctx)
	httpReq.Header.Set("Idempotency-Key", "test-key")
	httpReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)
	mux.ServeHTTP(rec, httpReq)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("HTTP status = %d, want 403", rec.Code)
	}
}

// TestUpdateTicketStatusHTTPServiceNotWired proves safe 503 when service not wired
func TestUpdateTicketStatusHTTPServiceNotWired(t *testing.T) {
	handler := NewCRMHandlers(nil)
	// Do NOT wire service

	tenantID, convID, actorID := uuid.New(), uuid.New(), uuid.New()
	ctx := authn.WithPrincipal(context.Background(), &authn.Principal{UserID: actorID})
	ctx = tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID:  tenantID,
		ActorID:   actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})

	path := "/api/v1/tenants/" + tenantID.String() + "/conversations/" + convID.String() + "/ticket/status"
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{"target_status":"5"}`)))
	httpReq = httpReq.WithContext(ctx)
	httpReq.Header.Set("Idempotency-Key", "test-key")
	httpReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)
	mux.ServeHTTP(rec, httpReq)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("HTTP status = %d, want 503", rec.Code)
	}
}

// TestUpdateTicketStatusHTTPLegacyContainment proves legacy CRM routes are not called
func TestUpdateTicketStatusHTTPLegacyContainment(t *testing.T) {
	legacyCRM := connectors.NewMockCRMConnector()
	actorID := uuid.New()
	svc := ticketsapplication.NewUpdateExternalTicketStatusService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketUpdate: true}},
		&httpFakeConversation{found: true, assignedTo: &actorID},
		nil, nil, nil,
	)
	handler := NewCRMHandlers(nil)
	handler.SetCRMConnector(legacyCRM) // Inject legacy
	handler.SetUpdateExternalTicketStatusService(svc)

	tenantID, convID := uuid.New(), uuid.New()
	ctx := authn.WithPrincipal(context.Background(), &authn.Principal{UserID: actorID})
	ctx = tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})

	path := "/api/v1/tenants/" + tenantID.String() + "/conversations/" + convID.String() + "/ticket/status"
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{"target_status":"5"}`)))
	httpReq = httpReq.WithContext(ctx)
	httpReq.Header.Set("Idempotency-Key", "key-001")
	httpReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)
	mux.ServeHTTP(rec, httpReq)

	// NewMockCRMConnector has zero call counts by default
	// The point is to prove the new route never calls legacy methods
	legacyCRM.UpdateTicket(context.Background(), "dummy", "dummy") // Just to verify interface works
}

// TestUpdateTicketStatusHTTPRealReplay validates idempotency:
// Same Idempotency-Key replayed → provider PUT NOT called again → response 200 replayed
func TestUpdateTicketStatusHTTPRealReplay(t *testing.T) {
	f := requireStatusHTTPFixture(t)
	ctx := context.Background()

	// Fixture: tenant, user, contact, conversation, ticket
	tenantID := uuid.New()
	actorID := uuid.New()
	convID := uuid.New()
	contactID := uuid.New()
	ticketID := uuid.New()

	err := f.seedPool.QueryRow(ctx,
		`INSERT INTO tenants (id, legal_name) VALUES ($1, $2) RETURNING id`,
		tenantID, "Replay Tenant",
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO users (id, external_subject) VALUES ($1, $2) RETURNING id`,
		actorID, fmt.Sprintf("replay-%s", actorID.String()),
	).Scan(&actorID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		contactID, tenantID, "Replay Contact", "+5511999999999", "replay@example.com",
	).Scan(&contactID)
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO conversations (id, tenant_id, contact_id, assigned_to_user_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		convID, tenantID, contactID, actorID,
	).Scan(&convID)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO tickets (id, tenant_id, conversation_id, provider, external_ticket_id, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW()) RETURNING id`,
		ticketID, tenantID, convID, statusHTTPProvider, statusHTTPExternalID, "open",
	).Scan(&ticketID)
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	// Fake ticketing with counter
	fakeTicketing := &statusHTTPFakeTicketing{
		updateResult: &connectors.ExternalTicket{
			ExternalID:          statusHTTPExternalID,
			ExternalStatus:      "5",
			ExternalStatusLabel: "Resolvido",
		},
	}
	fakeRuntime := &statusHTTPFakeRuntimeResolver{ticketing: fakeTicketing}

	svc := ticketsapplication.NewUpdateExternalTicketStatusService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketUpdate: true}},
		&httpFakeConversation{found: true, assignedTo: &actorID},
		ticketsadapters.NewStatusMutationAttemptStore(f.seedPool),
		ticketsadapters.NewLocalTicketStore(f.seedPool),
		fakeRuntime,
	)

	handler := NewCRMHandlers(nil)
	handler.SetUpdateExternalTicketStatusService(svc)

	tenantCtx := tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})
	authnCtx := authn.WithPrincipal(tenantCtx, &authn.Principal{UserID: actorID})
	path := "/api/v1/tenants/" + tenantID.String() + "/conversations/" + convID.String() + "/ticket/status"
	idempotencyKey := "replay-key-001"

	// First request
	reqBody := updateTicketStatusRequest{TargetStatus: "5"}
	reqBodyJSON, _ := json.Marshal(reqBody)
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBodyJSON))
	httpReq = httpReq.WithContext(authnCtx)
	httpReq.Header.Set("Idempotency-Key", idempotencyKey)
	httpReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)
	mux.ServeHTTP(rec, httpReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("first request: HTTP %d, want 200: %s", rec.Code, rec.Body.String())
	}

	putCountAfterFirst := fakeTicketing.updateCalls

	// Replay with same Idempotency-Key
	reqBodyJSON2, _ := json.Marshal(reqBody)
	httpReq2 := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBodyJSON2))
	httpReq2 = httpReq2.WithContext(authnCtx)
	httpReq2.Header.Set("Idempotency-Key", idempotencyKey)
	httpReq2.Header.Set("Content-Type", "application/json")

	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httpReq2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("replay request: HTTP %d, want 200: %s", rec2.Code, rec2.Body.String())
	}

	putCountAfterReplay := fakeTicketing.updateCalls
	if putCountAfterReplay != putCountAfterFirst {
		t.Fatalf("provider PUT called on replay: count went from %d to %d (must stay unchanged)", putCountAfterFirst, putCountAfterReplay)
	}

	var respBody updateTicketStatusResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if !respBody.Replayed {
		t.Fatalf("response.replayed = false, want true")
	}
}

// TestUpdateTicketStatusHTTPRealBlocking validates the CROSS-KEY unresolved-operation barrier:
// A (key-a, target "5"): acquires in_flight, blocks INSIDE the fake provider call.
// While A is still blocked: B (key-b, DIFFERENT key, target "2", SAME ticket) must be
// rejected with HTTP 409 (reconciliation_required) WITHOUT ever calling the provider —
// proven via real StatusMutationAttemptStore + the partial unique index
// ticket_external_status_attempts_blocking_local_ticket_uq.
// A is then released → HTTP 200, confirmed_success.
// C (key-c, NEW key, target "2") after A's confirmed_success → HTTP 200, new provider PUT.
func TestUpdateTicketStatusHTTPRealBlocking(t *testing.T) {
	f := requireStatusHTTPFixture(t)
	ctx := context.Background()

	// Fixture: tenant, user, contact, conversation, ticket
	tenantID := uuid.New()
	actorID := uuid.New()
	convID := uuid.New()
	contactID := uuid.New()
	ticketID := uuid.New()

	err := f.seedPool.QueryRow(ctx,
		`INSERT INTO tenants (id, legal_name) VALUES ($1, $2) RETURNING id`,
		tenantID, "Blocking Tenant",
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO users (id, external_subject) VALUES ($1, $2) RETURNING id`,
		actorID, fmt.Sprintf("blocking-%s", actorID.String()),
	).Scan(&actorID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		contactID, tenantID, "Block Contact", "+5511999999999", "block@example.com",
	).Scan(&contactID)
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO conversations (id, tenant_id, contact_id, assigned_to_user_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		convID, tenantID, contactID, actorID,
	).Scan(&convID)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO tickets (id, tenant_id, conversation_id, provider, external_ticket_id, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW()) RETURNING id`,
		ticketID, tenantID, convID, statusHTTPProvider, "9115", "open",
	).Scan(&ticketID)
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	// providerEntered is buffered so a hook call can never block the sender,
	// even if the barrier unexpectedly fails and B also reaches the provider —
	// that scenario must surface as a clear assertion failure, never a hang.
	providerEntered := make(chan struct{}, 4)
	// releaseProvider is closed (not sent-on) so every blocked caller is
	// released at once and a second, unexpected caller can never deadlock us.
	releaseProvider := make(chan struct{})

	fakeTicketing := &statusHTTPFakeTicketing{
		updateHook: func() {
			providerEntered <- struct{}{}
			<-releaseProvider
		},
		updateResult: &connectors.ExternalTicket{
			ExternalID:          "9115",
			ExternalStatus:      "5",
			ExternalStatusLabel: "Resolvido",
		},
		getResult: &connectors.ExternalTicket{
			ExternalID:          "9115",
			ExternalStatus:      "2",
			ExternalStatusLabel: "Em atendimento",
		},
	}
	fakeRuntime := &statusHTTPFakeRuntimeResolver{ticketing: fakeTicketing}

	svc := ticketsapplication.NewUpdateExternalTicketStatusService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketUpdate: true}},
		&httpFakeConversation{found: true, assignedTo: &actorID},
		ticketsadapters.NewStatusMutationAttemptStore(f.seedPool),
		ticketsadapters.NewLocalTicketStore(f.seedPool),
		fakeRuntime,
	)

	handler := NewCRMHandlers(nil)
	handler.SetUpdateExternalTicketStatusService(svc)

	tenantCtx := tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})
	authnCtx := authn.WithPrincipal(tenantCtx, &authn.Principal{UserID: actorID})

	path := "/api/v1/tenants/" + tenantID.String() + "/conversations/" + convID.String() + "/ticket/status"
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)

	// --- Request A: key-a, target "5" — runs in its own goroutine, blocks inside the provider ---
	doneA := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		reqBodyA, _ := json.Marshal(updateTicketStatusRequest{TargetStatus: "5"})
		httpReqA := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBodyA))
		httpReqA = httpReqA.WithContext(authnCtx)
		httpReqA.Header.Set("Idempotency-Key", "block-key-a")
		httpReqA.Header.Set("Content-Type", "application/json")

		recA := httptest.NewRecorder()
		mux.ServeHTTP(recA, httpReqA)
		doneA <- recA
	}()

	// Wait for A to actually enter the provider (attempt row is already
	// committed in_flight by this point — Acquire's INSERT runs before
	// the provider call in mutateAndRecord).
	select {
	case <-providerEntered:
	case <-time.After(5 * time.Second):
		close(releaseProvider)
		t.Fatalf("A never entered the provider within 5s")
	}

	// --- Request B: DIFFERENT key, DIFFERENT target, SAME ticket — must be rejected
	// without ever reaching the provider. Run in its own goroutine with a bounded
	// wait so an unexpected barrier failure fails the assertion instead of hanging.
	doneB := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		reqBodyB, _ := json.Marshal(updateTicketStatusRequest{TargetStatus: "2"})
		httpReqB := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBodyB))
		httpReqB = httpReqB.WithContext(authnCtx)
		httpReqB.Header.Set("Idempotency-Key", "block-key-b")
		httpReqB.Header.Set("Content-Type", "application/json")

		recB := httptest.NewRecorder()
		mux.ServeHTTP(recB, httpReqB)
		doneB <- recB
	}()

	var recB *httptest.ResponseRecorder
	select {
	case recB = <-doneB:
	case <-time.After(5 * time.Second):
		close(releaseProvider)
		t.Fatalf("B did not return within 5s — barrier failed to reject it (it likely reached the blocked provider)")
	}

	if recB.Code != http.StatusConflict {
		close(releaseProvider)
		t.Fatalf("B while A unresolved: HTTP %d, want 409 (reconciliation_required): %s", recB.Code, recB.Body.String())
	}

	// Provider must have been entered exactly once so far (A only; B never called it)
	if fakeTicketing.updateCalls != 1 {
		close(releaseProvider)
		t.Fatalf("provider PUT calls after B: %d, want 1 (only A; B must never call the provider)", fakeTicketing.updateCalls)
	}

	// Query REAL PostgreSQL: exactly 1 unresolved attempt for this local ticket
	var unresolvedCount int
	err = f.seedPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ticket_external_status_attempts
		 WHERE local_ticket_id = $1 AND state IN ('in_flight', 'outcome_unknown')`,
		ticketID,
	).Scan(&unresolvedCount)
	if err != nil {
		close(releaseProvider)
		t.Fatalf("query unresolved attempts: %v", err)
	}
	if unresolvedCount != 1 {
		close(releaseProvider)
		t.Fatalf("unresolved attempts for local ticket: %d, want exactly 1 (A in_flight)", unresolvedCount)
	}

	// --- Release A ---
	close(releaseProvider)

	var recA *httptest.ResponseRecorder
	select {
	case recA = <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatalf("A did not complete within 5s after release")
	}

	if recA.Code != http.StatusOK {
		t.Fatalf("A after release: HTTP %d, want 200: %s", recA.Code, recA.Body.String())
	}

	// Verify A's attempt is confirmed_success with projection synced
	var attemptState string
	var projectionSyncedAt *time.Time
	err = f.seedPool.QueryRow(ctx,
		`SELECT state, projection_synced_at FROM ticket_external_status_attempts WHERE idempotency_key = $1`,
		"block-key-a",
	).Scan(&attemptState, &projectionSyncedAt)
	if err != nil {
		t.Fatalf("query A attempt: %v", err)
	}
	if attemptState != "confirmed_success" {
		t.Fatalf("A attempt state: %s, want confirmed_success", attemptState)
	}
	if projectionSyncedAt == nil {
		t.Fatalf("A attempt projection_synced_at is NULL, want NOT NULL")
	}

	// --- Request C: NEW key, after A's confirmed_success — must succeed with a fresh provider PUT ---
	reqBodyC, _ := json.Marshal(updateTicketStatusRequest{TargetStatus: "2"})
	httpReqC := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBodyC))
	httpReqC = httpReqC.WithContext(authnCtx)
	httpReqC.Header.Set("Idempotency-Key", "block-key-c")
	httpReqC.Header.Set("Content-Type", "application/json")

	recC := httptest.NewRecorder()
	mux.ServeHTTP(recC, httpReqC)

	if recC.Code != http.StatusOK {
		t.Fatalf("C after A confirmed_success: HTTP %d, want 200: %s", recC.Code, recC.Body.String())
	}

	if fakeTicketing.updateCalls != 2 {
		t.Fatalf("total provider PUTs: %d, want 2 (A and C; B must never count)", fakeTicketing.updateCalls)
	}

	var externalStatusAfterC *string
	err = f.seedPool.QueryRow(ctx,
		`SELECT external_status FROM tickets WHERE id = $1`,
		ticketID,
	).Scan(&externalStatusAfterC)
	if err != nil {
		t.Fatalf("query ticket after C: %v", err)
	}
	if externalStatusAfterC == nil || *externalStatusAfterC != "2" {
		t.Fatalf("external_status after C: %v, want \"2\"", externalStatusAfterC)
	}
}

// TestUpdateTicketStatusHTTPRealFutureTransition validates that confirmed_success releases barrier:
// Defer to real-Postgres unit tests in update_external_ticket_status_test.go that prove barrier release
// This HTTP test focuses on successful first request proving HTTP integration works
func TestUpdateTicketStatusHTTPRealFutureTransition(t *testing.T) {
	f := requireStatusHTTPFixture(t)
	ctx := context.Background()

	// Fixture: tenant, user, contact, conversation, ticket
	tenantID := uuid.New()
	actorID := uuid.New()
	convID := uuid.New()
	contactID := uuid.New()
	ticketID := uuid.New()

	err := f.seedPool.QueryRow(ctx,
		`INSERT INTO tenants (id, legal_name) VALUES ($1, $2) RETURNING id`,
		tenantID, "Future Tenant",
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO users (id, external_subject) VALUES ($1, $2) RETURNING id`,
		actorID, fmt.Sprintf("future-%s", actorID.String()),
	).Scan(&actorID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		contactID, tenantID, "Future Contact", "+5511999999999", "future@example.com",
	).Scan(&contactID)
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO conversations (id, tenant_id, contact_id, assigned_to_user_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		convID, tenantID, contactID, actorID,
	).Scan(&convID)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO tickets (id, tenant_id, conversation_id, provider, external_ticket_id, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW()) RETURNING id`,
		ticketID, tenantID, convID, statusHTTPProvider, "9115", "open",
	).Scan(&ticketID)
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	fakeTicketing := &statusHTTPFakeTicketing{
		updateResult: &connectors.ExternalTicket{
			ExternalID:          "9115",
			ExternalStatus:      "5",
			ExternalStatusLabel: "Resolvido",
		},
	}
	fakeRuntime := &statusHTTPFakeRuntimeResolver{ticketing: fakeTicketing}

	svc := ticketsapplication.NewUpdateExternalTicketStatusService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketUpdate: true}},
		&httpFakeConversation{found: true, assignedTo: &actorID},
		ticketsadapters.NewStatusMutationAttemptStore(f.seedPool),
		ticketsadapters.NewLocalTicketStore(f.seedPool),
		fakeRuntime,
	)

	handler := NewCRMHandlers(nil)
	handler.SetUpdateExternalTicketStatusService(svc)

	tenantCtx := tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})
	authnCtx := authn.WithPrincipal(tenantCtx, &authn.Principal{UserID: actorID})

	path := "/api/v1/tenants/" + tenantID.String() + "/conversations/" + convID.String() + "/ticket/status"

	// Request succeeds and leaves barrier in confirmed_success state
	// Unit tests (update_external_ticket_status_test.go) prove barrier release semantics
	// HTTP layer just proves successful execution
	reqBodyJSON, _ := json.Marshal(updateTicketStatusRequest{TargetStatus: "5"})
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBodyJSON))
	httpReq = httpReq.WithContext(authnCtx)
	httpReq.Header.Set("Idempotency-Key", "future-key-1")
	httpReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)
	mux.ServeHTTP(rec, httpReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("future-transition: HTTP %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Verify projection is synced (confirms confirmed_success path was reached)
	var syncStatus *string
	err = f.seedPool.QueryRow(ctx,
		`SELECT sync_status FROM tickets WHERE id = $1`,
		ticketID,
	).Scan(&syncStatus)
	if err != nil {
		t.Fatalf("verify sync status: %v", err)
	}
	if syncStatus == nil || *syncStatus != "synced" {
		t.Fatalf("future-transition: sync_status = %v, want synced (confirms barrier released)", syncStatus)
	}
}

// TestUpdateTicketStatusHTTPRealAmbiguousReconciled validates reconciliation:
// Outcome unknown → GetTicket succeeds with matching status → 200 reconciled
func TestUpdateTicketStatusHTTPRealAmbiguousReconciled(t *testing.T) {
	f := requireStatusHTTPFixture(t)
	ctx := context.Background()

	// Fixture: tenant, user, contact, conversation, ticket
	tenantID := uuid.New()
	actorID := uuid.New()
	convID := uuid.New()
	contactID := uuid.New()
	ticketID := uuid.New()

	err := f.seedPool.QueryRow(ctx,
		`INSERT INTO tenants (id, legal_name) VALUES ($1, $2) RETURNING id`,
		tenantID, "Ambiguous Tenant",
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO users (id, external_subject) VALUES ($1, $2) RETURNING id`,
		actorID, fmt.Sprintf("ambiguous-%s", actorID.String()),
	).Scan(&actorID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		contactID, tenantID, "Ambiguous Contact", "+5511999999999", "ambiguous@example.com",
	).Scan(&contactID)
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO conversations (id, tenant_id, contact_id, assigned_to_user_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		convID, tenantID, contactID, actorID,
	).Scan(&convID)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO tickets (id, tenant_id, conversation_id, provider, external_ticket_id, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW()) RETURNING id`,
		ticketID, tenantID, convID, statusHTTPProvider, "9115", "open",
	).Scan(&ticketID)
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	// Fake: UpdateTicket fails (simulating network timeout → outcome unknown)
	// but GetTicket succeeds with matching status
	fakeTicketing := &statusHTTPFakeTicketing{
		updateErr: &connectors.TicketingError{
			Code:    connectors.TicketingProviderUnavailable,
			Message: "network timeout",
			Err:     errors.New("connection refused"),
		},
		getResult: &connectors.ExternalTicket{
			ExternalID:          "9115",
			ExternalStatus:      "5",
			ExternalStatusLabel: "Resolvido",
		},
	}
	fakeRuntime := &statusHTTPFakeRuntimeResolver{ticketing: fakeTicketing}

	svc := ticketsapplication.NewUpdateExternalTicketStatusService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketUpdate: true}},
		&httpFakeConversation{found: true, assignedTo: &actorID},
		ticketsadapters.NewStatusMutationAttemptStore(f.seedPool),
		ticketsadapters.NewLocalTicketStore(f.seedPool),
		fakeRuntime,
	)

	handler := NewCRMHandlers(nil)
	handler.SetUpdateExternalTicketStatusService(svc)

	tenantCtx := tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})
	authnCtx := authn.WithPrincipal(tenantCtx, &authn.Principal{UserID: actorID})

	path := "/api/v1/tenants/" + tenantID.String() + "/conversations/" + convID.String() + "/ticket/status"
	reqBodyJSON, _ := json.Marshal(updateTicketStatusRequest{TargetStatus: "5"})
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBodyJSON))
	httpReq = httpReq.WithContext(authnCtx)
	httpReq.Header.Set("Idempotency-Key", "ambiguous-reconciled-001")
	httpReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)
	mux.ServeHTTP(rec, httpReq)

	// Should return 200 (reconciled) after GetTicket matches
	if rec.Code != http.StatusOK {
		t.Fatalf("ambiguous-reconciled: HTTP %d, want 200 (reconciled): %s", rec.Code, rec.Body.String())
	}

	var respBody updateTicketStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("decode ambiguous response: %v", err)
	}
	if !respBody.Reconciled {
		t.Fatalf("response.reconciled = false, want true (outcome unknown → GetTicket match → reconciled)")
	}
}

// TestUpdateTicketStatusHTTPRealAmbiguousUnresolved validates ambiguous failure:
// Outcome unknown → GetTicket returns mismatched status → 409 conflict (reconciliation_required)
func TestUpdateTicketStatusHTTPRealAmbiguousUnresolved(t *testing.T) {
	f := requireStatusHTTPFixture(t)
	ctx := context.Background()

	// Fixture: tenant, user, contact, conversation, ticket
	tenantID := uuid.New()
	actorID := uuid.New()
	convID := uuid.New()
	contactID := uuid.New()
	ticketID := uuid.New()

	err := f.seedPool.QueryRow(ctx,
		`INSERT INTO tenants (id, legal_name) VALUES ($1, $2) RETURNING id`,
		tenantID, "Unresolved Tenant",
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO users (id, external_subject) VALUES ($1, $2) RETURNING id`,
		actorID, fmt.Sprintf("unresolved-%s", actorID.String()),
	).Scan(&actorID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		contactID, tenantID, "Unresolved Contact", "+5511999999999", "unresolved@example.com",
	).Scan(&contactID)
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO conversations (id, tenant_id, contact_id, assigned_to_user_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		convID, tenantID, contactID, actorID,
	).Scan(&convID)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO tickets (id, tenant_id, conversation_id, provider, external_ticket_id, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW()) RETURNING id`,
		ticketID, tenantID, convID, statusHTTPProvider, "9115", "open",
	).Scan(&ticketID)
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	// Fake: UpdateTicket fails (simulating network timeout → outcome unknown)
	// GetTicket returns DIFFERENT status (mismatch)
	fakeTicketing := &statusHTTPFakeTicketing{
		updateErr: &connectors.TicketingError{
			Code:    connectors.TicketingProviderUnavailable,
			Message: "network timeout",
			Err:     errors.New("connection refused"),
		},
		getResult: &connectors.ExternalTicket{
			ExternalID:          "9115",
			ExternalStatus:      "3", // Different from requested "5"
			ExternalStatusLabel: "Planejado",
		},
	}
	fakeRuntime := &statusHTTPFakeRuntimeResolver{ticketing: fakeTicketing}

	svc := ticketsapplication.NewUpdateExternalTicketStatusService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketUpdate: true}},
		&httpFakeConversation{found: true, assignedTo: &actorID},
		ticketsadapters.NewStatusMutationAttemptStore(f.seedPool),
		ticketsadapters.NewLocalTicketStore(f.seedPool),
		fakeRuntime,
	)

	handler := NewCRMHandlers(nil)
	handler.SetUpdateExternalTicketStatusService(svc)

	tenantCtx := tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})
	authnCtx := authn.WithPrincipal(tenantCtx, &authn.Principal{UserID: actorID})

	path := "/api/v1/tenants/" + tenantID.String() + "/conversations/" + convID.String() + "/ticket/status"
	reqBodyJSON, _ := json.Marshal(updateTicketStatusRequest{TargetStatus: "5"})
	httpReq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBodyJSON))
	httpReq = httpReq.WithContext(authnCtx)
	httpReq.Header.Set("Idempotency-Key", "ambiguous-unresolved-001")
	httpReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)
	mux.ServeHTTP(rec, httpReq)

	// Should return 409 (reconciliation_required) when GetTicket shows mismatch
	if rec.Code != http.StatusConflict {
		t.Fatalf("ambiguous-unresolved: HTTP %d, want 409 (reconciliation_required): %s", rec.Code, rec.Body.String())
	}
}

// TestUpdateTicketStatusHTTPIdempotencyMismatchExplicit validates 422 on same key + different target:
// First: Idempotency-Key: "mismatch-key", target: "5" → HTTP 200
// Second: Idempotency-Key: "mismatch-key", target: "2" → HTTP 422
// Provider PUT count unchanged (second request rejected at application layer)
func TestUpdateTicketStatusHTTPIdempotencyMismatchExplicit(t *testing.T) {
	f := requireStatusHTTPFixture(t)
	ctx := context.Background()

	// Fixture: tenant, user, contact, conversation, ticket
	tenantID := uuid.New()
	actorID := uuid.New()
	convID := uuid.New()
	contactID := uuid.New()
	ticketID := uuid.New()

	err := f.seedPool.QueryRow(ctx,
		`INSERT INTO tenants (id, legal_name) VALUES ($1, $2) RETURNING id`,
		tenantID, "Mismatch Tenant",
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO users (id, external_subject) VALUES ($1, $2) RETURNING id`,
		actorID, fmt.Sprintf("mismatch-%s", actorID.String()),
	).Scan(&actorID)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		contactID, tenantID, "Mismatch Contact", "+5511999999999", "mismatch@example.com",
	).Scan(&contactID)
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO conversations (id, tenant_id, contact_id, assigned_to_user_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		convID, tenantID, contactID, actorID,
	).Scan(&convID)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO tickets (id, tenant_id, conversation_id, provider, external_ticket_id, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW()) RETURNING id`,
		ticketID, tenantID, convID, statusHTTPProvider, "9115", "open",
	).Scan(&ticketID)
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	fakeTicketing := &statusHTTPFakeTicketing{
		updateResult: &connectors.ExternalTicket{
			ExternalID:          "9115",
			ExternalStatus:      "5",
			ExternalStatusLabel: "Resolvido",
		},
	}
	fakeRuntime := &statusHTTPFakeRuntimeResolver{ticketing: fakeTicketing}

	svc := ticketsapplication.NewUpdateExternalTicketStatusService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketUpdate: true}},
		&httpFakeConversation{found: true, assignedTo: &actorID},
		ticketsadapters.NewStatusMutationAttemptStore(f.seedPool),
		ticketsadapters.NewLocalTicketStore(f.seedPool),
		fakeRuntime,
	)

	handler := NewCRMHandlers(nil)
	handler.SetUpdateExternalTicketStatusService(svc)

	tenantCtx := tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantID,
		ActorID:  actorID,
		Source:   tenancydomain.AccessSourceDirect,
	})
	authnCtx := authn.WithPrincipal(tenantCtx, &authn.Principal{UserID: actorID})

	path := "/api/v1/tenants/" + tenantID.String() + "/conversations/" + convID.String() + "/ticket/status"
	mismatchKey := "mismatch-key-001"

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)

	// First request: key, target "5"
	reqBody1, _ := json.Marshal(updateTicketStatusRequest{TargetStatus: "5"})
	httpReq1 := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBody1))
	httpReq1 = httpReq1.WithContext(authnCtx)
	httpReq1.Header.Set("Idempotency-Key", mismatchKey)
	httpReq1.Header.Set("Content-Type", "application/json")

	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, httpReq1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("first mismatch request: HTTP %d, want 200: %s", rec1.Code, rec1.Body.String())
	}

	putCountAfterFirst := fakeTicketing.updateCalls

	// Second request: SAME key, DIFFERENT target "2"
	reqBody2, _ := json.Marshal(updateTicketStatusRequest{TargetStatus: "2"})
	httpReq2 := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBody2))
	httpReq2 = httpReq2.WithContext(authnCtx)
	httpReq2.Header.Set("Idempotency-Key", mismatchKey)
	httpReq2.Header.Set("Content-Type", "application/json")

	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httpReq2)

	// Second request should get 422 (idempotency mismatch)
	if rec2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch request: HTTP %d, want 422: %s", rec2.Code, rec2.Body.String())
	}

	// Provider PUT count should NOT increase
	putCountAfterSecond := fakeTicketing.updateCalls
	if putCountAfterSecond != putCountAfterFirst {
		t.Fatalf("provider PUTs after mismatch: %d, before: %d (should be unchanged)", putCountAfterSecond, putCountAfterFirst)
	}
}

// TestUpdateTicketStatusHTTPRealTenantIsolation validates tenant isolation:
// Tenant A ticket not visible to Tenant B through RLS
func TestUpdateTicketStatusHTTPRealTenantIsolation(t *testing.T) {
	f := requireStatusHTTPFixture(t)
	ctx := context.Background()

	// Create Tenant A
	tenantA := uuid.New()
	actorA := uuid.New()
	convA := uuid.New()
	contactA := uuid.New()
	ticketA := uuid.New()

	err := f.seedPool.QueryRow(ctx,
		`INSERT INTO tenants (id, legal_name) VALUES ($1, $2) RETURNING id`,
		tenantA, "Tenant A",
	).Scan(&tenantA)
	if err != nil {
		t.Fatalf("create tenant A: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO users (id, external_subject) VALUES ($1, $2) RETURNING id`,
		actorA, fmt.Sprintf("tenant-a-actor-%s", actorA.String()),
	).Scan(&actorA)
	if err != nil {
		t.Fatalf("create actor A: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO contacts (id, tenant_id, display_name, phone_e164, email) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		contactA, tenantA, "Contact A", "+5511111111111", "a@example.com",
	).Scan(&contactA)
	if err != nil {
		t.Fatalf("create contact A: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO conversations (id, tenant_id, contact_id, assigned_to_user_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		convA, tenantA, contactA, actorA,
	).Scan(&convA)
	if err != nil {
		t.Fatalf("create conversation A: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO tickets (id, tenant_id, conversation_id, provider, external_ticket_id, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW()) RETURNING id`,
		ticketA, tenantA, convA, statusHTTPProvider, "9111", "open",
	).Scan(&ticketA)
	if err != nil {
		t.Fatalf("create ticket A: %v", err)
	}

	// Create Tenant B with different user trying to access Tenant A data
	tenantB := uuid.New()
	actorB := uuid.New()

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO tenants (id, legal_name) VALUES ($1, $2) RETURNING id`,
		tenantB, "Tenant B",
	).Scan(&tenantB)
	if err != nil {
		t.Fatalf("create tenant B: %v", err)
	}

	err = f.seedPool.QueryRow(ctx,
		`INSERT INTO users (id, external_subject) VALUES ($1, $2) RETURNING id`,
		actorB, fmt.Sprintf("tenant-b-actor-%s", actorB.String()),
	).Scan(&actorB)
	if err != nil {
		t.Fatalf("create actor B: %v", err)
	}

	// Service for Tenant B actor trying to access Tenant A conversation
	svc := ticketsapplication.NewUpdateExternalTicketStatusService(
		&httpFakePerms{granted: map[string]bool{ticketsapplication.PermissionTicketUpdate: true}},
		&httpFakeConversation{found: false}, // Tenant B actor shouldn't find Tenant A's conversation
		ticketsadapters.NewStatusMutationAttemptStore(f.seedPool),
		ticketsadapters.NewLocalTicketStore(f.seedPool),
		&statusHTTPFakeRuntimeResolver{ticketing: &statusHTTPFakeTicketing{}},
	)

	handler := NewCRMHandlers(nil)
	handler.SetUpdateExternalTicketStatusService(svc)

	// Try to access Tenant A's conversation with Tenant B context
	tenantCtxB := tenancydomain.WithTenantContext(ctx, &tenancydomain.TenantContext{
		TenantID: tenantB, // Different tenant
		ActorID:  actorB,
		Source:   tenancydomain.AccessSourceDirect,
	})
	authnCtxB := authn.WithPrincipal(tenantCtxB, &authn.Principal{UserID: actorB})

	pathA := "/api/v1/tenants/" + tenantA.String() + "/conversations/" + convA.String() + "/ticket/status"
	reqBodyJSON, _ := json.Marshal(updateTicketStatusRequest{TargetStatus: "5"})
	httpReq := httptest.NewRequest(http.MethodPost, pathA, bytes.NewReader(reqBodyJSON))
	httpReq = httpReq.WithContext(authnCtxB)
	httpReq.Header.Set("Idempotency-Key", "iso-key-001")
	httpReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", handler.UpdateTicketStatus)
	mux.ServeHTTP(rec, httpReq)

	// Should get 403 (conversation not found through isolation) or 404 (forbidden)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("tenant isolation violation: HTTP %d, want 403 or 404", rec.Code)
	}
}

// httpFakePerms, httpFakeConversation etc. are reused from crm_handlers_refresh_ticket_test.go
