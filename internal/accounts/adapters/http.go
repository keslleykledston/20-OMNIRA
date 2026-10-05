package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/accounts/domain"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
)

const (
	permAccountRead   = "account.read"
	permAccountManage = "account.manage"
)

// Handler serves the customer-account API (ADR-0018). Authority is the permission matrix of an ACTIVE membership;
// the tenant always comes from the session, never from a payload.
type Handler struct {
	pool     *pgxpool.Pool
	repo     *PostgresRepository
	audit    auditports.AuditEventRepository
	disabled bool
}

// WithEnabled switches the account API on (the default) or off (OMNIRA_CUSTOMER_ACCOUNTS_ENABLED=false): off answers 404.
func (h *Handler) WithEnabled(on bool) *Handler {
	h.disabled = !on
	return h
}

func NewHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository) *Handler {
	return &Handler{pool: pool, repo: NewPostgresRepository(pool), audit: audit}
}

// authorizeWith checks a second permission for the same caller (the tenant context is already established).
func (h *Handler) authorizeWith(r *http.Request, permission string) (*tenancydomain.TenantContext, int) {
	return h.authorize(r, permission)
}

func (h *Handler) authorize(r *http.Request, permission string) (*tenancydomain.TenantContext, int) {
	if h.disabled {
		return nil, http.StatusNotFound
	}
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		return nil, http.StatusUnauthorized
	}
	var ok bool
	if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(SELECT 1 FROM memberships m JOIN role_permissions rp ON rp.role_id = m.role_id
		              WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`, tc.TenantID, tc.ActorID, permission).Scan(&ok); err != nil {
		return nil, http.StatusInternalServerError
	}
	if !ok {
		return nil, http.StatusForbidden
	}
	return tc, 0
}

type accountDTO struct {
	ID        uuid.UUID         `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"account_type"`
	Status    string            `json:"status"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
	External  []externalLinkDTO `json:"external_links,omitempty"`
}

type externalLinkDTO struct {
	ID                uuid.UUID `json:"id"`
	Provider          string    `json:"provider"`
	ConnectionID      uuid.UUID `json:"connection_id"`
	ExternalCompanyID string    `json:"external_company_id"`
	NameSnapshot      *string   `json:"external_name_snapshot,omitempty"`
	Status            string    `json:"status"`
	Source            string    `json:"source"`
	VerifiedAt        *string   `json:"verified_at,omitempty"`
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func toDTO(a *domain.Account) accountDTO {
	return accountDTO{ID: a.ID, Name: a.Name, Type: string(a.Type), Status: string(a.Status), CreatedAt: ts(a.CreatedAt), UpdatedAt: ts(a.UpdatedAt)}
}

func toLinkDTO(l domain.ExternalLink) externalLinkDTO {
	d := externalLinkDTO{ID: l.ID, Provider: l.Provider, ConnectionID: l.ConnectionID, ExternalCompanyID: l.ExternalCompanyID, NameSnapshot: l.ExternalNameSnapshot, Status: string(l.Status), Source: string(l.Source)}
	if l.VerifiedAt != nil {
		v := ts(*l.VerifiedAt)
		d.VerifiedAt = &v
	}
	return d
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return false
	}
	return true
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrAccountNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, domain.ErrInvalidAccount):
		http.Error(w, "invalid account", http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrExternalLinkTaken):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (h *Handler) record(r *http.Request, tc *tenancydomain.TenantContext, action auditdomain.AuditAction, accountID uuid.UUID, meta map[string]any) {
	if h.audit == nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, auditdomain.ResourceAccount, accountID, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = h.audit.Store(r.Context(), ev)
}

// ListAccounts: GET /tenants/{tenant_id}/accounts?q=&status=&limit=
func (h *Handler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	tc, code := h.authorize(r, permAccountRead)
	if code != 0 {
		http.Error(w, http.StatusText(code), code)
		return
	}
	f := ListFilter{Query: r.URL.Query().Get("q")}
	if v := r.URL.Query().Get("status"); v != "" {
		st := domain.AccountStatus(v)
		if !st.Valid() {
			http.Error(w, "invalid status", http.StatusBadRequest)
			return
		}
		f.Status = &st
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		f.Limit = n
	}
	list, err := h.repo.ListAccounts(r.Context(), tc.TenantID, f)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]accountDTO, 0, len(list))
	for i := range list {
		out = append(out, toDTO(&list[i]))
	}
	write(w, http.StatusOK, map[string]any{"items": out})
}

// GetAccount: GET /tenants/{tenant_id}/accounts/{account_id} (with its provider links)
func (h *Handler) GetAccount(w http.ResponseWriter, r *http.Request) {
	tc, code := h.authorize(r, permAccountRead)
	if code != 0 {
		http.Error(w, http.StatusText(code), code)
		return
	}
	id, err := uuid.Parse(r.PathValue("account_id"))
	if err != nil {
		http.Error(w, "invalid account_id", http.StatusBadRequest)
		return
	}
	a, err := h.repo.GetAccount(r.Context(), tc.TenantID, id)
	if err != nil {
		fail(w, err)
		return
	}
	links, err := h.repo.ListExternalLinks(r.Context(), tc.TenantID, id)
	if err != nil {
		fail(w, err)
		return
	}
	dto := toDTO(a)
	for _, l := range links {
		dto.External = append(dto.External, toLinkDTO(l))
	}
	write(w, http.StatusOK, dto)
}

type accountTicketDTO struct {
	ID               uuid.UUID `json:"id"`
	ConversationID   uuid.UUID `json:"conversation_id"`
	Subject          string    `json:"subject"`
	Status           string    `json:"status"`
	Priority         string    `json:"priority"`
	Provider         *string   `json:"provider"`
	ExternalTicketID *string   `json:"external_ticket_id"`
	CreatedAt        string    `json:"created_at"`
	UpdatedAt        string    `json:"updated_at"`
}

// ListAccountTickets: GET /tenants/{tenant_id}/accounts/{account_id}/tickets — the REAL tickets (external link, subject
// or primary topic) that target this account. Needs account.read AND ticket.read: tenant-wide ticket visibility is its
// own grant.
func (h *Handler) ListAccountTickets(w http.ResponseWriter, r *http.Request) {
	tc, code := h.authorize(r, permAccountRead)
	if code == 0 {
		tc, code = h.authorizeWith(r, "ticket.read")
	}
	if code != 0 {
		http.Error(w, http.StatusText(code), code)
		return
	}
	id, err := uuid.Parse(r.PathValue("account_id"))
	if err != nil {
		http.Error(w, "invalid account_id", http.StatusBadRequest)
		return
	}
	if _, err := h.repo.GetAccount(r.Context(), tc.TenantID, id); err != nil {
		fail(w, err)
		return
	}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT t.id, t.conversation_id, t.subject, t.status, t.priority, t.provider, t.external_ticket_id, t.created_at, t.updated_at
		FROM tickets t
		WHERE t.tenant_id = $1 AND t.customer_account_id = $2 AND `+ticketsdomain.RealTicketSQL("t")+`
		ORDER BY t.updated_at DESC, t.id DESC LIMIT 100`, tc.TenantID, id)
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	out := []accountTicketDTO{}
	for rows.Next() {
		var d accountTicketDTO
		var created, updated time.Time
		if err := rows.Scan(&d.ID, &d.ConversationID, &d.Subject, &d.Status, &d.Priority, &d.Provider, &d.ExternalTicketID, &created, &updated); err != nil {
			fail(w, err)
			return
		}
		d.CreatedAt, d.UpdatedAt = ts(created), ts(updated)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		fail(w, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"items": out})
}

type createAccountRequest struct {
	Name        string `json:"name"`
	AccountType string `json:"account_type"`
}

// CreateAccount: POST /tenants/{tenant_id}/accounts. A company that exists in a provider is materialized through the
// contact-company flow (it re-validates the provider directory); this is for an organization with no provider record.
func (h *Handler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	tc, code := h.authorize(r, permAccountManage)
	if code != 0 {
		http.Error(w, http.StatusText(code), code)
		return
	}
	var req createAccountRequest
	if !decode(w, r, &req) {
		return
	}
	typ := domain.AccountType(req.AccountType)
	if req.AccountType == "" {
		typ = domain.TypeCustomer
	}
	a, err := h.repo.CreateAccount(r.Context(), tc.TenantID, req.Name, typ)
	if err != nil {
		fail(w, err)
		return
	}
	h.record(r, tc, auditdomain.ActionAccountCreated, a.ID, map[string]any{"account_type": string(a.Type)})
	write(w, http.StatusCreated, toDTO(a))
}

type updateAccountRequest struct {
	Name        *string `json:"name"`
	AccountType *string `json:"account_type"`
	Status      *string `json:"status"`
}

// UpdateAccount: PATCH /tenants/{tenant_id}/accounts/{account_id}
func (h *Handler) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	tc, code := h.authorize(r, permAccountManage)
	if code != 0 {
		http.Error(w, http.StatusText(code), code)
		return
	}
	id, err := uuid.Parse(r.PathValue("account_id"))
	if err != nil {
		http.Error(w, "invalid account_id", http.StatusBadRequest)
		return
	}
	var req updateAccountRequest
	if !decode(w, r, &req) {
		return
	}
	var typ *domain.AccountType
	var st *domain.AccountStatus
	if req.AccountType != nil {
		t := domain.AccountType(*req.AccountType)
		typ = &t
	}
	if req.Status != nil {
		s := domain.AccountStatus(*req.Status)
		st = &s
	}
	a, err := h.repo.UpdateAccount(r.Context(), tc.TenantID, id, req.Name, typ, st)
	if err != nil {
		fail(w, err)
		return
	}
	h.record(r, tc, auditdomain.ActionAccountUpdated, a.ID, map[string]any{"status": string(a.Status), "account_type": string(a.Type)})
	write(w, http.StatusOK, toDTO(a))
}
