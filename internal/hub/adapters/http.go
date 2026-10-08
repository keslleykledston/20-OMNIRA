package adapters

import (
	"encoding/json"
	"errors"
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

// requestScope extracts the caller and the hub, and refuses every client-side tenant selector.
func requestScope(w http.ResponseWriter, r *http.Request) (actor, hub uuid.UUID, ok bool) {
	principal, err := authn.FromContext(r.Context())
	if err != nil || principal.UserID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return uuid.Nil, uuid.Nil, false
	}
	// A tenant chosen by the client is not an input of this API at all.
	if r.URL.Query().Has("tenant_id") {
		http.Error(w, "tenant selection is not accepted; access is derived from your grants", http.StatusBadRequest)
		return uuid.Nil, uuid.Nil, false
	}
	hubID, err := uuid.Parse(r.PathValue("hub_id"))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return uuid.Nil, uuid.Nil, false
	}
	return principal.UserID, hubID, true
}

func writeAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrAccessDenied):
		// One answer for "no such hub", "not a member", "no grant", "revoked", "out of scope": no enumeration
		// oracle, matching the API contract's Text404 ("not visible to the caller").
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, application.ErrInvalidRequest):
		http.Error(w, "invalid request", http.StatusBadRequest)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
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
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
	}
	items, next, err := h.repo.ListHubInboxItems(r.Context(), hubID, opts.Limit, opts.Cursor)
	if errors.Is(err, ErrInvalidCursor) {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	out := make([]inboxItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toDTO(it))
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
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if err := h.authz.AuthorizeHubMember(ctx, actor, hubID); err != nil {
		writeAccessError(w, err)
		return
	}
	item, err := h.repo.GetHubInboxItemByID(ctx, hubID, itemID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
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
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	msgs := []messageDTO{}
	for rows.Next() {
		var m messageDTO
		if err := rows.Scan(&m.ID, &m.Direction, &m.MessageType, &m.Body, &m.Status, &m.CreatedAt); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		msgs = append(msgs, m)
	}
	writeJSON(w, map[string]any{
		"item":         toDTO(item),
		"tenant":       map[string]any{"id": tc.TenantID, "name": tenantName},
		"access":       map[string]any{"source": tc.Source, "hub_id": tc.HubID, "grant_id": tc.EffectiveGrantID},
		"conversation": map[string]any{"id": item.ConversationID, "status": convStatus, "title": convTitle, "created_at": convCreated},
		"messages":     msgs,
	})
}
