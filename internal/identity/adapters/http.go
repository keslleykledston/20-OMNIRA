package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	"github.com/omnira/omnira/internal/identity/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

const permManage = "identity.manage"

// Handler serves the internal-identity API. EVERY route needs identity.manage (a sensitive permission: it decides who
// the system considers staff). The tenant always comes from the session.
type Handler struct {
	pool      *pgxpool.Pool
	repo      *Repository
	audit     auditports.AuditEventRepository
	conflicts metric.Int64Counter
}

func NewHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository) *Handler {
	conflicts, _ := otel.Meter("omnira/identity").Int64Counter("identity_conflict_events_total")
	return &Handler{pool: pool, repo: NewRepository(pool), audit: audit, conflicts: conflicts}
}

// countConflict records a conflict event (opened | resolved_confirmed_internal | resolved_identity_revoked): no ids.
func (h *Handler) countConflict(ctx context.Context, event string, n int) {
	if h.conflicts != nil && n > 0 {
		h.conflicts.Add(ctx, int64(n), metric.WithAttributes(attribute.String("event", event)))
	}
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) (*tenancydomain.TenantContext, bool) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	var ok bool
	if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(SELECT 1 FROM memberships m JOIN role_permissions rp ON rp.role_id = m.role_id
		              WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tc.TenantID, tc.ActorID, permManage).Scan(&ok); err != nil {
		http.Error(w, "failed to check permission", http.StatusInternalServerError)
		return nil, false
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return tc, true
}

type identityDTO struct {
	ID                 uuid.UUID `json:"id"`
	UserID             uuid.UUID `json:"user_id"`
	Type               string    `json:"identity_type"`
	Value              string    `json:"value"`
	Status             string    `json:"status"`
	VerificationSource *string   `json:"verification_source,omitempty"`
	VerifiedAt         *string   `json:"verified_at,omitempty"`
	RevokedAt          *string   `json:"revoked_at,omitempty"`
	CreatedAt          string    `json:"created_at"`
}

func ts(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

func toDTO(i domain.Identity) identityDTO {
	d := identityDTO{ID: i.ID, UserID: i.UserID, Type: string(i.Type), Value: i.Normalized, Status: string(i.Status), VerifiedAt: ts(i.VerifiedAt), RevokedAt: ts(i.RevokedAt), CreatedAt: i.CreatedAt.UTC().Format(time.RFC3339Nano)}
	if i.VerificationSource != nil {
		s := string(*i.VerificationSource)
		d.VerificationSource = &s
	}
	return d
}

type conflictDTO struct {
	ID         uuid.UUID `json:"id"`
	IdentityID uuid.UUID `json:"identity_id"`
	ContactID  uuid.UUID `json:"contact_id"`
	Status     string    `json:"status"`
	Resolution *string   `json:"resolution,omitempty"`
	Note       *string   `json:"note,omitempty"`
	DetectedAt string    `json:"detected_at"`
	ResolvedAt *string   `json:"resolved_at,omitempty"`
}

func toConflictDTO(c domain.Conflict) conflictDTO {
	d := conflictDTO{ID: c.ID, IdentityID: c.IdentityID, ContactID: c.ContactID, Status: c.Status, Note: c.Note, DetectedAt: c.DetectedAt.UTC().Format(time.RFC3339Nano), ResolvedAt: ts(c.ResolvedAt)}
	if c.Resolution != nil {
		s := string(*c.Resolution)
		d.Resolution = &s
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
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrConflictNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, domain.ErrInvalidIdentity), errors.Is(err, domain.ErrInvalidResolution):
		http.Error(w, "invalid value", http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrUserNotInTenant):
		http.Error(w, "the user is not a member of this tenant", http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrSourceNotAllowed):
		http.Error(w, "this verification source is reserved for system flows", http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrDuplicate), errors.Is(err, domain.ErrAlreadyVerified), errors.Is(err, domain.ErrInvalidState), errors.Is(err, domain.ErrConflictNotOpen):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		log.Printf("identity: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (h *Handler) record(r *http.Request, tc *tenancydomain.TenantContext, action auditdomain.AuditAction, resource auditdomain.ResourceType, id uuid.UUID, meta map[string]any) {
	if h.audit == nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, resource, id, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = h.audit.Store(r.Context(), ev)
}

// List: GET /tenants/{tenant_id}/identities?user_id=&status=
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var userID *uuid.UUID
	if v := r.URL.Query().Get("user_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			http.Error(w, "invalid user_id", http.StatusBadRequest)
			return
		}
		userID = &id
	}
	var status *domain.Status
	if v := r.URL.Query().Get("status"); v != "" {
		s := domain.Status(v)
		if s != domain.StatusPending && s != domain.StatusVerified && s != domain.StatusRevoked {
			http.Error(w, "invalid status", http.StatusBadRequest)
			return
		}
		status = &s
	}
	list, err := h.repo.List(r.Context(), tc.TenantID, userID, status)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]identityDTO, 0, len(list))
	for _, i := range list {
		out = append(out, toDTO(i))
	}
	write(w, http.StatusOK, map[string]any{"items": out})
}

type createRequest struct {
	UserID       uuid.UUID  `json:"user_id"`
	Type         string     `json:"identity_type"`
	Value        string     `json:"value"`
	Provider     string     `json:"provider"`
	ConnectionID *uuid.UUID `json:"connection_id"`
}

// Create: POST /tenants/{tenant_id}/identities — always PENDING. Verification is a separate, audited step.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var req createRequest
	if !decode(w, r, &req) {
		return
	}
	typ := domain.Type(req.Type)
	var normalized, scope string
	var err error
	switch typ {
	case domain.TypePhone:
		normalized, err = domain.NormalizePhone(req.Value)
	case domain.TypeEmail:
		normalized, err = domain.NormalizeEmail(req.Value)
	case domain.TypeProviderParticipant:
		if req.Provider == "" || req.ConnectionID == nil {
			http.Error(w, "provider and connection_id are required for a provider participant", http.StatusBadRequest)
			return
		}
		var own bool
		if qerr := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM channel_connections WHERE tenant_id=$1 AND id=$2 AND lower(provider)=lower($3))`, tc.TenantID, *req.ConnectionID, req.Provider).Scan(&own); qerr != nil || !own {
			http.Error(w, "unknown connection", http.StatusUnprocessableEntity)
			return
		}
		scope = domain.ParticipantScope(req.Provider, *req.ConnectionID)
		normalized, err = domain.NormalizeParticipant(req.Value)
	default:
		http.Error(w, "identity_type must be phone, email or provider_participant", http.StatusBadRequest)
		return
	}
	if err != nil || req.UserID == uuid.Nil {
		http.Error(w, "invalid value", http.StatusUnprocessableEntity)
		return
	}
	var created *domain.Identity
	if err := platformdb.WithSavepoint(r.Context(), h.pool, func(ctx context.Context) error {
		created, err = h.repo.Create(ctx, tc.TenantID, tc.ActorID, req.UserID, typ, scope, req.Value, normalized)
		return err
	}); err != nil {
		fail(w, err)
		return
	}
	h.record(r, tc, auditdomain.ActionUserIdentityCreated, auditdomain.ResourceUserIdentity, created.ID, map[string]any{"identity_type": string(typ), "user_id": created.UserID.String()})
	write(w, http.StatusCreated, toDTO(*created))
}

type verifyRequest struct {
	VerificationSource string `json:"verification_source"`
}

// Verify: POST /tenants/{tenant_id}/identities/{identity_id}/verify. The API accepts only the human "admin" source;
// provider_verified / challenge / import_verified are reserved for the system flows that prove them, and nothing here
// can ever be an AI decision.
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("identity_id"))
	if err != nil {
		http.Error(w, "invalid identity_id", http.StatusBadRequest)
		return
	}
	var req verifyRequest
	if !decode(w, r, &req) {
		return
	}
	if domain.VerificationSource(req.VerificationSource) != domain.SourceAdmin {
		fail(w, domain.ErrSourceNotAllowed)
		return
	}
	var res *VerifyResult
	if err := platformdb.WithSavepoint(r.Context(), h.pool, func(ctx context.Context) error {
		res, err = h.repo.Verify(ctx, tc.TenantID, tc.ActorID, id, domain.SourceAdmin)
		return err
	}); err != nil {
		fail(w, err)
		return
	}
	h.record(r, tc, auditdomain.ActionUserIdentityVerified, auditdomain.ResourceUserIdentity, id, map[string]any{"verification_source": string(domain.SourceAdmin), "conflicts_opened": len(res.Conflicts)})
	h.countConflict(r.Context(), "opened", len(res.Conflicts))
	conflicts := make([]conflictDTO, 0, len(res.Conflicts))
	for _, c := range res.Conflicts {
		h.record(r, tc, auditdomain.ActionIdentityConflictFound, auditdomain.ResourceIdentityConflict, c.ID, map[string]any{"identity_id": id.String(), "contact_id": c.ContactID.String()})
		conflicts = append(conflicts, toConflictDTO(c))
	}
	write(w, http.StatusOK, map[string]any{"identity": toDTO(*res.Identity), "conflicts": conflicts})
}

// Revoke: POST /tenants/{tenant_id}/identities/{identity_id}/revoke
func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("identity_id"))
	if err != nil {
		http.Error(w, "invalid identity_id", http.StatusBadRequest)
		return
	}
	var got *domain.Identity
	if err := platformdb.WithSavepoint(r.Context(), h.pool, func(ctx context.Context) error {
		got, err = h.repo.Revoke(ctx, tc.TenantID, tc.ActorID, id)
		return err
	}); err != nil {
		fail(w, err)
		return
	}
	h.record(r, tc, auditdomain.ActionUserIdentityRevoked, auditdomain.ResourceUserIdentity, id, nil)
	write(w, http.StatusOK, toDTO(*got))
}

// ListConflicts: GET /tenants/{tenant_id}/identity-conflicts?status=open|all (default open)
func (h *Handler) ListConflicts(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r)
	if !ok {
		return
	}
	list, err := h.repo.ListConflicts(r.Context(), tc.TenantID, r.URL.Query().Get("status") != "all")
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]conflictDTO, 0, len(list))
	for _, c := range list {
		out = append(out, toConflictDTO(c))
	}
	write(w, http.StatusOK, map[string]any{"items": out})
}

type resolveRequest struct {
	Resolution string `json:"resolution"`
	Note       string `json:"note"`
}

// ResolveConflict: POST /tenants/{tenant_id}/identity-conflicts/{conflict_id}/resolve
func (h *Handler) ResolveConflict(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("conflict_id"))
	if err != nil {
		http.Error(w, "invalid conflict_id", http.StatusBadRequest)
		return
	}
	var req resolveRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Note) > 500 {
		http.Error(w, "note too long", http.StatusBadRequest)
		return
	}
	var got *domain.Conflict
	if err := platformdb.WithSavepoint(r.Context(), h.pool, func(ctx context.Context) error {
		got, err = h.repo.ResolveConflict(ctx, tc.TenantID, tc.ActorID, id, domain.Resolution(req.Resolution), req.Note)
		return err
	}); err != nil {
		fail(w, err)
		return
	}
	h.countConflict(r.Context(), "resolved_"+req.Resolution, 1)
	h.record(r, tc, auditdomain.ActionIdentityConflictResolved, auditdomain.ResourceIdentityConflict, id, map[string]any{"resolution": req.Resolution})
	write(w, http.StatusOK, toConflictDTO(*got))
}
