package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	accountsadapters "github.com/omnira/omnira/internal/accounts/adapters"
	accountsdomain "github.com/omnira/omnira/internal/accounts/domain"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	"github.com/omnira/omnira/internal/contacts/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsports "github.com/omnira/omnira/internal/tickets/ports"
)

// directoryProvider is the provider string of the company directory behind TicketingRuntime (K3G today).
const directoryProvider = accountsdomain.ProviderK3G

const (
	permClassify    = "contact.classify"
	permAccountRead = "account.read"
)

// ClassificationHandler is the edit API for a contact's classification and company links (ADR-0018). The server decides
// everything that matters: the tenant comes from the session, the source is always "manual", and a provider company is
// only ever taken from the tenant's own CompanyDirectory (never from a name, CNPJ or status sent by the browser).
type ClassificationHandler struct {
	pool     *pgxpool.Pool
	repo     *ClassificationRepository
	accounts *accountsadapters.PostgresRepository
	audit    auditports.AuditEventRepository
	resolver ticketsports.TicketingRuntimeResolver
	changes  metric.Int64Counter
	disabled bool
}

// WithEnabled switches the whole classification/company API on (the default) or off
// (OMNIRA_CONTACT_CLASSIFICATION_ENABLED=false): a switched-off feature answers 404, like every other flag.
func (h *ClassificationHandler) WithEnabled(on bool) *ClassificationHandler {
	h.disabled = !on
	return h
}

func NewClassificationHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository) *ClassificationHandler {
	changes, _ := otel.Meter("omnira/contacts").Int64Counter("contact_classification_changes_total")
	return &ClassificationHandler{pool: pool, repo: NewClassificationRepository(pool), accounts: accountsadapters.NewPostgresRepository(pool), audit: audit, changes: changes}
}

// countChange records one classification change by from -> to and source; never a contact id, name or phone.
func (h *ClassificationHandler) countChange(ctx context.Context, c Change, source domain.ClassificationSource) {
	if h.changes == nil || !c.Changed {
		return
	}
	h.changes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("from", string(c.PreviousKind)), attribute.String("to", string(c.Kind)), attribute.String("source", string(source))))
}

// SetCompanyDirectoryResolver wires the SAME tenant-scoped runtime resolver ticketing uses (no second credential).
// Without it, directory selections answer 503 and local accounts still work.
func (h *ClassificationHandler) SetCompanyDirectoryResolver(r ticketsports.TicketingRuntimeResolver) {
	h.resolver = r
}

func memberHas(ctx context.Context, pool *pgxpool.Pool, tc *tenancydomain.TenantContext, key string) (bool, error) {
	var ok bool
	err := platformdb.QuerierFromContext(ctx, pool).QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM memberships m JOIN role_permissions rp ON rp.role_id = m.role_id
		              WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tc.TenantID, tc.ActorID, key).Scan(&ok)
	return ok, err
}

func (h *ClassificationHandler) authorize(w http.ResponseWriter, r *http.Request, permission string) (*tenancydomain.TenantContext, uuid.UUID, bool) {
	if h.disabled {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, uuid.Nil, false
	}
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, uuid.Nil, false
	}
	ok, err := memberHas(r.Context(), h.pool, tc, permission)
	if err != nil {
		http.Error(w, "failed to check permission", http.StatusInternalServerError)
		return nil, uuid.Nil, false
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, uuid.Nil, false
	}
	contactID, err := uuid.Parse(r.PathValue("contact_id"))
	if err != nil {
		http.Error(w, "invalid contact_id", http.StatusBadRequest)
		return nil, uuid.Nil, false
	}
	return tc, contactID, true
}

// atomically runs fn inside a savepoint (see platformdb.WithSavepoint).
func atomically(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context) error) error {
	return platformdb.WithSavepoint(ctx, pool, fn)
}

type accountRefDTO struct {
	AccountID          *uuid.UUID `json:"account_id"`
	DirectoryCompanyID string     `json:"directory_company_id"`
	RelationshipType   string     `json:"relationship_type"`
	Primary            bool       `json:"primary"`
	// EvidenceID: the company was suggested by integration evidence (a validated ticket selection). The server
	// re-checks it belongs to THIS contact and names exactly this directory company; the link is then recorded as
	// ticket_flow. Evidence alone never classifies or links anything: a human still has to accept.
	EvidenceID *uuid.UUID `json:"evidence_id"`
}

type linkDTO struct {
	ID               uuid.UUID `json:"id"`
	AccountID        uuid.UUID `json:"account_id"`
	AccountName      string    `json:"account_name"`
	RelationshipType string    `json:"relationship_type"`
	Status           string    `json:"status"`
	Primary          bool      `json:"primary"`
	Source           string    `json:"source"`
	CreatedAt        string    `json:"created_at"`
	EndedAt          *string   `json:"ended_at,omitempty"`
}

func toLinkDTO(l Link) linkDTO {
	d := linkDTO{ID: l.ID, AccountID: l.AccountID, AccountName: l.AccountName, RelationshipType: string(l.Relationship), Status: l.Status, Primary: l.Primary, Source: string(l.Source), CreatedAt: l.CreatedAt.UTC().Format(time.RFC3339Nano)}
	if l.EndedAt != nil {
		v := l.EndedAt.UTC().Format(time.RFC3339Nano)
		d.EndedAt = &v
	}
	return d
}

type classificationView struct {
	Kind                 string    `json:"kind"`
	InternalRole         *string   `json:"internal_role"`
	ClassificationSource *string   `json:"classification_source"`
	ClassifiedAt         *string   `json:"classified_at"`
	Accounts             []linkDTO `json:"accounts"`
}

func (h *ClassificationHandler) view(ctx context.Context, tenantID, contactID uuid.UUID) (classificationView, error) {
	var v classificationView
	var at *time.Time
	if err := platformdb.QuerierFromContext(ctx, h.pool).QueryRow(ctx, `SELECT kind, internal_role, classification_source, classified_at FROM contacts WHERE tenant_id=$1 AND id=$2`, tenantID, contactID).
		Scan(&v.Kind, &v.InternalRole, &v.ClassificationSource, &at); err != nil {
		return v, err
	}
	if at != nil {
		s := at.UTC().Format(time.RFC3339Nano)
		v.ClassifiedAt = &s
	}
	links, err := h.repo.ListLinks(ctx, tenantID, contactID, false)
	if err != nil {
		return v, err
	}
	v.Accounts = make([]linkDTO, 0, len(links))
	for _, l := range links {
		v.Accounts = append(v.Accounts, toLinkDTO(l))
	}
	return v, nil
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return false
	}
	return true
}

// failDomain maps a domain error to the response. It never echoes provider text.
func failDomain(w http.ResponseWriter, err error) {
	var dirErr *directoryError
	switch {
	case errors.As(err, &dirErr):
		http.Error(w, dirErr.message, dirErr.status)
	case errors.Is(err, domain.ErrContactNotFound), errors.Is(err, domain.ErrLinkNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, domain.ErrAccountMissing):
		http.Error(w, "account not found or archived", http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrCustomerNeedsAccount):
		http.Error(w, "a customer needs at least one linked company", http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrInternalNeedsRole):
		http.Error(w, "an internal contact needs a role: team, partner or supplier", http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrLastLink):
		http.Error(w, "this is the customer's last company: reclassify the contact in the same request", http.StatusConflict)
	case errors.Is(err, domain.ErrInvalidInput):
		http.Error(w, "invalid input", http.StatusBadRequest)
	default:
		log.Printf("contacts classification: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

type directoryError struct {
	status  int
	message string
}

func (e *directoryError) Error() string { return e.message }

// resolveRef turns one request entry into a link input. A directory company is revalidated against the tenant's own
// CompanyDirectory, materialized as account + external link (source directory_selection) in the caller's transaction,
// and refused when it is unknown or inactive in the provider.
func (h *ClassificationHandler) resolveRef(ctx context.Context, tc *tenancydomain.TenantContext, contactID uuid.UUID, ref accountRefDTO) (domain.AccountLinkInput, error) {
	in := domain.AccountLinkInput{Relationship: domain.RelationshipType(ref.RelationshipType), Primary: ref.Primary, VerifiedByActor: true}
	dirID := strings.TrimSpace(ref.DirectoryCompanyID)
	switch {
	case ref.AccountID != nil && dirID != "", ref.AccountID == nil && dirID == "":
		return in, domain.ErrInvalidInput
	case ref.EvidenceID != nil && dirID == "":
		return in, domain.ErrInvalidInput // evidence names a provider company: it needs the directory id
	case ref.AccountID != nil:
		in.AccountID = *ref.AccountID
		return in, nil
	}
	if h.resolver == nil {
		return in, &directoryError{http.StatusServiceUnavailable, "company directory is not configured"}
	}
	rt, err := h.resolver.Resolve(ctx, tc.TenantID)
	if err != nil {
		var resErr *ticketsports.ResolutionError
		if errors.As(err, &resErr) {
			return in, &directoryError{http.StatusServiceUnavailable, "company directory is not available"}
		}
		return in, err
	}
	companies, err := rt.CompanyDirectory.ListCompanies(ctx)
	if err != nil {
		log.Printf("contacts classification: list companies: %v", err)
		return in, &directoryError{http.StatusBadGateway, "company directory failed"}
	}
	var picked *ticketsports.Company
	for i := range companies {
		if companies[i].ExternalID == dirID {
			picked = &companies[i]
			break
		}
	}
	if picked == nil || !picked.Active {
		return in, &directoryError{http.StatusUnprocessableEntity, "company not found or inactive in the directory"}
	}
	if ref.EvidenceID != nil {
		var ok bool
		if err := platformdb.QuerierFromContext(ctx, h.pool).QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM crm_contact_company_evidence
			              WHERE tenant_id=$1 AND id=$2 AND contact_id=$3 AND connection_id=$4 AND external_company_id=$5 AND revoked_at IS NULL)`,
			tc.TenantID, *ref.EvidenceID, contactID, rt.ConnectionID, picked.ExternalID).Scan(&ok); err != nil {
			return in, err
		}
		if !ok {
			return in, &directoryError{http.StatusUnprocessableEntity, "evidence does not match this contact and company"}
		}
		in.Source = domain.SourceTicketFlow
	}
	existing, err := h.accounts.FindByExternal(ctx, tc.TenantID, directoryProvider, rt.ConnectionID, picked.ExternalID)
	if err != nil {
		return in, err
	}
	now := time.Now().UTC()
	accountID := uuid.Nil
	if existing != nil {
		accountID = existing.AccountID
	} else {
		acc, err := h.accounts.CreateAccount(ctx, tc.TenantID, picked.Name, accountsdomain.TypeCustomer)
		if err != nil {
			return in, err
		}
		accountID = acc.ID
	}
	name := picked.Name
	if _, err := h.accounts.UpsertExternalLink(ctx, accountsdomain.ExternalLink{
		TenantID: tc.TenantID, AccountID: accountID, Provider: directoryProvider, ConnectionID: rt.ConnectionID,
		ExternalCompanyID: picked.ExternalID, ExternalNameSnapshot: &name, Source: accountsdomain.SourceDirectorySelection, VerifiedAt: &now,
	}); err != nil {
		return in, err
	}
	in.AccountID = accountID
	return in, nil
}

func (h *ClassificationHandler) resolveRefs(ctx context.Context, tc *tenancydomain.TenantContext, contactID uuid.UUID, refs []accountRefDTO) ([]domain.AccountLinkInput, error) {
	out := make([]domain.AccountLinkInput, 0, len(refs))
	primaries := 0
	for _, ref := range refs {
		in, err := h.resolveRef(ctx, tc, contactID, ref)
		if err != nil {
			return nil, err
		}
		if in.Primary {
			primaries++
		}
		out = append(out, in)
	}
	if primaries > 1 || len(refs) > 20 {
		return nil, domain.ErrInvalidInput
	}
	return out, nil
}

func (h *ClassificationHandler) record(r *http.Request, tc *tenancydomain.TenantContext, action auditdomain.AuditAction, contactID uuid.UUID, meta map[string]any) {
	if h.audit == nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, auditdomain.ResourceContact, contactID, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = h.audit.Store(r.Context(), ev)
}

// recordKindRecompute audits the derived conversation_kind changes a reclassification caused (one entry, with counts).
func (h *ClassificationHandler) recordKindRecompute(r *http.Request, tc *tenancydomain.TenantContext, contactID uuid.UUID, c Change) {
	if c.ConversationsRecomputed == 0 && c.GroupsRecomputed == 0 {
		return
	}
	h.record(r, tc, auditdomain.ActionConversationKindChanged, contactID, map[string]any{"conversations": c.ConversationsRecomputed, "groups": c.GroupsRecomputed, "contact_kind": string(c.Kind)})
}

type suggestionDTO struct {
	EvidenceID        uuid.UUID  `json:"evidence_id"`
	ExternalCompanyID string     `json:"external_company_id"`
	ConnectionID      uuid.UUID  `json:"connection_id"`
	Source            string     `json:"source"`
	FirstVerifiedAt   string     `json:"first_verified_at"`
	LastVerifiedAt    string     `json:"last_verified_at"`
	AccountID         *uuid.UUID `json:"account_id,omitempty"`
	AccountName       *string    `json:"account_name,omitempty"`
	AlreadyLinked     bool       `json:"already_linked"`
}

// ListCompanySuggestions: GET /contacts/{contact_id}/company-suggestions (account.read). Companies that integration
// evidence (a validated ticket selection) associated with this contact. It is a SUGGESTION: it reads the local table
// only (no provider call), carries no provider metadata, and never classifies or links anything by itself.
func (h *ClassificationHandler) ListCompanySuggestions(w http.ResponseWriter, r *http.Request) {
	tc, contactID, ok := h.authorize(w, r, permAccountRead)
	if !ok {
		return
	}
	var exists bool
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM contacts WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, contactID).Scan(&exists); err != nil {
		failDomain(w, err)
		return
	}
	if !exists {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	rows, err := q.Query(r.Context(), `
		SELECT e.id, e.external_company_id, e.connection_id, e.source, e.first_verified_at, e.last_verified_at,
		       l.account_id, a.name,
		       COALESCE(EXISTS(SELECT 1 FROM contact_account_links cl
		                       WHERE cl.tenant_id=e.tenant_id AND cl.contact_id=e.contact_id AND cl.account_id=l.account_id AND cl.status='active'), false)
		FROM crm_contact_company_evidence e
		LEFT JOIN account_external_links l ON l.tenant_id=e.tenant_id AND l.connection_id=e.connection_id AND l.external_company_id=e.external_company_id
		LEFT JOIN customer_accounts a ON a.tenant_id=l.tenant_id AND a.id=l.account_id
		WHERE e.tenant_id=$1 AND e.contact_id=$2 AND e.revoked_at IS NULL
		ORDER BY e.last_verified_at DESC, e.id`, tc.TenantID, contactID)
	if err != nil {
		failDomain(w, err)
		return
	}
	defer rows.Close()
	out := []suggestionDTO{}
	for rows.Next() {
		var d suggestionDTO
		var first, last time.Time
		if err := rows.Scan(&d.EvidenceID, &d.ExternalCompanyID, &d.ConnectionID, &d.Source, &first, &last, &d.AccountID, &d.AccountName, &d.AlreadyLinked); err != nil {
			failDomain(w, err)
			return
		}
		d.FirstVerifiedAt, d.LastVerifiedAt = first.UTC().Format(time.RFC3339Nano), last.UTC().Format(time.RFC3339Nano)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		failDomain(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"items": out})
}

// GetClassification: GET /contacts/{contact_id}/classification  (account.read)
func (h *ClassificationHandler) GetClassification(w http.ResponseWriter, r *http.Request) {
	tc, contactID, ok := h.authorize(w, r, permAccountRead)
	if !ok {
		return
	}
	v, err := h.view(r.Context(), tc.TenantID, contactID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		failDomain(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, v)
}

type putClassificationRequest struct {
	Kind     string          `json:"kind"`
	Accounts []accountRefDTO `json:"accounts"`
	// InternalRole: team | partner | supplier, required when kind is internal and refused for any other kind.
	InternalRole string `json:"internal_role"`
	// EndLinks ends every active company link when the contact stops being a customer (default: links are kept).
	EndLinks bool `json:"end_links"`
}

// PutClassification: PUT /contacts/{contact_id}/classification  (contact.classify). Kind and companies change in ONE
// transaction: unclassified -> customer needs at least one company in the same call (or one already linked).
func (h *ClassificationHandler) PutClassification(w http.ResponseWriter, r *http.Request) {
	tc, contactID, ok := h.authorize(w, r, permClassify)
	if !ok {
		return
	}
	var req putClassificationRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	kind := domain.ContactKind(req.Kind)
	if !kind.Valid() {
		http.Error(w, "kind must be unclassified, customer, internal, other or spam", http.StatusBadRequest)
		return
	}
	var change Change
	err := atomically(r.Context(), h.pool, func(ctx context.Context) error {
		inputs, err := h.resolveRefs(ctx, tc, contactID, req.Accounts)
		if err != nil {
			return err
		}
		change, err = h.repo.ClassifyWithRole(ctx, tc.TenantID, tc.ActorID, contactID, kind, domain.SourceManual, inputs, req.EndLinks, domain.InternalRole(req.InternalRole))
		return err
	})
	if err != nil {
		failDomain(w, err)
		return
	}
	h.countChange(r.Context(), change, domain.SourceManual)
	if change.Changed {
		if kind == domain.KindSpam {
			_, _ = platformdb.QuerierFromContext(r.Context(), h.pool).Exec(r.Context(), `
				UPDATE conversations SET queue_id = NULL, routing_retry_at = NULL, updated_at = now()
				WHERE tenant_id = $1 AND contact_id = $2 AND status = 'open' AND assigned_to_user_id IS NULL AND queue_id IS NOT NULL`, tc.TenantID, contactID)
		}
		action := auditdomain.ActionContactReclassified
		if change.PreviousKind == domain.KindUnclassified {
			action = auditdomain.ActionContactClassified
		}
		h.record(r, tc, action, contactID, map[string]any{"kind_from": string(change.PreviousKind), "kind_to": string(kind), "internal_role_from": string(change.PreviousRole), "internal_role_to": string(change.Role), "classification_source": string(domain.SourceManual), "accounts": len(req.Accounts)})
		h.recordKindRecompute(r, tc, contactID, change)
	} else if len(req.Accounts) > 0 {
		h.record(r, tc, auditdomain.ActionContactAccountLinked, contactID, map[string]any{"accounts": len(req.Accounts), "classification_source": string(domain.SourceManual)})
	}
	v, err := h.view(r.Context(), tc.TenantID, contactID)
	if err != nil {
		failDomain(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, v)
}

// LinkAccount: POST /contacts/{contact_id}/accounts (contact.classify)
func (h *ClassificationHandler) LinkAccount(w http.ResponseWriter, r *http.Request) {
	tc, contactID, ok := h.authorize(w, r, permClassify)
	if !ok {
		return
	}
	var ref accountRefDTO
	if !decodeStrict(w, r, &ref) {
		return
	}
	var link *Link
	err := atomically(r.Context(), h.pool, func(ctx context.Context) error {
		in, err := h.resolveRef(ctx, tc, contactID, ref)
		if err != nil {
			return err
		}
		link, err = h.repo.LinkAccount(ctx, tc.TenantID, tc.ActorID, contactID, in, domain.SourceManual)
		return err
	})
	if err != nil {
		failDomain(w, err)
		return
	}
	h.record(r, tc, auditdomain.ActionContactAccountLinked, contactID, map[string]any{"account_id": link.AccountID.String(), "relationship_type": string(link.Relationship), "primary": link.Primary, "classification_source": string(domain.SourceManual)})
	writeJSONStatus(w, http.StatusOK, toLinkDTO(*link))
}

type endLinkRequest struct {
	// ReclassifyTo is required when this is the last active company of a customer: other | unclassified.
	ReclassifyTo string `json:"reclassify_to"`
}

// EndLink: POST /contacts/{contact_id}/accounts/{link_id}/end (contact.classify). Soft end, never a delete.
func (h *ClassificationHandler) EndLink(w http.ResponseWriter, r *http.Request) {
	tc, contactID, ok := h.authorize(w, r, permClassify)
	if !ok {
		return
	}
	linkID, err := uuid.Parse(r.PathValue("link_id"))
	if err != nil {
		http.Error(w, "invalid link_id", http.StatusBadRequest)
		return
	}
	var req endLinkRequest
	if r.ContentLength != 0 && !decodeStrict(w, r, &req) {
		return
	}
	var target *domain.ClassificationSource
	after := domain.ContactKind(req.ReclassifyTo)
	if req.ReclassifyTo != "" {
		if after != domain.KindOther && after != domain.KindUnclassified {
			http.Error(w, "reclassify_to must be other or unclassified", http.StatusBadRequest)
			return
		}
		s := domain.SourceManual
		target = &s
	}
	var change Change
	err = atomically(r.Context(), h.pool, func(ctx context.Context) error {
		change, err = h.repo.EndLink(ctx, tc.TenantID, tc.ActorID, contactID, linkID, target, after)
		return err
	})
	if err != nil {
		failDomain(w, err)
		return
	}
	h.countChange(r.Context(), change, domain.SourceManual)
	h.record(r, tc, auditdomain.ActionContactAccountUnlinked, contactID, map[string]any{"link_id": linkID.String(), "kind_changed": change.Changed, "kind_from": string(change.PreviousKind), "kind_to": string(change.Kind)})
	if change.Changed {
		h.record(r, tc, auditdomain.ActionContactReclassified, contactID, map[string]any{"kind_from": string(change.PreviousKind), "kind_to": string(change.Kind), "classification_source": string(domain.SourceManual)})
		h.recordKindRecompute(r, tc, contactID, change)
	}
	v, err := h.view(r.Context(), tc.TenantID, contactID)
	if err != nil {
		failDomain(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, v)
}

// SetPrimary: POST /contacts/{contact_id}/accounts/{link_id}/primary (contact.classify)
func (h *ClassificationHandler) SetPrimary(w http.ResponseWriter, r *http.Request) {
	tc, contactID, ok := h.authorize(w, r, permClassify)
	if !ok {
		return
	}
	linkID, err := uuid.Parse(r.PathValue("link_id"))
	if err != nil {
		http.Error(w, "invalid link_id", http.StatusBadRequest)
		return
	}
	if err := atomically(r.Context(), h.pool, func(ctx context.Context) error {
		return h.repo.SetPrimary(ctx, tc.TenantID, contactID, linkID)
	}); err != nil {
		failDomain(w, err)
		return
	}
	h.record(r, tc, auditdomain.ActionContactPrimaryAccount, contactID, map[string]any{"link_id": linkID.String()})
	v, err := h.view(r.Context(), tc.TenantID, contactID)
	if err != nil {
		failDomain(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, v)
}
