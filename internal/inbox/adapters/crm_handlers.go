package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/platform/db"
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
	dbPool    *pgxpool.Pool
	crm       connectors.CRMConnector
	k3gClient *connectors.K3GCRMClient
	// externalTicketService is the real, tenant-scoped external ticket
	// creation path (PRODUCT.6-M). Nil until server.go wires it — the
	// canonical runtime composition root, never a handler-built service
	// graph.
	externalTicketService externalTicketCreator
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

// SetK3GCRMClient — configura o cliente K3G CRM
func (h *CRMHandlers) SetK3GCRMClient(client *connectors.K3GCRMClient) {
	h.k3gClient = client
}

// SetExternalTicketService wires the real CreateExternalTicket application
// service (PRODUCT.6-M). Canonical runtime composition (server.go/main.go)
// calls this with a Postgres/K3G-backed *ticketsapplication.Service; tests
// may inject a fake satisfying externalTicketCreator.
func (h *CRMHandlers) SetExternalTicketService(svc externalTicketCreator) {
	h.externalTicketService = svc
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

// GetCurrentTicket — reports whether a real ERP ticketing connector is
// configured for this tenant, so TicketPanel can render an honest
// unavailable state proactively (PRODUCT.6-B) instead of only discovering
// it reactively when the operator tries to create a ticket. It never
// fabricates or returns a ticket: with no connector configured (the only
// state possible today), it always answers 503.
// GET /api/v1/tenants/{tenantId}/conversations/{conversationId}/ticket
func (h *CRMHandlers) GetCurrentTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.crm == nil {
		ticketingUnavailable(w)
		return
	}
	// A real connector exists but there is no per-conversation ticket
	// lookup yet (no local projection model — PRODUCT.6-A). Nothing to
	// report until that lands.
	http.Error(w, "no ticket associated with this conversation", http.StatusNotFound)
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
	ExternalTicketID  string `json:"external_ticket_id,omitempty"`
	Severe            bool   `json:"severe,omitempty"`
	ProviderErrorCode string `json:"provider_error_code,omitempty"`
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
		// Covers WRITE_OUTCOME_UNKNOWN, an in-flight replay, and a
		// confirmed provider success whose durable record or local
		// projection is not (yet) complete — never an automatic retry.
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(externalTicketProblemResponse{
			Error: "ticket creation outcome requires reconciliation before any retry", Code: "TICKET_RECONCILIATION_REQUIRED",
			AttemptID: res.AttemptID.String(), AttemptState: string(res.AttemptState),
			ExternalTicketID: res.ExternalTicketID, Severe: res.Severe,
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

// CRMActivityRequest — payload para criar activity (atendimento)
type CRMActivityRequest struct {
	Subject   string `json:"subject"`
	CompanyID string `json:"company_id"`
	ContactID string `json:"contact_id"`
}

// CRMActivityResponse — resposta de activity
type CRMActivityResponse struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Subject   string `json:"subject"`
	ContactID string `json:"contact_id"`
	CompanyID string `json:"company_id"`
	CreatedAt string `json:"created_at"`
}

// CreateActivity — cria activity (atendimento WHATSAPP) para uma conversa
// POST /api/v1/tenants/{tenantId}/conversations/{conversationId}/crm/activity
func (h *CRMHandlers) CreateActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	tenantID, convID := r.PathValue("tenant_id"), r.PathValue("conversation_id")
	if tenantID == "" || convID == "" {
		http.Error(w, "missing tenant or conversation ID", http.StatusBadRequest)
		return
	}

	// Parse request
	var req CRMActivityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Subject == "" || req.ContactID == "" || req.CompanyID == "" {
		http.Error(w, "subject, contact_id and company_id required", http.StatusBadRequest)
		return
	}

	// Get tenant ID
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		http.Error(w, "invalid tenant ID", http.StatusBadRequest)
		return
	}

	// Check tenant access via RLS
	activityID := ""
	err = db.WithTenantSession(ctx, h.dbPool, tid, false, func(sessionCtx context.Context) error {
		// Create activity in CRM (type is always WHATSAPP in this context)
		activity, crErr := h.k3gClient.CreateActivity(sessionCtx, "WHATSAPP", req.Subject, req.ContactID, req.CompanyID)
		if crErr != nil {
			return crErr
		}
		activityID = activity.ID
		return nil
	})
	if err != nil {
		http.Error(w, "activity creation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Return response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(CRMActivityResponse{
		ID:        activityID,
		Type:      "WHATSAPP",
		Subject:   req.Subject,
		ContactID: req.ContactID,
		CompanyID: req.CompanyID,
		CreatedAt: "", // CRM retorna, mas não temos aqui
	})
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

// ListCompanies — lista empresas do CRM K3G
// GET /api/v1/integrations/companies
func (h *CRMHandlers) ListCompanies(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := authn.FromContext(ctx); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Check if K3G CRM client is configured
	if h.k3gClient == nil {
		// Return empty list if CRM not configured
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CompanyListResponse{Items: []CompanyItem{}})
		return
	}

	// List companies from K3G CRM
	companies, err := h.k3gClient.ListCompanies(ctx)
	if err != nil {
		http.Error(w, "failed to list companies: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to response format
	items := make([]CompanyItem, len(companies))
	for i, co := range companies {
		items[i] = CompanyItem{
			ID:   co.ID,
			Name: co.Name,
			CNPJ: co.CNPJ,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CompanyListResponse{Items: items})
}
