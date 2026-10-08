package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/application"
	"github.com/omnira/omnira/internal/hub/domain"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// HTTPHandler is the first, deliberately small vertical slice of the Hub API (read-only):
//
//	GET /api/v1/hubs/{hub_id}/inbox              the caller's authorized view of the hub's inbox
//	GET /api/v1/hubs/{hub_id}/inbox/{item_id}    open one item: its conversation header and messages
//
// Both MUST be mounted behind the authn middleware and tenancyadapters.UserSessionMiddleware, which opens
// the caller's own RLS session (user id set, never system admin). Without that session every query below
// sees zero rows: the slice fails closed.
//
// Tenant authority never comes from the request: the inbox is whatever RLS shows this user, and an item is
// opened by ITS id; its tenant and queue are read from the persisted row and then authorized.
type HTTPHandler struct {
	pool  *pgxpool.Pool
	repo  *PostgresHubRepository
	authz *application.HubAuthorizationService
}

func NewHTTPHandler(pool *pgxpool.Pool) *HTTPHandler {
	repo := NewPostgresHubRepository(pool)
	return &HTTPHandler{pool: pool, repo: repo, authz: application.NewHubAuthorizationService(repo)}
}

type inboxItemDTO struct {
	ID             uuid.UUID  `json:"id"`
	TenantID       uuid.UUID  `json:"tenant_id"`
	TenantName     string     `json:"tenant_name"`
	ConversationID uuid.UUID  `json:"conversation_id"`
	QueueID        *uuid.UUID `json:"queue_id,omitempty"`
	CustomerName   string     `json:"customer_name"`
	Channel        string     `json:"channel"`
	Status         string     `json:"status"`
	Priority       string     `json:"priority"`
	SLADueAt       *time.Time `json:"sla_due_at,omitempty"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	UnreadCount    int        `json:"unread_count"`
}

func toDTO(i *domain.HubInboxItem) inboxItemDTO {
	return inboxItemDTO{ID: i.ID, TenantID: i.TenantID, ConversationID: i.ConversationID, QueueID: i.QueueID, CustomerName: i.CustomerName,
		Channel: i.Channel, Status: i.Status, Priority: i.Priority, SLADueAt: i.SLADueAt, LastActivityAt: i.LastActivityAt, UnreadCount: i.UnreadCount}
}

// httpError is http.Error that also marks the response non-cacheable. (net/http's own Error strips Cache-Control
// on newer Go versions, so setting the header before calling it would not survive.)
func httpError(w http.ResponseWriter, msg string, code int) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = fmt.Fprintln(w, msg)
}

// requestScope extracts the caller and the hub, and refuses every client-side tenant selector.
func requestScope(w http.ResponseWriter, r *http.Request) (actor, hub uuid.UUID, ok bool) {
	principal, err := authn.FromContext(r.Context())
	if err != nil || principal.UserID == uuid.Nil {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return uuid.Nil, uuid.Nil, false
	}
	// A tenant chosen by the client is not an input of this API at all.
	if r.URL.Query().Has("tenant_id") {
		httpError(w, "tenant selection is not accepted; access is derived from your grants", http.StatusBadRequest)
		return uuid.Nil, uuid.Nil, false
	}
	hubID, err := uuid.Parse(r.PathValue("hub_id"))
	if err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return uuid.Nil, uuid.Nil, false
	}
	return principal.UserID, hubID, true
}

func writeAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrAccessDenied):
		// One answer for "no such hub", "not a member", "no grant", "revoked", "out of scope": no enumeration
		// oracle, matching the API contract's Text404 ("not visible to the caller").
		httpError(w, "not found", http.StatusNotFound)
	case errors.Is(err, application.ErrInvalidRequest):
		httpError(w, "invalid request", http.StatusBadRequest)
	default:
		httpError(w, "internal server error", http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// ListInbox returns the hub inbox items the CALLER may see. RLS, running under the caller's session,
// removes every tenant/queue they hold no live grant for; the handler never receives a tenant list.
func (h *HTTPHandler) ListInbox(w http.ResponseWriter, r *http.Request) {
	actor, hubID, ok := requestScope(w, r)
	if !ok {
		return
	}
	if err := h.authz.AuthorizeHubMember(r.Context(), actor, hubID); err != nil {
		writeAccessError(w, err)
		return
	}
	opts := pagination.ParsePageOptionsFromQuery(r)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			httpError(w, "invalid limit", http.StatusBadRequest)
			return
		}
	}
	items, next, err := h.repo.ListHubInboxItems(r.Context(), hubID, opts.Limit, opts.Cursor)
	if errors.Is(err, ErrInvalidCursor) {
		httpError(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	if err != nil {
		httpError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	names := h.tenantNames(r.Context(), items)
	out := make([]inboxItemDTO, 0, len(items))
	for _, it := range items {
		dto := toDTO(it)
		dto.TenantName = names[it.TenantID]
		out = append(out, dto)
	}
	pagination.WritePaginationHeaders(w, &pagination.PageResult{Count: len(out), Limit: opts.Limit, HasMore: next != "", NextCursor: next})
	writeJSON(w, map[string]any{"items": out, "has_more": next != "", "next_cursor": next, "count": len(out), "limit": opts.Limit})
}

type messageDTO struct {
	ID          uuid.UUID `json:"id"`
	Direction   string    `json:"direction"`
	MessageType string    `json:"message_type"`
	Body        string    `json:"body"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// OpenInboxItem opens one inbox item. Identity is resolved on the server: item id -> persisted row ->
// (tenant, queue) -> grant/contract/scope check -> EffectiveTenantContext -> conversation load.
func (h *HTTPHandler) OpenInboxItem(w http.ResponseWriter, r *http.Request) {
	actor, hubID, ok := requestScope(w, r)
	if !ok {
		return
	}
	itemID, err := uuid.Parse(r.PathValue("item_id"))
	if err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if err := h.authz.AuthorizeHubMember(ctx, actor, hubID); err != nil {
		writeAccessError(w, err)
		return
	}
	item, err := h.repo.GetHubInboxItemByID(ctx, hubID, itemID)
	if err != nil {
		httpError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if item == nil { // missing, or hidden by RLS: indistinguishable on purpose
		writeAccessError(w, application.ErrAccessDenied)
		return
	}
	correlation := r.Header.Get("X-Request-Id")
	if correlation == "" {
		correlation = uuid.NewString()
	}
	tc, err := h.authz.ResolveHubAccess(ctx, application.HubAccessRequest{
		ActorID: actor, HubID: hubID, TenantID: item.TenantID, QueueID: item.QueueID, CorrelationID: correlation,
	})
	if err != nil {
		writeAccessError(w, err)
		return
	}
	ctx = tenancydomain.WithTenantContext(ctx, tc)
	q := platformdb.QuerierFromContext(ctx, h.pool)

	var tenantName string
	var convStatus, convTitle string
	var convCreated time.Time
	err = q.QueryRow(ctx, `SELECT COALESCE(NULLIF(t.trade_name, ''), t.legal_name), c.status, COALESCE(c.title, ''), c.created_at
	                       FROM conversations c JOIN tenants t ON t.id = c.tenant_id
	                       WHERE c.id = $1 AND c.tenant_id = $2`, item.ConversationID, tc.TenantID).Scan(&tenantName, &convStatus, &convTitle, &convCreated)
	if err != nil {
		// RLS hid it or it vanished: same uniform answer, never a 500 that confirms existence
		writeAccessError(w, application.ErrAccessDenied)
		return
	}
	rows, err := q.Query(ctx, `SELECT id, direction, message_type, COALESCE(body, ''), status, created_at
	                           FROM messages WHERE conversation_id = $1 AND tenant_id = $2
	                           ORDER BY created_at ASC, id ASC LIMIT 200`, item.ConversationID, tc.TenantID)
	if err != nil {
		httpError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	msgs := []messageDTO{}
	for rows.Next() {
		var m messageDTO
		if err := rows.Scan(&m.ID, &m.Direction, &m.MessageType, &m.Body, &m.Status, &m.CreatedAt); err != nil {
			httpError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		msgs = append(msgs, m)
	}
	dto := toDTO(item)
	dto.TenantName = tenantName
	writeJSON(w, map[string]any{
		"item":         dto,
		"tenant":       map[string]any{"id": tc.TenantID, "name": tenantName},
		"access":       map[string]any{"source": tc.Source, "hub_id": tc.HubID, "grant_id": tc.EffectiveGrantID},
		"conversation": map[string]any{"id": item.ConversationID, "status": convStatus, "title": convTitle, "created_at": convCreated},
		"messages":     msgs,
	})
}

// tenantNames resolves display names for the tenants of the given rows, through the CALLER's session: a tenant the
// caller holds no live grant for is simply not readable (RLS), so a name can never leak for an unauthorized tenant.
func (h *HTTPHandler) tenantNames(ctx context.Context, items []*domain.HubInboxItem) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for _, it := range items {
		if !seen[it.TenantID] {
			seen[it.TenantID] = true
			ids = append(ids, it.TenantID)
		}
	}
	if len(ids) == 0 {
		return out
	}
	rows, err := platformdb.QuerierFromContext(ctx, h.pool).Query(ctx, `SELECT id, COALESCE(NULLIF(trade_name, ''), legal_name) FROM tenants WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err == nil {
			out[id] = name
		}
	}
	return out
}

type hubDTO struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Role string    `json:"role"`
}

// ListMyHubs returns the active hubs the caller is a member of. Membership grants no tenant access by itself; this
// only lets the UI know whether to offer the Hub workspace and which hub to open. Mount behind UserSessionMiddleware.
func (h *HTTPHandler) ListMyHubs(w http.ResponseWriter, r *http.Request) {
	principal, err := authn.FromContext(r.Context())
	if err != nil || principal.UserID == uuid.Nil {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT h.id, h.name, r.key
		FROM hub_memberships hm
		JOIN service_hubs h ON h.id = hm.hub_id
		JOIN roles r ON r.id = hm.role_id
		WHERE hm.user_id = $1 AND h.status = 'active'
		ORDER BY h.name, h.id`, principal.UserID)
	if err != nil {
		httpError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []hubDTO{}
	for rows.Next() {
		var d hubDTO
		if err := rows.Scan(&d.ID, &d.Name, &d.Role); err != nil {
			httpError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		httpError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"items": out})
}
