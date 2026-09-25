package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsapplication "github.com/omnira/omnira/internal/tickets/application"
	ticketsports "github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// CRMTicketRequest — payload para criar/atualizar ticket
type CRMTicketRequest struct {
	Subject string `json:"subject"`
	Status  string `json:"status,omitempty"`
}

// CRMTicketResponse — resposta de ticket
type CRMTicketResponse struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Subject   string `json:"subject"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// CRMHandlers — handlers para CRM
type CRMHandlers struct {
	dbPool *pgxpool.Pool
	crm    connectors.CRMConnector
	// companyDirectoryResolver is the tenant-scoped K3G runtime resolver
	// (PRODUCT.7B1A). Reuses PRODUCT.6-L's ticketsports.TicketingRuntimeResolver
	// exactly as-is. Used ONLY by ListCompanies today: CreateActivity does
	// NOT use it (PRODUCT.7B1B security correction — see CreateActivity's
	// containment comment: CompanyDirectory membership is not sufficient
	// authorization for an external write). Replaces the confirmed
	// PRODUCT.7B P0: a single globally-bootstrapped k3gClient shared by
	// every tenant — fully retired, no remaining usage in this file.
	companyDirectoryResolver ticketsports.TicketingRuntimeResolver
	// activityConversations loads the canonical, tenant-scoped conversation
	// facts CreateActivity needs (PRODUCT.7B1B): who it is assigned to
	// (authorization) and its persisted crm_contact_id. CreateActivity is
	// currently CONTAINED (never calls a provider) — see its doc comment —
	// but authorization and the crm_contact_id precondition are still real,
	// enforced checks, never req.ContactID (which no longer exists in the
	// request contract).
	activityConversations activityConversationReader
	// activityPermissions checks conversation.manage for CreateActivity's
	// "assignee or conversation.manage" authorization (PRODUCT.7B1B) — the
	// same primitive CreateExternalTicket/Send already use. This route
	// previously had NO authorization beyond "authenticated".
	activityPermissions ticketsports.PermissionChecker
	// externalTicketService is the real, tenant-scoped external ticket
	// creation path (PRODUCT.6-M). Nil until server.go wires it — the
	// canonical runtime composition root, never a handler-built service
	// graph.
	externalTicketService externalTicketCreator
	// readTicketService is the real, conversation-scoped, LOCAL-ONLY
	// ticket read path (PRODUCT.6-O1). Nil until server.go wires it.
	readTicketService conversationTicketReader
	// refreshTicketService is the real, conversation-scoped, EXPLICIT
	// provider projection refresh path (PRODUCT.6-O1R). Nil until
	// server.go wires it.
	refreshTicketService ticketProjectionRefresher
	// updateExternalTicketStatusService is the real, conversation-scoped,
	// EXTERNAL STATUS MUTATION path (PRODUCT.6-O2B3). Nil until server.go
	// wires it.
	updateExternalTicketStatusService externalTicketStatusMutator
}

// NewCRMHandlers — cria novo CRM handler.
//
// PRODUCT.6-B: no ticketing connector is installed here. There is no
// tenant-scoped ERP ticket-provider resolution/configuration model yet
// (IXCConnector implements the real interface but is intentionally left
// unwired — see docs/adr/... future ERP ticket projection ADR). Until a
// real per-tenant connector exists, every ticket CRUD request must fail
// explicitly rather than silently succeed against an in-memory fake —
// see ticketingUnavailable / errTicketingNotConfigured below.
func NewCRMHandlers(dbPool *pgxpool.Pool) *CRMHandlers {
	return &CRMHandlers{dbPool: dbPool}
}

// SetCRMConnector — test-only injection point. Canonical runtime
// composition (server.go) must never call this: a mock/fake ticketing
// connector must not become product runtime authority (PRODUCT.6-B).
func (h *CRMHandlers) SetCRMConnector(crm connectors.CRMConnector) {
	h.crm = crm
}

// SetCompanyDirectoryResolver wires the tenant-scoped K3G runtime resolver
// ListCompanies uses (PRODUCT.7B1A). CreateActivity does NOT use this —
// see its containment comment (PRODUCT.7B1B). Canonical runtime
// composition (server.go/main.go) passes the SAME
// *ticketsadapters.K3GTicketingRuntimeResolver instance already
// constructed for ticketing (REUSE, not a second resolver/credential);
// tests may inject a fake satisfying ticketsports.TicketingRuntimeResolver.
func (h *CRMHandlers) SetCompanyDirectoryResolver(resolver ticketsports.TicketingRuntimeResolver) {
	h.companyDirectoryResolver = resolver
}

// SetActivityConversationReader wires the canonical, tenant-scoped
// conversation reader CreateActivity uses to derive crm_contact_id and
// check assignment (PRODUCT.7B1B). Canonical runtime composition
// (server.go/main.go) passes a *PostgresActivityConversations; tests may
// inject a fake satisfying activityConversationReader.
func (h *CRMHandlers) SetActivityConversationReader(reader activityConversationReader) {
	h.activityConversations = reader
}

// SetActivityPermissionChecker wires CreateActivity's conversation.manage
// check (PRODUCT.7B1B). Canonical runtime composition (server.go/main.go)
// passes the SAME channeladapters.PostgresPermissionChecker instance
// already used by ticketing/messaging (REUSE, not a new permission
// primitive); tests may inject a fake satisfying
// ticketsports.PermissionChecker.
func (h *CRMHandlers) SetActivityPermissionChecker(checker ticketsports.PermissionChecker) {
	h.activityPermissions = checker
}

// SetExternalTicketService wires the real CreateExternalTicket application
// service (PRODUCT.6-M). Canonical runtime composition (server.go/main.go)
// calls this with a Postgres/K3G-backed *ticketsapplication.Service; tests
// may inject a fake satisfying externalTicketCreator.
func (h *CRMHandlers) SetExternalTicketService(svc externalTicketCreator) {
	h.externalTicketService = svc
}

// SetReadTicketService wires the real, conversation-scoped ticket read
// path (PRODUCT.6-O1). Canonical runtime composition (server.go/main.go)
// calls this with a Postgres-backed
// *ticketsapplication.ReadConversationTicketService; tests may inject a
// fake satisfying conversationTicketReader.
func (h *CRMHandlers) SetReadTicketService(svc conversationTicketReader) {
	h.readTicketService = svc
}

// SetRefreshTicketService wires the real, conversation-scoped provider
// projection refresh path (PRODUCT.6-O1R). Canonical runtime composition
// (server.go/main.go) calls this with a Postgres/K3G-backed
// *ticketsapplication.RefreshTicketProjectionService; tests may inject a
// fake satisfying ticketProjectionRefresher.
func (h *CRMHandlers) SetRefreshTicketService(svc ticketProjectionRefresher) {
	h.refreshTicketService = svc
}

// updateExternalTicketStatusService wires the real UpdateExternalTicketStatus application
// service (PRODUCT.6-O2B3). Canonical runtime composition (server.go/main.go)
// calls this with a Postgres/K3G-backed *ticketsapplication.UpdateExternalTicketStatusService;
// tests may inject a fake.
func (h *CRMHandlers) SetUpdateExternalTicketStatusService(svc externalTicketStatusMutator) {
	h.updateExternalTicketStatusService = svc
}

// externalTicketStatusMutator is the seam PRODUCT.6-O2B3's UpdateTicketStatus
// calls through — narrow on purpose so HTTP tests can inject a fake
// without a real Postgres-backed application service.
// *ticketsapplication.UpdateExternalTicketStatusService satisfies this.
type externalTicketStatusMutator interface {
	UpdateExternalTicketStatus(ctx context.Context, cmd ticketsapplication.UpdateExternalTicketStatusCommand) (*ticketsapplication.StatusResult, error)
}

// ticketingUnavailable writes the canonical response for "no real ERP
// ticketing connector is configured for this tenant" — 503, matching the
// exact convention already used by the frontend for the same class of
// problem (web/src/lib/integrations.ts: "Integração indisponível: o
// servidor não está configurado para este provedor."). Never a fake
// 200/201, never a fallback to local-only storage.
func ticketingUnavailable(w http.ResponseWriter) {
	http.Error(w, "ticketing integration not configured for this tenant", http.StatusServiceUnavailable)
}

// conversationTicketReader is the seam PRODUCT.6-O1's GetCurrentTicket
// calls through — narrow on purpose so HTTP tests can inject a fake
// without a real Postgres-backed application service.
// *ticketsapplication.ReadConversationTicketService satisfies this without
// any adapter.
type conversationTicketReader interface {
	ReadConversationTicket(ctx context.Context, cmd ticketsapplication.ReadConversationTicketCommand) (*ticketsapplication.TicketReadResult, error)
}

// conversationTicketResponse is the provider-neutral read shape
// (PRODUCT.6-O1 section 5) — no raw K3G payload, no credential/Bearer
// identity, no K3G-specific field names. Nullable fields are simply
// omitted (empty string / absent) when Linked is false.
type conversationTicketResponse struct {
	LocalTicketID       uuid.UUID  `json:"local_ticket_id"`
	Linked              bool       `json:"linked"`
	Provider            string     `json:"provider,omitempty"`
	ExternalTicketID    string     `json:"external_ticket_id,omitempty"`
	ExternalStatus      string     `json:"external_status,omitempty"`
	ExternalStatusLabel string     `json:"external_status_label,omitempty"`
	SyncStatus          string     `json:"sync_status,omitempty"`
	LastSyncedAt        *time.Time `json:"last_synced_at,omitempty"`
}

// GetCurrentTicket — PRODUCT.6-O1: the real, conversation-scoped, LOCAL-ONLY
// ticket read. Authorized by conversation ownership (assignee or
// conversation.manage — the same primitive CreateExternalTicket uses),
// NEVER by tenant-wide ticket.read (PRODUCT.6-F: that permission stays
// reserved for the tenant-wide list/export surface, internal/tickets/
// adapters.Handler). Never calls the provider — a K3G outage or missing
// tenant configuration has no effect on this route (PRODUCT.6-O0 section 2:
// READ STRATEGY = LOCAL + EXPLICIT REFRESH; refresh is PRODUCT.6-O1R).
// Gated on h.readTicketService, never the legacy h.crm connector (which
// still gates the separately-contained GET-by-ticket-id/UPDATE/CLOSE
// routes below).
// GET /api/v1/tenants/{tenantId}/conversations/{conversationId}/ticket
func (h *CRMHandlers) GetCurrentTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.readTicketService == nil {
		ticketingUnavailable(w)
		return
	}

	tenantID, convID := r.PathValue("tenant_id"), r.PathValue("conversation_id")
	if tenantID == "" || convID == "" {
		http.Error(w, "missing tenant or conversation ID", http.StatusBadRequest)
		return
	}
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		http.Error(w, "invalid tenant ID", http.StatusBadRequest)
		return
	}
	cid, err := uuid.Parse(convID)
	if err != nil {
		http.Error(w, "invalid conversation ID", http.StatusBadRequest)
		return
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	result, err := h.readTicketService.ReadConversationTicket(ctx, ticketsapplication.ReadConversationTicketCommand{
		TenantID: tid, ConversationID: cid, ActorUserID: tc.ActorID,
	})
	if err != nil {
		failReadConversationTicket(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(conversationTicketResponse{
		LocalTicketID: result.LocalTicketID, Linked: result.Linked, Provider: result.Provider,
		ExternalTicketID: result.ExternalTicketID, ExternalStatus: result.ExternalStatus,
		ExternalStatusLabel: result.ExternalStatusLabel, SyncStatus: result.SyncStatus, LastSyncedAt: result.LastSyncedAt,
	})
}

// failReadConversationTicket maps ReadConversationTicket's application
// errors to HTTP — reusing the exact same authorization error identifiers
// CreateExternalTicket already defines (ErrForbidden, ErrConversationNotFound,
// ErrUnassigned, ErrNotAssignedToYou), since conversation-scoped
// authorization is one concept shared by both routes.
func failReadConversationTicket(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ticketsapplication.ErrForbidden), errors.Is(err, ticketsapplication.ErrUnassigned), errors.Is(err, ticketsapplication.ErrNotAssignedToYou):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, ticketsapplication.ErrConversationNotFound), errors.Is(err, ticketsapplication.ErrNoActiveTicket):
		http.Error(w, "no active ticket for this conversation", http.StatusNotFound)
	case errors.Is(err, ticketsapplication.ErrInconsistentExternalLink):
		http.Error(w, "ticket has inconsistent external linkage and requires reconciliation", http.StatusConflict)
	default:
		log.Printf("tickets read conversation ticket: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// ticketProjectionRefresher is the seam PRODUCT.6-O1R's RefreshTicket route
// calls through — narrow on purpose so HTTP tests can inject a fake without
// a real Postgres/K3G-backed application service.
// *ticketsapplication.RefreshTicketProjectionService satisfies this without
// any adapter.
type ticketProjectionRefresher interface {
	RefreshTicketProjection(ctx context.Context, cmd ticketsapplication.RefreshTicketProjectionCommand) (*ticketsapplication.TicketReadResult, error)
}

// RefreshTicket — PRODUCT.6-O1R: the real, conversation-scoped, EXPLICIT
// provider projection refresh. Authorized identically to CreateTicket
// (ticket.create + conversation ownership) — never tenant-wide ticket.read.
// Makes exactly one outbound TicketingConnector.GetTicket call and writes
// only provider-owned projection metadata; never creates, updates, or
// closes an external ticket, and never establishes a new link. A command,
// not a read — POST, never GET.
// POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/refresh
func (h *CRMHandlers) RefreshTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.refreshTicketService == nil {
		ticketingUnavailable(w)
		return
	}

	tenantID, convID := r.PathValue("tenant_id"), r.PathValue("conversation_id")
	if tenantID == "" || convID == "" {
		http.Error(w, "missing tenant or conversation ID", http.StatusBadRequest)
		return
	}
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		http.Error(w, "invalid tenant ID", http.StatusBadRequest)
		return
	}
	cid, err := uuid.Parse(convID)
	if err != nil {
		http.Error(w, "invalid conversation ID", http.StatusBadRequest)
		return
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	result, err := h.refreshTicketService.RefreshTicketProjection(ctx, ticketsapplication.RefreshTicketProjectionCommand{
		TenantID: tid, ConversationID: cid, ActorUserID: tc.ActorID,
	})
	if err != nil {
		failRefreshTicketProjection(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(conversationTicketResponse{
		LocalTicketID: result.LocalTicketID, Linked: result.Linked, Provider: result.Provider,
		ExternalTicketID: result.ExternalTicketID, ExternalStatus: result.ExternalStatus,
		ExternalStatusLabel: result.ExternalStatusLabel, SyncStatus: result.SyncStatus, LastSyncedAt: result.LastSyncedAt,
	})
}

// failRefreshTicketProjection maps RefreshTicketProjection's application
// errors to HTTP. No branch here ever implies CREATE is a valid recovery —
// every error either fails closed (409/403/404/503) or is an ordinary
// input problem (400).
func failRefreshTicketProjection(w http.ResponseWriter, err error) {
	var resErr *ticketsports.ResolutionError
	switch {
	case errors.As(err, &resErr):
		// PRODUCT.6-L: no/ambiguous/invalid tenant ticketing configuration.
		ticketingUnavailable(w)
	case errors.Is(err, ticketsapplication.ErrForbidden), errors.Is(err, ticketsapplication.ErrNotAssignedToYou):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, ticketsapplication.ErrConversationNotFound), errors.Is(err, ticketsapplication.ErrNoActiveTicket):
		http.Error(w, "no active ticket for this conversation", http.StatusNotFound)
	case errors.Is(err, ticketsapplication.ErrUnassigned):
		http.Error(w, "conversation must be assigned before its ticket can be refreshed", http.StatusConflict)
	case errors.Is(err, ticketsapplication.ErrTicketNotLinked):
		// Active local ticket exists but has no external link yet — refresh
		// is a freshness operation on an EXISTING link, never a path to
		// CREATE one.
		http.Error(w, "active ticket is not linked to an external ticket", http.StatusConflict)
	case errors.Is(err, ticketsapplication.ErrInconsistentExternalLink), errors.Is(err, ticketsapplication.ErrProviderMismatch),
		errors.Is(err, ticketsapplication.ErrExternalTicketNotFound), errors.Is(err, ticketsapplication.ErrExternalIDMismatch):
		http.Error(w, "ticket requires reconciliation before it can be refreshed", http.StatusConflict)
	case errors.Is(err, ticketsapplication.ErrProviderUnavailable):
		http.Error(w, "ticketing provider unavailable", http.StatusServiceUnavailable)
	default:
		log.Printf("tickets refresh projection: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// updateTicketStatusRequest — PRODUCT.6-O2B3 external status mutation body
type updateTicketStatusRequest struct {
	TargetStatus string `json:"target_status"`
}

// updateTicketStatusResponse — PRODUCT.6-O2B3 external status mutation response
type updateTicketStatusResponse struct {
	LocalTicketID       uuid.UUID  `json:"local_ticket_id"`
	Provider            string     `json:"provider"`
	ExternalTicketID    string     `json:"external_ticket_id"`
	ExternalStatus      string     `json:"external_status"`
	ExternalStatusLabel string     `json:"external_status_label"`
	SyncStatus          string     `json:"sync_status"`
	LastSyncedAt        *time.Time `json:"last_synced_at,omitempty"`
	Replayed            bool       `json:"replayed"`
	Reconciled          bool       `json:"reconciled"`
}

// UpdateTicketStatus — PRODUCT.6-O2B3: external ticket status mutation
// POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status
func (h *CRMHandlers) UpdateTicketStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.updateExternalTicketStatusService == nil {
		ticketingUnavailable(w)
		return
	}

	// Require Idempotency-Key header
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		http.Error(w, "missing or empty Idempotency-Key header", http.StatusBadRequest)
		return
	}

	// Parse path parameters
	tenantID, convID := r.PathValue("tenant_id"), r.PathValue("conversation_id")
	if tenantID == "" || convID == "" {
		http.Error(w, "missing tenant or conversation ID", http.StatusBadRequest)
		return
	}
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		http.Error(w, "invalid tenant ID", http.StatusBadRequest)
		return
	}
	cid, err := uuid.Parse(convID)
	if err != nil {
		http.Error(w, "invalid conversation ID", http.StatusBadRequest)
		return
	}

	// Extract authenticated actor
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse request body
	var req updateTicketStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.TargetStatus == "" {
		http.Error(w, "target_status is required", http.StatusBadRequest)
		return
	}

	// Call application service
	result, err := h.updateExternalTicketStatusService.UpdateExternalTicketStatus(ctx, ticketsapplication.UpdateExternalTicketStatusCommand{
		TenantID:       tid,
		ConversationID: cid,
		ActorUserID:    tc.ActorID,
		TargetStatus:   req.TargetStatus,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		failUpdateTicketStatus(w, err)
		return
	}

	// Map outcome to HTTP status
	switch result.Outcome {
	case ticketsapplication.OutcomeStatusUpdated, ticketsapplication.OutcomeStatusReplaySuccess, ticketsapplication.OutcomeStatusReconciledSuccess:
		w.Header().Set("Content-Type", "application/json")
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(updateTicketStatusResponse{
			LocalTicketID:       result.LocalTicketID,
			Provider:            result.Provider,
			ExternalTicketID:    result.ExternalTicketID,
			ExternalStatus:      result.ExternalStatus,
			ExternalStatusLabel: result.ExternalStatusLabel,
			SyncStatus:          result.SyncStatus,
			LastSyncedAt:        result.LastSyncedAt,
			Replayed:            result.Replayed,
			Reconciled:          result.Reconciled,
		})
	case ticketsapplication.OutcomeStatusReconciliationRequired:
		http.Error(w, "ticket status requires reconciliation", http.StatusConflict)
	case ticketsapplication.OutcomeStatusDefinitiveFailure:
		http.Error(w, "ticketing provider rejected the request", http.StatusUnprocessableEntity)
	default:
		log.Printf("unknown outcome: %q", result.Outcome)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// failUpdateTicketStatus maps UpdateExternalTicketStatus application errors to HTTP
func failUpdateTicketStatus(w http.ResponseWriter, err error) {
	var resErr *ticketsports.ResolutionError
	switch {
	case errors.As(err, &resErr):
		ticketingUnavailable(w)
	case errors.Is(err, ticketsapplication.ErrForbidden), errors.Is(err, ticketsapplication.ErrNotAssignedToYou):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, ticketsapplication.ErrNoActiveTicket):
		http.Error(w, "no active ticket for this conversation", http.StatusNotFound)
	case errors.Is(err, ticketsapplication.ErrTicketNotLinked):
		http.Error(w, "active ticket is not linked to an external ticket", http.StatusConflict)
	case errors.Is(err, ticketsapplication.ErrInconsistentExternalLink), errors.Is(err, ticketsapplication.ErrProviderMismatch):
		http.Error(w, "ticket requires reconciliation", http.StatusConflict)
	case errors.Is(err, ticketsapplication.ErrStatusIdempotencyMismatch):
		http.Error(w, "idempotency key already used with different request", http.StatusUnprocessableEntity)
	case errors.Is(err, ticketsapplication.ErrProviderUnavailable):
		http.Error(w, "ticketing provider unavailable", http.StatusServiceUnavailable)
	default:
		log.Printf("update ticket status: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// externalTicketCreator is the seam PRODUCT.6-M's CreateTicket route calls
// through — narrow on purpose (not *ticketsapplication.Service directly) so
// HTTP tests can inject a fake without a real Postgres-backed application
// service. *ticketsapplication.Service satisfies this without any adapter.
type externalTicketCreator interface {
	CreateExternalTicket(ctx context.Context, cmd ticketsapplication.CreateExternalTicketCommand) (*ticketsapplication.Result, error)
}

// externalTicketCreateRequest is the PRODUCT.6-M browser-facing request
// shape — deliberately provider-neutral (no companyId/name/content/
// assigneeUserId). ConversationID comes from the route, never the body;
// tenant/actor come from the authenticated TenantContext, never the body.
type externalTicketCreateRequest struct {
	SelectedCustomerExternalID string `json:"selected_customer_external_id"`
	Subject                    string `json:"subject"`
	Description                string `json:"description"`
}

// externalTicketCreateResponse is the smallest provider-neutral success
// shape — no K3G Bearer identity, no credential material, no raw provider
// payload.
type externalTicketCreateResponse struct {
	LocalTicketID    uuid.UUID `json:"local_ticket_id"`
	ExternalTicketID string    `json:"external_ticket_id,omitempty"`
	Provider         string    `json:"provider,omitempty"`
	SyncStatus       string    `json:"sync_status"`
	Replayed         bool      `json:"replayed"`
}

// externalTicketProblemResponse is used for outcomes that must NOT be
// confused with an ordinary retryable error (PRODUCT.6-M section 9): a
// stable machine-readable Code, distinct from any generic 5xx text body,
// so a caller can tell "do not automatically retry create" apart from
// "safe to retry later".
type externalTicketProblemResponse struct {
	Error             string `json:"error"`
	Code              string `json:"code"`
	AttemptID         string `json:"attempt_id,omitempty"`
	AttemptState      string `json:"attempt_state,omitempty"`
	LocalTicketID     string `json:"local_ticket_id,omitempty"`
	Provider          string `json:"provider,omitempty"`
	ExternalTicketID  string `json:"external_ticket_id,omitempty"`
	SyncStatus        string `json:"sync_status,omitempty"`
	Severe            bool   `json:"severe,omitempty"`
	ProviderErrorCode string `json:"provider_error_code,omitempty"`
}

// attemptIDOrEmpty omits a zero-value UUID (no real attempt exists, e.g.
// OutcomeAlreadyLinked) rather than rendering it as a fake-looking
// "00000000-0000-0000-0000-000000000000" attempt id.
func attemptIDOrEmpty(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

// CreateTicket — cria ticket externo real via CreateExternalTicket
// (PRODUCT.6-M). TenantID/ActorUserID vêm exclusivamente do TenantContext
// autenticado (nunca do corpo); SelectedCustomerExternalID chega do
// browser mas é validado server-side pela application service via
// CompanyDirectory (PRODUCT.6-K0/6-K2) — este handler nunca encaminha o
// valor bruto ao K3G.
// POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket
func (h *CRMHandlers) CreateTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// PRODUCT.6-M containment: the real create path is gated on
	// externalTicketService, not h.crm (h.crm still gates the
	// PRODUCT.6-B-contained GET/UPDATE/CLOSE routes below, which have no
	// proven real path yet).
	if h.externalTicketService == nil {
		ticketingUnavailable(w)
		return
	}

	tenantID, convID := r.PathValue("tenant_id"), r.PathValue("conversation_id")
	if tenantID == "" || convID == "" {
		http.Error(w, "missing tenant or conversation ID", http.StatusBadRequest)
		return
	}
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		http.Error(w, "invalid tenant ID", http.StatusBadRequest)
		return
	}
	cid, err := uuid.Parse(convID)
	if err != nil {
		http.Error(w, "invalid conversation ID", http.StatusBadRequest)
		return
	}

	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req externalTicketCreateRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil || json.Unmarshal(body, &req) != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	result, err := h.externalTicketService.CreateExternalTicket(ctx, ticketsapplication.CreateExternalTicketCommand{
		TenantID: tid, ConversationID: cid, ActorUserID: tc.ActorID,
		SelectedCustomerExternalID: req.SelectedCustomerExternalID,
		Subject:                    req.Subject,
		Description:                req.Description,
		IdempotencyKey:             r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		failExternalTicketCreate(w, err)
		return
	}
	writeExternalTicketResult(w, result)
}

// failExternalTicketCreate maps CreateExternalTicket's application errors
// to HTTP, reusing existing repository conventions rather than inventing
// new ones: ErrUnassigned/ErrIdempotencyMismatch mirror
// internal/messages/adapters.fail's exact status choices (409 and 422
// respectively) for the same application-level concepts.
func failExternalTicketCreate(w http.ResponseWriter, err error) {
	var resErr *ticketsports.ResolutionError
	switch {
	case errors.As(err, &resErr):
		// PRODUCT.6-L: no/ambiguous/invalid tenant ticketing configuration
		// is exactly the class of problem ticketingUnavailable already
		// communicates — same status, same body, no new convention.
		ticketingUnavailable(w)
	case errors.Is(err, ticketsapplication.ErrForbidden), errors.Is(err, ticketsapplication.ErrNotAssignedToYou):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, ticketsapplication.ErrConversationNotFound), errors.Is(err, ticketsapplication.ErrNoLocalTicketToEnrich):
		http.Error(w, "conversation or local ticket not found", http.StatusNotFound)
	case errors.Is(err, ticketsapplication.ErrUnassigned):
		http.Error(w, "conversation must be assigned before an external ticket can be created", http.StatusConflict)
	case errors.Is(err, ticketsapplication.ErrIdempotencyMismatch):
		http.Error(w, "Idempotency-Key was already used with a different request", http.StatusUnprocessableEntity)
	case errors.Is(err, ticketsapplication.ErrInvalidCommand), errors.Is(err, ticketsapplication.ErrInvalidIdempotencyKey), errors.Is(err, ticketsapplication.ErrInvalidCompany):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		log.Printf("tickets create external: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// writeExternalTicketResult maps every non-error Result.Outcome to its HTTP
// representation. OutcomeReconciliationRequired and OutcomeDefinitiveFailure
// deliberately never share a status/body shape with an ordinary retryable
// error (PRODUCT.6-M section 9) — both carry a stable Code field a client
// can branch on without guessing from prose.
func writeExternalTicketResult(w http.ResponseWriter, res *ticketsapplication.Result) {
	w.Header().Set("Content-Type", "application/json")
	switch res.Outcome {
	case ticketsapplication.OutcomeCreated, ticketsapplication.OutcomeReplaySuccess:
		replayed := res.Outcome == ticketsapplication.OutcomeReplaySuccess
		status := http.StatusCreated
		if replayed {
			status = http.StatusOK
			w.Header().Set("Idempotent-Replayed", "true")
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(externalTicketCreateResponse{
			LocalTicketID: res.LocalTicketID, ExternalTicketID: res.ExternalTicketID,
			Provider: res.Provider, SyncStatus: "synced", Replayed: replayed,
		})
	case ticketsapplication.OutcomeDefinitiveFailure:
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(externalTicketProblemResponse{
			Error: "external ticket provider rejected the request", Code: "TICKET_DEFINITIVE_FAILURE",
			AttemptID: res.AttemptID.String(), ProviderErrorCode: string(res.FailureCode),
		})
	case ticketsapplication.OutcomeReconciliationRequired:
		// Covers WRITE_OUTCOME_UNKNOWN, an in-flight replay, a confirmed
		// provider success whose durable record or local projection is not
		// (yet) complete, and a different key blocked by another
		// in-progress attempt on the same local ticket (PRODUCT.6-M5) —
		// never an automatic retry.
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(externalTicketProblemResponse{
			Error: "ticket creation outcome requires reconciliation before any retry", Code: "TICKET_RECONCILIATION_REQUIRED",
			AttemptID: attemptIDOrEmpty(res.AttemptID), AttemptState: string(res.AttemptState),
			ExternalTicketID: res.ExternalTicketID, Severe: res.Severe,
		})
	case ticketsapplication.OutcomeAlreadyLinked:
		// PRODUCT.6-M5: the active local ticket already has a consistent
		// external link from a DIFFERENT create intent (different
		// Idempotency-Key) — never treated as this call's own replay
		// (Replayed stays false), never a second provider call.
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(externalTicketProblemResponse{
			Error: "conversation's local ticket is already linked to an external ticket", Code: "TICKET_ALREADY_LINKED",
			LocalTicketID: res.LocalTicketID.String(), Provider: res.Provider, ExternalTicketID: res.ExternalTicketID,
			SyncStatus: "synced",
		})
	default:
		log.Printf("tickets create external: unknown outcome %q", res.Outcome)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// GetTicket — obtém ticket
// GET /api/v1/tenants/{tenantId}/conversations/{conversationId}/ticket/{ticketId}
func (h *CRMHandlers) GetTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.crm == nil {
		ticketingUnavailable(w)
		return
	}

	ticketID := r.PathValue("ticket_id")
	if ticketID == "" {
		http.Error(w, "missing ticket ID", http.StatusBadRequest)
		return
	}

	ticket, err := h.crm.GetTicket(ctx, ticketID)
	if err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CRMTicketResponse{
		ID:        ticket.ID,
		Status:    ticket.Status,
		Subject:   ticket.Subject,
		CreatedAt: ticket.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: ticket.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// UpdateTicket — atualiza ticket
// PATCH /api/v1/tenants/{tenantId}/conversations/{conversationId}/ticket/{ticketId}
func (h *CRMHandlers) UpdateTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.crm == nil {
		ticketingUnavailable(w)
		return
	}

	ticketID := r.PathValue("ticket_id")
	var req CRMTicketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Status == "" {
		http.Error(w, "status required", http.StatusBadRequest)
		return
	}

	err := h.crm.UpdateTicket(ctx, ticketID, req.Status)
	if err != nil {
		http.Error(w, "ticket update failed", http.StatusInternalServerError)
		return
	}

	ticket, _ := h.crm.GetTicket(ctx, ticketID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CRMTicketResponse{
		ID:        ticket.ID,
		Status:    ticket.Status,
		Subject:   ticket.Subject,
		CreatedAt: ticket.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: ticket.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// CloseTicket — fecha ticket
// POST /api/v1/tenants/{tenantId}/conversations/{conversationId}/ticket/{ticketId}/close
func (h *CRMHandlers) CloseTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.crm == nil {
		ticketingUnavailable(w)
		return
	}

	ticketID := r.PathValue("ticket_id")
	err := h.crm.CloseTicket(ctx, ticketID)
	if err != nil {
		http.Error(w, "ticket close failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// activityConversation is the canonical, tenant-scoped conversation facts
// CreateActivity needs (PRODUCT.7B1B): who it is assigned to
// (authorization) and its persisted crm_contact_id — the ONLY trusted CRM
// contact identity for the provider write.
type activityConversation struct {
	AssignedToUserID *uuid.UUID
	CRMContactID     *uuid.UUID
}

// activityConversationReader is the seam CreateActivity uses to load
// activityConversation. Narrow on purpose so tests can inject a fake
// without a real Postgres conversation. *PostgresActivityConversations
// satisfies this without any adapter.
type activityConversationReader interface {
	LoadForActivity(ctx context.Context, conversationID uuid.UUID) (*activityConversation, bool, error)
}

// CRMActivityRequest — payload para criar activity (atendimento).
//
// PRODUCT.7B1B: contact_id and company_id were REMOVED from this
// contract. contact_id was previously trusted directly from the browser
// with zero validation; company_id was trusted as soon as it matched
// SOME active company in the tenant's directory — which only proves the
// company exists for the tenant, never that it is the company associated
// with THIS conversation/contact (confirmed P0: an untrusted external-
// write target either way). The server now derives the only trusted
// contact identity from the conversation's own persisted crm_contact_id;
// no authoritative company identity exists today (see CreateActivity's
// containment below), so the field was dropped rather than kept and
// half-trusted. A client that still sends contact_id/company_id has them
// silently ignored by json.Unmarshal (unknown fields) — never read, never
// able to influence server behavior.
type CRMActivityRequest struct {
	Subject string `json:"subject"`
}

// CreateActivity — TEMPORARILY CONTAINED (PRODUCT.7B1B security
// correction).
// POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/crm/activity
//
// This route previously (a) trusted req.ContactID/req.CompanyID directly
// — contact_id with zero validation, company_id with only "exists in the
// tenant's CompanyDirectory" — before writing to real K3G, and (b)
// depended on a single globally-bootstrapped h.k3gClient. Both were
// confirmed P0 untrusted-external-write-target/cross-tenant gaps.
//
// PRODUCT.7B1B closed the contact_id and global-client gaps, but a
// security review found CompanyDirectory membership alone is NOT
// sufficient authorization: it proves a company exists and is active for
// the tenant, never that it is the company associated with this
// conversation's CRM contact. No authoritative Contact/Conversation→
// Company relationship exists anywhere in this codebase today, and no
// K3G read contract can prove one from crm_contact_id alone (audited:
// K3GCRMClient has ListCompanies/FindCustomerByPhone/CreateContact/
// CreateActivity — no GetContact(id) or company-membership read).
//
// This route therefore still authenticates, authorizes (assignee or
// conversation.manage), and requires the conversation to have a linked
// CRM contact — then ALWAYS fails closed before any provider
// interaction. It never calls a provider. Restored once PRODUCT.7B2
// establishes a real Contact/Conversation→Company linkage, or K3G
// exposes a contract able to prove it.
func (h *CRMHandlers) CreateActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.activityConversations == nil || h.activityPermissions == nil {
		ticketingUnavailable(w)
		return
	}

	tenantID, convID := r.PathValue("tenant_id"), r.PathValue("conversation_id")
	if tenantID == "" || convID == "" {
		http.Error(w, "missing tenant or conversation ID", http.StatusBadRequest)
		return
	}
	if _, err := uuid.Parse(tenantID); err != nil {
		http.Error(w, "invalid tenant ID", http.StatusBadRequest)
		return
	}
	cid, err := uuid.Parse(convID)
	if err != nil {
		http.Error(w, "invalid conversation ID", http.StatusBadRequest)
		return
	}
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req CRMActivityRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil || json.Unmarshal(body, &req) != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Subject = strings.TrimSpace(req.Subject)
	if req.Subject == "" {
		http.Error(w, "subject required", http.StatusBadRequest)
		return
	}

	// Authorization: assignee or conversation.manage — the same primitive
	// CreateExternalTicket/Send already use for "who may act on this
	// conversation's CRM/messaging surface". This route previously had
	// NO authorization beyond "authenticated". Checked, and enforced,
	// BEFORE the containment response below — an unauthorized actor gets
	// its own 403/409, never a response that would confirm a linkable
	// conversation exists.
	conv, found, err := h.activityConversations.LoadForActivity(ctx, cid)
	if err != nil {
		log.Printf("crm create activity: load conversation: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	if conv.AssignedToUserID == nil {
		http.Error(w, "conversation must be assigned before an activity can be created", http.StatusConflict)
		return
	}
	if *conv.AssignedToUserID != tc.ActorID {
		canManage, permErr := h.activityPermissions.HasPermission(ctx, tc.ActorID, ticketsapplication.PermissionConversationManage)
		if permErr != nil {
			log.Printf("crm create activity: check permission: %v", permErr)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if !canManage {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	// The ONLY trusted CRM contact identity: the conversation's own
	// persisted crm_contact_id. A conversation with none fails closed —
	// automatic CRM-contact creation is the separate, already-audited
	// inbound flow (internal/inbox/application.InboundService.Ingest),
	// never invoked from here.
	if conv.CRMContactID == nil {
		http.Error(w, "conversation has no linked CRM contact", http.StatusConflict)
		return
	}

	// PRODUCT.7B1B containment: even with a linked CRM contact, there is
	// no authoritative source today proving which company that contact
	// belongs to. Accepting "exists in this tenant's CompanyDirectory" as
	// sufficient authorization would still let the browser pick the
	// actual write target — exactly the gap this slice exists to close.
	// Fail closed, never call the provider.
	http.Error(w, "CRM company context for this conversation is not yet authoritative", http.StatusConflict)
}

// CompanyItem — representação de uma empresa
type CompanyItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	CNPJ string `json:"cnpj,omitempty"`
}

// CompanyListResponse — resposta com lista de empresas
type CompanyListResponse struct {
	Items []CompanyItem `json:"items"`
}

// ListCompanies — lista empresas do CRM K3G do tenant autenticado.
// GET /api/v1/tenants/{tenant_id}/crm/companies
//
// PRODUCT.7B1A: this route no longer depends on h.k3gClient (a single
// globally-bootstrapped client shared by every tenant, resolved once at
// boot from a hardcoded "ACME"-matching connection — the confirmed P0
// cross-tenant company-directory leak from PRODUCT.7B). It now resolves
// the SAME per-tenant runtime CreateExternalTicket already uses
// (PRODUCT.6-L): one K3G credential per tenant, decrypted fresh for this
// request, never shared across tenants, never falling back to another
// tenant's or a global client. A tenant with no/ambiguous/invalid K3G
// configuration fails closed (503, ticketingUnavailable) — it never
// receives another tenant's directory.
func (h *CRMHandlers) ListCompanies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.companyDirectoryResolver == nil {
		ticketingUnavailable(w)
		return
	}

	tenantID := r.PathValue("tenant_id")
	if tenantID == "" {
		http.Error(w, "missing tenant ID", http.StatusBadRequest)
		return
	}
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		http.Error(w, "invalid tenant ID", http.StatusBadRequest)
		return
	}

	// K3GTicketingRuntimeResolver.Resolve itself checks the requested
	// tenantID against the session's own TenantContext (tenantSession
	// already authorized {tenant_id} against this principal's membership)
	// and fails closed on ANY mismatch — the same defense-in-depth
	// CreateExternalTicket relies on, reused here for free.
	rt, err := h.companyDirectoryResolver.Resolve(ctx, tid)
	if err != nil {
		var resErr *ticketsports.ResolutionError
		if errors.As(err, &resErr) {
			// No/ambiguous/invalid tenant K3G configuration is exactly the
			// class of problem ticketingUnavailable already communicates —
			// same status, same body, no new convention (mirrors
			// failExternalTicketCreate/failRefreshTicketProjection).
			ticketingUnavailable(w)
			return
		}
		log.Printf("crm list companies: resolve runtime: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	companies, err := rt.CompanyDirectory.ListCompanies(ctx)
	if err != nil {
		log.Printf("crm list companies: %v", err)
		http.Error(w, "failed to list companies", http.StatusInternalServerError)
		return
	}

	items := make([]CompanyItem, len(companies))
	for i, co := range companies {
		items[i] = CompanyItem{
			ID:   co.ExternalID,
			Name: co.Name,
			CNPJ: co.CNPJ,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CompanyListResponse{Items: items})
}
