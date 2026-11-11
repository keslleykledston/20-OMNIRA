package adapters

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/grouporganizer"
)

// GroupOrganizerHandler is the HTTP surface of ADR-0059 Gate 2 (catalog, links, relationships). Behind
// OMNIRA_GROUP_ORGANIZER_ENABLED and the same Access API flag as pools/audit; only a Hub member whose
// contract delegates 'groups' management for the tenant in question gets past the service (proven again
// inside every transaction); everything else is one uniform 404, same as the rest of this package.
type GroupOrganizerHandler struct{ svc *grouporganizer.Service }

func NewGroupOrganizerHandler(pool *pgxpool.Pool) *GroupOrganizerHandler {
	return &GroupOrganizerHandler{svc: grouporganizer.New(pool)}
}

func writeGroupOrganizerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, grouporganizer.ErrForbidden), errors.Is(err, grouporganizer.ErrNotFound):
		httpError(w, "not found", http.StatusNotFound)
	case errors.Is(err, grouporganizer.ErrKeyMismatch):
		httpError(w, "this Idempotency-Key was already used for a different request", http.StatusUnprocessableEntity)
	case errors.Is(err, grouporganizer.ErrInvalid):
		httpError(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, grouporganizer.ErrConflict):
		httpError(w, err.Error(), http.StatusConflict)
	default:
		log.Printf("hub group organizer: %v", err)
		httpError(w, "internal server error", http.StatusInternalServerError)
	}
}

// GET /api/v1/hubs/{hub_id}/groups/catalog?before=<RFC3339>&limit=<n>
func (h *GroupOrganizerHandler) ListCatalog(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	var before *time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			httpError(w, "invalid request", http.StatusBadRequest)
			return
		}
		before = &t
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httpError(w, "invalid request", http.StatusBadRequest)
			return
		}
		limit = n
	}
	items, err := h.svc.ListCatalog(r.Context(), actor, hub, before, limit)
	if err != nil {
		writeGroupOrganizerError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

type upsertCatalogRequest struct {
	SourceTenantID            uuid.UUID `json:"source_tenant_id"`
	SourceChannelConnectionID uuid.UUID `json:"source_channel_connection_id"`
	ProviderGroupID           string    `json:"provider_group_id"`
	DisplayName               string    `json:"display_name"`
}

// POST /api/v1/hubs/{hub_id}/groups/catalog
func (h *GroupOrganizerHandler) UpsertCatalogEntry(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	var body upsertCatalogRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	e, err := h.svc.UpsertCatalogEntry(r.Context(), actor, hub, body.SourceTenantID, body.SourceChannelConnectionID, body.ProviderGroupID, body.DisplayName)
	if err != nil {
		writeGroupOrganizerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(e)
}

// GET /api/v1/hubs/{hub_id}/groups/catalog/{group_id}/links
func (h *GroupOrganizerHandler) ListLinks(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	group, ok := pathUUID(w, r, "group_id")
	if !ok {
		return
	}
	items, err := h.svc.ListLinks(r.Context(), actor, hub, group)
	if err != nil {
		writeGroupOrganizerError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

type createLinkRequest struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Role     string    `json:"role"`
}

func writeCreated(w http.ResponseWriter, replayed bool, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if replayed {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(v)
}

// POST /api/v1/hubs/{hub_id}/groups/catalog/{group_id}/links  (Idempotency-Key required)
func (h *GroupOrganizerHandler) CreateLink(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	group, ok := pathUUID(w, r, "group_id")
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpError(w, "Idempotency-Key header is required", http.StatusBadRequest)
		return
	}
	var body createLinkRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	l, replayed, err := h.svc.CreateLink(r.Context(), actor, hub, group, body.TenantID, body.Role, key)
	if err != nil {
		writeGroupOrganizerError(w, err)
		return
	}
	writeCreated(w, replayed, l)
}

type revokeLinkRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

// POST /api/v1/hubs/{hub_id}/groups/catalog/{group_id}/links/{tenant_id}/revoke
func (h *GroupOrganizerHandler) RevokeLink(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	group, ok := pathUUID(w, r, "group_id")
	if !ok {
		return
	}
	tenant, ok := pathUUID(w, r, "tenant_id")
	if !ok {
		return
	}
	var body revokeLinkRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	if err := h.svc.RevokeLink(r.Context(), actor, hub, group, tenant, body.ExpectedVersion); err != nil {
		writeGroupOrganizerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type transferLinkRequest struct {
	ToTenantID      uuid.UUID `json:"to_tenant_id"`
	Role            string    `json:"role"`
	ExpectedVersion int64     `json:"expected_version"`
}

// POST /api/v1/hubs/{hub_id}/groups/catalog/{group_id}/links/{tenant_id}/transfer  (Idempotency-Key required)
func (h *GroupOrganizerHandler) TransferLink(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	group, ok := pathUUID(w, r, "group_id")
	if !ok {
		return
	}
	from, ok := pathUUID(w, r, "tenant_id")
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpError(w, "Idempotency-Key header is required", http.StatusBadRequest)
		return
	}
	var body transferLinkRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	l, replayed, err := h.svc.TransferLink(r.Context(), actor, hub, group, from, body.ToTenantID, body.Role, body.ExpectedVersion, key)
	if err != nil {
		writeGroupOrganizerError(w, err)
		return
	}
	writeCreated(w, replayed, l)
}

// GET /api/v1/hubs/{hub_id}/groups/catalog/{group_id}/resolve-context (ADR-0059 Gate 4)
//
// Read-only. Never creates a ticket, never sums two tenants' access: EffectiveAccessResolver still
// decides exactly one tenant per request downstream of whatever this returns. "no_access" and "the
// group does not exist in this hub" look identical on purpose (no enumeration oracle).
func (h *GroupOrganizerHandler) ResolveContext(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	group, ok := pathUUID(w, r, "group_id")
	if !ok {
		return
	}
	res, err := h.svc.ResolveContext(r.Context(), actor, hub, group)
	if err != nil {
		writeGroupOrganizerError(w, err)
		return
	}
	writeJSON(w, res)
}

// GET /api/v1/hubs/{hub_id}/groups/relationships
func (h *GroupOrganizerHandler) ListRelationships(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	items, err := h.svc.ListRelationships(r.Context(), actor, hub)
	if err != nil {
		writeGroupOrganizerError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

type createRelationshipRequest struct {
	ConsumerTenantID uuid.UUID `json:"consumer_tenant_id"`
	SupplierTenantID uuid.UUID `json:"supplier_tenant_id"`
	ServiceKind      string    `json:"service_kind"`
	ServiceReference string    `json:"service_reference"`
}

// POST /api/v1/hubs/{hub_id}/groups/relationships  (Idempotency-Key required)
func (h *GroupOrganizerHandler) CreateRelationship(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpError(w, "Idempotency-Key header is required", http.StatusBadRequest)
		return
	}
	var body createRelationshipRequest
	if !decodeWrite(w, r, &body) {
		return
	}
	rel, replayed, err := h.svc.CreateRelationship(r.Context(), actor, hub, body.ConsumerTenantID, body.SupplierTenantID, body.ServiceKind, body.ServiceReference, key)
	if err != nil {
		writeGroupOrganizerError(w, err)
		return
	}
	writeCreated(w, replayed, rel)
}

// POST /api/v1/hubs/{hub_id}/groups/relationships/{relationship_id}/revoke
func (h *GroupOrganizerHandler) RevokeRelationship(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	rel, ok := pathUUID(w, r, "relationship_id")
	if !ok {
		return
	}
	if err := h.svc.RevokeRelationship(r.Context(), actor, hub, rel); err != nil {
		writeGroupOrganizerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
