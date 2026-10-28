package adapters

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	"github.com/omnira/omnira/internal/external/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// AdminHandler is the tenant administrator's screen for external applications (Configurações > Integrações > Aplicações externas).
// These routes are cookie-session routes of a PERSON: they sit behind the same authentication, same-origin check and tenant authorization
// as every other settings route, they require integration.read / integration.manage (administrators; supervisors may only read), and they
// are not delegable to the Hub, so a Hub agent never sees or changes an application. The secret appears once, in the response that created it.
type AdminHandler struct {
	pool  *pgxpool.Pool
	store Store
	audit auditports.AuditEventRepository
	drain Drainer
	// scopeDrain frees what the installation may no longer hold after its scopes or allow-lists changed (see conversationcontrol.DrainOutOfScope).
	scopeDrain Drainer
}

func NewAdminHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository) *AdminHandler {
	return &AdminHandler{pool: pool, audit: audit}
}

// WithDrainer sets what runs, in the same transaction, when an installation is suspended or revoked (releases the conversations it holds).
func (h *AdminHandler) WithDrainer(d Drainer) *AdminHandler { h.drain = d; return h }

// WithScopeDrainer sets what runs, in the same transaction, when scopes or allow-lists are narrowed.
func (h *AdminHandler) WithScopeDrainer(d Drainer) *AdminHandler { h.scopeDrain = d; return h }

const (
	permRead   = "integration.read"
	permManage = "integration.manage"
	maxBody    = 64 << 10
)

func (h *AdminHandler) authorize(w http.ResponseWriter, r *http.Request, permission string) (*tenancydomain.TenantContext, platformdb.Querier, bool) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, nil, false
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	ok, err := tenancyadapters.ActorHasPermission(r.Context(), q, tc.TenantID, tc.ActorID, permission)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return nil, nil, false
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, nil, false
	}
	return tc, q, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields() // tenant_id and any other stray field are refused, never ignored
	if err := dec.Decode(dst); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type credentialView struct {
	ID         uuid.UUID  `json:"id"`
	KeyID      string     `json:"key_id"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Live       bool       `json:"live"`
}

type installationView struct {
	ID                uuid.UUID        `json:"id"`
	Name              string           `json:"name"`
	Status            string           `json:"status"`
	Scopes            []string         `json:"scopes"`
	QueueIDs          []uuid.UUID      `json:"queue_ids"`
	ChannelIDs        []uuid.UUID      `json:"channel_ids"`
	RatePerMin        int              `json:"rate_limit_per_min"`
	LeaseSeconds      *int             `json:"lease_seconds"`
	AllowOutsideHours bool             `json:"allow_outside_hours"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	SuspendedAt       *time.Time       `json:"suspended_at"`
	RevokedAt         *time.Time       `json:"revoked_at"`
	Credentials       []credentialView `json:"credentials,omitempty"`
}

func viewOf(in domain.Installation, creds []domain.Credential, now time.Time) installationView {
	v := installationView{ID: in.ID, Name: in.Name, Status: string(in.Status), Scopes: domain.ScopeStrings(in.Scopes), QueueIDs: in.QueueIDs, ChannelIDs: in.ChannelIDs,
		RatePerMin: in.RatePerMin, LeaseSeconds: in.LeaseSeconds, AllowOutsideHours: in.AllowOutsideHours, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt,
		SuspendedAt: in.SuspendedAt, RevokedAt: in.RevokedAt}
	if v.QueueIDs == nil {
		v.QueueIDs = []uuid.UUID{}
	}
	if v.ChannelIDs == nil {
		v.ChannelIDs = []uuid.UUID{}
	}
	for _, c := range creds {
		v.Credentials = append(v.Credentials, credView(c, now))
	}
	return v
}

func credView(c domain.Credential, now time.Time) credentialView {
	return credentialView{ID: c.ID, KeyID: c.KeyID, Label: c.Label, CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt, RevokedAt: c.RevokedAt, LastUsedAt: c.LastUsedAt, Live: c.Live(now)}
}

type issuedView struct {
	credentialView
	Token string `json:"token"` // shown once; the server keeps only a digest
}

func (h *AdminHandler) record(r *http.Request, id uuid.UUID, action auditdomain.AuditAction, meta map[string]any) {
	if h.audit == nil {
		return
	}
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil {
		return
	}
	correlation := uuid.Nil
	if rid := r.Header.Get("X-Request-ID"); rid != "" {
		if u, perr := uuid.Parse(rid); perr == nil {
			correlation = u // the log line and the audit row of one request share the id
		}
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, auditdomain.ResourceExternalApp, id, auditdomain.OutcomeSuccess, correlation)
	if err != nil {
		return
	}
	for k, v := range meta { // never a token, a digest or a secret
		ev.SetMetadata(k, v)
	}
	_ = h.audit.Store(r.Context(), ev)
}

func mapStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		http.Error(w, "installation not found", http.StatusNotFound)
	case errors.Is(err, domain.ErrNameTaken):
		http.Error(w, "an installation with this name already exists", http.StatusConflict)
	case errors.Is(err, domain.ErrInstallationRevoked):
		http.Error(w, "installation is revoked", http.StatusConflict)
	case errors.Is(err, domain.ErrTooManyCredentials):
		http.Error(w, "an installation holds at most 2 live credentials; revoke one first", http.StatusConflict)
	case errors.Is(err, ErrBadTransition):
		http.Error(w, "this status change is not allowed from the current status", http.StatusConflict)
	case errors.Is(err, ErrReferenceNotFound):
		http.Error(w, "a queue or channel line of the allow-list does not exist in this company", http.StatusBadRequest)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func installationID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("app_id"))
	if err != nil {
		http.Error(w, "invalid app_id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func parseIDs(raw []string) ([]uuid.UUID, bool) {
	out := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

// List: GET /tenants/{t}/integrations/apps
func (h *AdminHandler) List(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permRead)
	if !ok {
		return
	}
	items, err := h.store.List(r.Context(), q, tc.TenantID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	out := make([]installationView, 0, len(items))
	for _, in := range items {
		creds, err := h.store.Credentials(r.Context(), q, tc.TenantID, in.ID)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		out = append(out, viewOf(in, creds, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "count": len(out)})
}

// Get: GET /tenants/{t}/integrations/apps/{app_id}
func (h *AdminHandler) Get(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permRead)
	if !ok {
		return
	}
	id, ok := installationID(w, r)
	if !ok {
		return
	}
	in, err := h.store.Get(r.Context(), q, tc.TenantID, id)
	if err != nil {
		mapStoreError(w, err)
		return
	}
	creds, err := h.store.Credentials(r.Context(), q, tc.TenantID, id)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, viewOf(in, creds, time.Now()))
}

type auditEntryView struct {
	ID        uuid.UUID      `json:"id"`
	Action    string         `json:"action"`
	ActorID   uuid.UUID      `json:"actor_id"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// History: GET /tenants/{t}/integrations/apps/{app_id}/audit — a light trail of who changed this installation and when (never a token
// or a secret: record() already keeps those out of the metadata it stores).
func (h *AdminHandler) History(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permRead)
	if !ok {
		return
	}
	id, ok := installationID(w, r)
	if !ok {
		return
	}
	if _, err := h.store.Get(r.Context(), q, tc.TenantID, id); err != nil {
		mapStoreError(w, err)
		return
	}
	if h.audit == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []auditEntryView{}, "count": 0})
		return
	}
	events, err := h.audit.FindByResource(r.Context(), tc.TenantID, auditdomain.ResourceExternalApp, id, 50, 0)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	out := make([]auditEntryView, len(events))
	for i, e := range events {
		out[i] = auditEntryView{ID: e.ID, Action: string(e.Action), ActorID: e.ActorID, Metadata: e.Metadata, CreatedAt: e.CreatedAt}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "count": len(out)})
}

type createRequest struct {
	Name              string   `json:"name"`
	Scopes            []string `json:"scopes"`
	QueueIDs          []string `json:"queue_ids"`
	ChannelIDs        []string `json:"channel_ids"`
	RatePerMin        *int     `json:"rate_limit_per_min"`
	LeaseSeconds      *int     `json:"lease_seconds"`
	AllowOutsideHours bool     `json:"allow_outside_hours"`
	CredentialDays    *int     `json:"credential_ttl_days"`
	CredentialLabel   string   `json:"credential_label"`
}

// Create: POST /tenants/{t}/integrations/apps — returns the installation and its first credential (the token, once).
func (h *AdminHandler) Create(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permManage)
	if !ok {
		return
	}
	var req createRequest
	if !decodeBody(w, r, &req) {
		return
	}
	name, err := domain.ValidName(req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	scopes, err := domain.ParseScopes(req.Scopes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rate := domain.DefaultRatePerMin
	if req.RatePerMin != nil {
		rate = *req.RatePerMin
	}
	if err := domain.ValidRate(rate); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := domain.ValidLease(req.LeaseSeconds); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ttl := domain.DefaultCredentialTTL
	if req.CredentialDays != nil {
		ttl = time.Duration(*req.CredentialDays) * 24 * time.Hour
	}
	if err := domain.ValidTTL(ttl); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	queues, ok1 := parseIDs(req.QueueIDs)
	channels, ok2 := parseIDs(req.ChannelIDs)
	if !ok1 || !ok2 || len(req.CredentialLabel) > 80 {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	inst, cred, err := h.store.Create(r.Context(), q, tc.TenantID, tc.ActorID, CreateInput{Name: name, Scopes: scopes, QueueIDs: queues, ChannelIDs: channels,
		RatePerMin: rate, LeaseSeconds: req.LeaseSeconds, AllowOutsideHours: req.AllowOutsideHours, CredentialTTL: ttl, CredentialLabel: req.CredentialLabel})
	if err != nil {
		mapStoreError(w, err)
		return
	}
	h.record(r, inst.ID, auditdomain.ActionExternalAppCreated, map[string]any{"name": inst.Name, "scopes": domain.ScopeStrings(inst.Scopes), "credential_key_id": cred.KeyID})
	writeJSON(w, http.StatusCreated, map[string]any{
		"installation": viewOf(inst, nil, time.Now()),
		"credential":   issuedView{credentialView: credView(cred.Credential, time.Now()), Token: cred.Token},
	})
}

type updateRequest struct {
	Name              *string   `json:"name"`
	Scopes            *[]string `json:"scopes"`
	QueueIDs          *[]string `json:"queue_ids"`
	ChannelIDs        *[]string `json:"channel_ids"`
	RatePerMin        *int      `json:"rate_limit_per_min"`
	LeaseSeconds      *int      `json:"lease_seconds"`
	ClearLease        bool      `json:"clear_lease"`
	AllowOutsideHours *bool     `json:"allow_outside_hours"`
}

// Update: PATCH /tenants/{t}/integrations/apps/{app_id}
func (h *AdminHandler) Update(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permManage)
	if !ok {
		return
	}
	id, ok := installationID(w, r)
	if !ok {
		return
	}
	var req updateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	var in UpdateInput
	if req.Name != nil {
		name, err := domain.ValidName(*req.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		in.Name = &name
	}
	if req.Scopes != nil {
		scopes, err := domain.ParseScopes(*req.Scopes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		in.Scopes = scopes
	}
	if req.RatePerMin != nil {
		if err := domain.ValidRate(*req.RatePerMin); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		in.RatePerMin = req.RatePerMin
	}
	if req.LeaseSeconds != nil && req.ClearLease {
		http.Error(w, "lease_seconds and clear_lease are exclusive", http.StatusBadRequest)
		return
	}
	if err := domain.ValidLease(req.LeaseSeconds); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	in.LeaseSeconds, in.ClearLease, in.AllowOutsideHours = req.LeaseSeconds, req.ClearLease, req.AllowOutsideHours
	if req.QueueIDs != nil {
		ids, ok := parseIDs(*req.QueueIDs)
		if !ok {
			http.Error(w, "invalid queue_ids", http.StatusBadRequest)
			return
		}
		in.QueueIDs = &ids
	}
	if req.ChannelIDs != nil {
		ids, ok := parseIDs(*req.ChannelIDs)
		if !ok {
			http.Error(w, "invalid channel_ids", http.StatusBadRequest)
			return
		}
		in.ChannelIDs = &ids
	}
	inst, err := h.store.Update(r.Context(), q, tc.TenantID, id, in)
	if err != nil {
		mapStoreError(w, err)
		return
	}
	released := 0
	if h.scopeDrain != nil && (in.Scopes != nil || in.QueueIDs != nil || in.ChannelIDs != nil) {
		var derr error
		if released, derr = h.scopeDrain(r.Context(), q, tc.TenantID, id, "scope or allow-list changed"); derr != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}
	h.record(r, id, auditdomain.ActionExternalAppUpdated, map[string]any{"name": inst.Name, "scopes": domain.ScopeStrings(inst.Scopes), "released_conversations": released})
	creds, _ := h.store.Credentials(r.Context(), q, tc.TenantID, id)
	writeJSON(w, http.StatusOK, viewOf(inst, creds, time.Now()))
}

// Transition: POST /tenants/{t}/integrations/apps/{app_id}/{suspend|reactivate|revoke}
func (h *AdminHandler) Transition(t Transition, action auditdomain.AuditAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tc, q, ok := h.authorize(w, r, permManage)
		if !ok {
			return
		}
		id, ok := installationID(w, r)
		if !ok {
			return
		}
		inst, released, err := h.store.Apply(r.Context(), q, tc.TenantID, id, t, h.drain)
		if err != nil {
			mapStoreError(w, err)
			return
		}
		h.record(r, id, action, map[string]any{"name": inst.Name, "released_conversations": released})
		creds, _ := h.store.Credentials(r.Context(), q, tc.TenantID, id)
		writeJSON(w, http.StatusOK, map[string]any{"installation": viewOf(inst, creds, time.Now()), "released_conversations": released})
	}
}

type issueRequest struct {
	Label string `json:"label"`
	Days  *int   `json:"ttl_days"`
}

// IssueCredential: POST /tenants/{t}/integrations/apps/{app_id}/credentials — the next credential during a rotation (token shown once).
func (h *AdminHandler) IssueCredential(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permManage)
	if !ok {
		return
	}
	id, ok := installationID(w, r)
	if !ok {
		return
	}
	var req issueRequest
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	ttl := domain.DefaultCredentialTTL
	if req.Days != nil {
		ttl = time.Duration(*req.Days) * 24 * time.Hour
	}
	if err := domain.ValidTTL(ttl); err != nil || len(req.Label) > 80 {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	cred, err := h.store.IssueCredential(r.Context(), q, tc.TenantID, id, tc.ActorID, req.Label, ttl)
	if err != nil {
		mapStoreError(w, err)
		return
	}
	h.record(r, id, auditdomain.ActionExternalAppCredentialIssued, map[string]any{"credential_key_id": cred.KeyID})
	writeJSON(w, http.StatusCreated, issuedView{credentialView: credView(cred.Credential, time.Now()), Token: cred.Token})
}

// RevokeCredential: DELETE /tenants/{t}/integrations/apps/{app_id}/credentials/{credential_id}
func (h *AdminHandler) RevokeCredential(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permManage)
	if !ok {
		return
	}
	id, ok := installationID(w, r)
	if !ok {
		return
	}
	cid, err := uuid.Parse(r.PathValue("credential_id"))
	if err != nil {
		http.Error(w, "invalid credential_id", http.StatusBadRequest)
		return
	}
	if err := h.store.RevokeCredential(r.Context(), q, tc.TenantID, id, cid); err != nil {
		mapStoreError(w, err)
		return
	}
	// the last usable credential gone: the application can no longer say a word, so what it holds goes back to the company now (an expired one is caught by the worker)
	if h.drain != nil {
		var live int
		if err := q.QueryRow(r.Context(), `SELECT count(*) FROM external_app_credentials WHERE tenant_id = $1 AND installation_id = $2 AND revoked_at IS NULL AND expires_at > now()`, tc.TenantID, id).Scan(&live); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if live == 0 {
			if _, err := h.drain(r.Context(), q, tc.TenantID, id, "no usable credential"); err != nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
		}
	}
	h.record(r, id, auditdomain.ActionExternalAppCredentialRevoked, map[string]any{"credential_id": cid.String()})
	w.WriteHeader(http.StatusNoContent)
}
