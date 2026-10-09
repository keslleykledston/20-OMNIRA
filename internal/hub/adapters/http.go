package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/application"
	"github.com/omnira/omnira/internal/hub/domain"
	"github.com/omnira/omnira/internal/hub/provisioning"
	"github.com/omnira/omnira/internal/hub/replying"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapp "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// HTTPHandler is the Hub API:
//
//	GET  /api/v1/hubs/{hub_id}/inbox                       the caller's authorized view of the hub's inbox
//	GET  /api/v1/hubs/{hub_id}/inbox/{item_id}             open one item: its conversation header and messages
//	POST /api/v1/hubs/{hub_id}/inbox/{item_id}/claim       take the conversation (reply-capable grant required)
//	POST /api/v1/hubs/{hub_id}/inbox/{item_id}/messages    answer it (reply-capable grant, claimed, Idempotency-Key)
//
// The reads are RLS-only; the writes authorize in the caller's session and execute through internal/hub/replying.
// All routes MUST be mounted behind the authn middleware and tenancyadapters.UserSessionMiddleware, which opens
// the caller's own RLS session (user id set, never system admin). Without that session every query below
// sees zero rows: the slice fails closed.
//
// Tenant authority never comes from the request: the inbox is whatever RLS shows this user, and an item is
// opened by ITS id; its tenant and queue are read from the persisted row and then authorized.
type HTTPHandler struct {
	pool  *pgxpool.Pool
	repo  *PostgresHubRepository
	authz *application.HubAuthorizationService
	reply *replying.Service
	// adminEnabled mirrors OMNIRA_HUB_ADMIN_API_ENABLED so the hub list only advertises the screen when its API is mounted.
	adminEnabled bool
	// accessEnabled mirrors OMNIRA_HUB_ACCESS_API_ENABLED for the same reason (the Access panel).
	accessEnabled bool
}

// WithAdminAPI marks the company-management API as mounted (see ListMyHubs).
func (h *HTTPHandler) WithAdminAPI(enabled bool) *HTTPHandler { h.adminEnabled = enabled; return h }

// WithAccessAPI marks the Access panel API as mounted (see ListMyHubs).
func (h *HTTPHandler) WithAccessAPI(enabled bool) *HTTPHandler { h.accessEnabled = enabled; return h }

func NewHTTPHandler(pool *pgxpool.Pool) *HTTPHandler {
	repo := NewPostgresHubRepository(pool)
	authz := application.NewHubAuthorizationService(repo)
	return &HTTPHandler{pool: pool, repo: repo, authz: authz,
		reply: replying.New(pool, authz, repo, messagesadapters.NewPostgresOutboundStore(pool))}
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
	only, ok := parseCompanyFilter(w, r)
	if !ok {
		return
	}
	items, next, err := h.repo.ListHubInboxItems(r.Context(), hubID, only, opts.Limit, opts.Cursor)
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
	writeJSON(w, map[string]any{"items": out, "has_more": next != "", "next_cursor": next, "count": len(out), "limit": opts.Limit,
		"companies": h.companiesOf(r.Context(), hubID, actor)})
}

// parseCompanyFilter reads `companies=<uuid>,<uuid>`: a screen filter that only NARROWS the caller's authorized view. It is
// not a tenant selector (tenant_id is still refused): rows outside the caller's grants are invisible whatever it says.
func parseCompanyFilter(w http.ResponseWriter, r *http.Request) ([]uuid.UUID, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("companies"))
	if raw == "" {
		return nil, true
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 50 {
		httpError(w, "too many companies", http.StatusBadRequest)
		return nil, false
	}
	out := make([]uuid.UUID, 0, len(parts))
	for _, p := range parts {
		id, err := uuid.Parse(strings.TrimSpace(p))
		if err != nil || id == uuid.Nil {
			httpError(w, "invalid companies filter", http.StatusBadRequest)
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

type companyDTO struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	// FullContext tells the screen it may open the full workspace of this company for this person (ADR-0040): serving is enabled and the
	// person holds conversation.read there through this hub, right now. Display only: every request is decided again, by the server and the data layer.
	FullContext bool `json:"full_context"`
}

// companiesOf lists the companies this person is currently allowed to serve through the hub, to feed the screen's filter.
// It only describes (names for a dropdown); what is READABLE is still decided by RLS on every row.
func (h *HTTPHandler) companiesOf(ctx context.Context, hubID, actor uuid.UUID) []companyDTO {
	out := []companyDTO{}
	rows, err := platformdb.QuerierFromContext(ctx, h.pool).Query(ctx, `
		SELECT DISTINCT t.id, COALESCE(NULLIF(t.trade_name, ''), t.legal_name) AS name,
		       ($3::boolean AND COALESCE('conversation.read' = ANY (delegated_permissions(t.id, $2, $1)), false)) AS full_context
		FROM effective_access_grants g
		JOIN hub_tenant_service_contracts k ON k.id = g.service_contract_id AND k.status = 'active' AND k.valid_from <= now() AND (k.valid_until IS NULL OR k.valid_until > now())
		JOIN tenants t ON t.id = g.tenant_id AND t.status = 'active'
		WHERE g.hub_id = $1 AND g.user_id = $2 AND g.status = 'active' AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())
		ORDER BY 2, 1`, hubID, actor, tenancyadapters.DelegatedServingEnabled())
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c companyDTO
		if err := rows.Scan(&c.ID, &c.Name, &c.FullContext); err == nil {
			out = append(out, c)
		}
	}
	return out
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
	var assignee *uuid.UUID
	err = q.QueryRow(ctx, `SELECT COALESCE(NULLIF(t.trade_name, ''), t.legal_name), c.status, COALESCE(c.title, ''), c.created_at, c.assigned_to_user_id
	                       FROM conversations c JOIN tenants t ON t.id = c.tenant_id
	                       WHERE c.id = $1 AND c.tenant_id = $2`, item.ConversationID, tc.TenantID).Scan(&tenantName, &convStatus, &convTitle, &convCreated, &assignee)
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
	// Who holds the conversation, WITHOUT naming anyone else: the composer only needs to know whether it is the caller.
	assignment := "none"
	if assignee != nil {
		assignment = "other"
		if *assignee == actor {
			assignment = "me"
		}
	}
	writeJSON(w, map[string]any{
		"item":         dto,
		"tenant":       map[string]any{"id": tc.TenantID, "name": tenantName},
		"access":       map[string]any{"source": tc.Source, "hub_id": tc.HubID, "grant_id": tc.EffectiveGrantID, "can_reply": tc.CanReply},
		"conversation": map[string]any{"id": item.ConversationID, "status": convStatus, "title": convTitle, "created_at": convCreated, "assignment": assignment},
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
	// CanManageCompanies only tells the UI whether to offer the company-management screen. The server re-decides every
	// request (AdminHandler); a client that ignores or forges this gains nothing.
	CanManageCompanies bool `json:"can_manage_companies"`
	// CanManageAccess: same idea for the Access panel (people and permissions); true for hub admins when it is mounted.
	CanManageAccess bool `json:"can_manage_access"`
	// CanManageInstances: at least one instance where the contract delegates management to this person (ADR-0038 phase 3).
	CanManageInstances bool `json:"can_manage_instances"`
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
	rows.Close()
	if h.accessEnabled {
		for i := range out {
			out[i].CanManageAccess = out[i].Role == "hub_admin"
		}
	}
	if h.adminEnabled {
		isOp, err := provisioning.IsPlatformOperator(r.Context(), platformdb.QuerierFromContext(r.Context(), h.pool), principal.UserID)
		if err != nil {
			httpError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		for i := range out {
			out[i].CanManageCompanies = isOp && out[i].Role == "hub_admin"
			// asked of the database (the same function the row-level policies use), for THIS person, never inferred from the role alone
			if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(),
				`SELECT EXISTS (SELECT 1 FROM hub_tenant_service_contracts c WHERE c.hub_id = $1 AND has_hub_manage_access(c.tenant_id, $2, NULL, $1))`,
				out[i].ID, principal.UserID).Scan(&out[i].CanManageInstances); err != nil {
				httpError(w, "internal server error", http.StatusInternalServerError)
				return
			}
		}
	}
	writeJSON(w, map[string]any{"items": out})
}

type managedInstanceDTO struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Name     string    `json:"name"`
	Scopes   []string  `json:"scopes"` // the delegated scopes this person may use here
}

// GET /api/v1/hubs/{hub_id}/managed — the instances of the hub this person may MANAGE (ADR-0038 phase 3), with the scopes. Asked of the
// database with the function the policies use; an unknown hub, a non-member and "nothing delegated" are all an empty list.
func (h *HTTPHandler) ListManaged(w http.ResponseWriter, r *http.Request) {
	actor, hub, ok := requestScope(w, r)
	if !ok {
		return
	}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `SELECT tenant_id, name, scopes FROM managed_instances($2, $1)`, hub, actor)
	if err != nil {
		httpError(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []managedInstanceDTO{}
	for rows.Next() {
		var d managedInstanceDTO
		if err := rows.Scan(&d.TenantID, &d.Name, &d.Scopes); err != nil {
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

// ---------------------------------------------------------------- write path (claim / reply)

type replyScope struct {
	actor, hub, item, expectedTenant uuid.UUID
}

type claimRequest struct {
	ExpectedTenantID uuid.UUID `json:"expected_tenant_id"`
}

type replyRequest struct {
	ExpectedTenantID uuid.UUID `json:"expected_tenant_id"`
	Text             string    `json:"text"`
}

// decodeWrite reads the small JSON body of a write. The only tenant-ish field a write carries is expected_tenant_id:
// the company the screen showed. It authorizes nothing; it only makes the server refuse when the screen and the
// persisted item disagree ("answering as A while looking at B").
func decodeWrite(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	// exactly one JSON value: `{"a":1}{"b":2}` is not a request (Codex L3)
	if dec.More() {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func writeReplyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrAccessDenied), errors.Is(err, messagesapp.ErrNotFound):
		httpError(w, "not found", http.StatusNotFound)
	case errors.Is(err, application.ErrReplyNotAllowed):
		httpError(w, "your access to this company is read-only", http.StatusForbidden)
	case errors.Is(err, replying.ErrTenantMismatch):
		httpError(w, "the company on screen is not the company of this conversation; reload", http.StatusConflict)
	case errors.Is(err, replying.ErrTaken), errors.Is(err, messagesapp.ErrNotAssignedToYou):
		httpError(w, "conversation is assigned to another agent", http.StatusConflict)
	case errors.Is(err, replying.ErrNotHolder):
		httpError(w, "the conversation is not yours: only who holds it may transfer it", http.StatusConflict)
	case errors.Is(err, replying.ErrTransferTarget):
		httpError(w, "transfer target not available: that person cannot take this conversation now", http.StatusConflict)
	case errors.Is(err, replying.ErrClosed), errors.Is(err, messagesapp.ErrConversationClosed):
		httpError(w, "conversation is finalized: the contact's next message starts a new attendance", http.StatusConflict)
	case errors.Is(err, messagesapp.ErrUnassigned):
		httpError(w, "claim the conversation before replying", http.StatusConflict)
	case errors.Is(err, messagesapp.ErrChannelUnavailable):
		httpError(w, "conversation has no active text channel", http.StatusConflict)
	case errors.Is(err, messagesapp.ErrWindowClosed):
		httpError(w, "customer service window closed: free text is only accepted within 24 h of the customer's last message", http.StatusConflict)
	case errors.Is(err, messagesapp.ErrConversationChanged):
		httpError(w, "conversation changed, retry", http.StatusConflict)
	case errors.Is(err, messagesapp.ErrInvalidKey):
		httpError(w, "Idempotency-Key must be 8-128 chars of [A-Za-z0-9._:-]", http.StatusBadRequest)
	case errors.Is(err, messagesapp.ErrInvalidText), errors.Is(err, messagesapp.ErrIdempotencyMismatch):
		httpError(w, "invalid text or Idempotency-Key reused with a different request", http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrInvalidRequest):
		httpError(w, "invalid request", http.StatusBadRequest)
	default:
		log.Printf("hub write: %v", err)
		httpError(w, "internal server error", http.StatusInternalServerError)
	}
}

func (h *HTTPHandler) writeScope(w http.ResponseWriter, r *http.Request) (replyScope, bool) {
	actor, hubID, ok := requestScope(w, r)
	if !ok {
		return replyScope{}, false
	}
	itemID, err := uuid.Parse(r.PathValue("item_id"))
	if err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return replyScope{}, false
	}
	return replyScope{actor: actor, hub: hubID, item: itemID}, true
}

func correlationOf(r *http.Request) string {
	if v := r.Header.Get("X-Request-Id"); v != "" {
		return v
	}
	return uuid.NewString()
}

// ClaimItem serves POST /hubs/{hub_id}/inbox/{item_id}/claim: the agent takes the conversation. Needs a reply-capable grant.
func (h *HTTPHandler) ClaimItem(w http.ResponseWriter, r *http.Request) {
	sc, ok := h.writeScope(w, r)
	if !ok {
		return
	}
	var req claimRequest
	if !decodeWrite(w, r, &req) {
		return
	}
	t, err := h.reply.Authorize(r.Context(), sc.actor, sc.hub, sc.item, req.ExpectedTenantID, correlationOf(r))
	if err != nil {
		writeReplyError(w, err)
		return
	}
	changed, err := h.reply.Claim(r.Context(), sc.actor, t)
	if err != nil {
		writeReplyError(w, err)
		return
	}
	writeJSON(w, map[string]any{"item_id": t.Item.ID, "conversation_id": t.Item.ConversationID, "changed": changed,
		"tenant": map[string]any{"id": t.Item.TenantID, "name": t.TenantName}})
}

type transferRequest struct {
	ExpectedTenantID uuid.UUID  `json:"expected_tenant_id"`
	ToUserID         *uuid.UUID `json:"to_user_id"` // null = give it back to the queue
}

// TransferCandidates serves GET /hubs/{hub_id}/inbox/{item_id}/transfer-candidates?expected_tenant_id=: who the holder may hand the
// conversation to. The company on screen is checked like in every write (a stale screen gets 409, never another company's people).
func (h *HTTPHandler) TransferCandidates(w http.ResponseWriter, r *http.Request) {
	sc, ok := h.writeScope(w, r)
	if !ok {
		return
	}
	expected, err := uuid.Parse(r.URL.Query().Get("expected_tenant_id"))
	if err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	t, err := h.reply.Authorize(r.Context(), sc.actor, sc.hub, sc.item, expected, correlationOf(r))
	if err != nil {
		writeReplyError(w, err)
		return
	}
	items, err := h.reply.Candidates(r.Context(), sc.actor, t)
	if err != nil {
		writeReplyError(w, err)
		return
	}
	writeJSON(w, map[string]any{"items": items})
}

// TransferItem serves POST /hubs/{hub_id}/inbox/{item_id}/transfer: the holder hands the conversation to another person of the hub, or
// gives it back to the queue (to_user_id null). Needs a reply-capable grant, and the person chosen must have one too.
func (h *HTTPHandler) TransferItem(w http.ResponseWriter, r *http.Request) {
	sc, ok := h.writeScope(w, r)
	if !ok {
		return
	}
	var req transferRequest
	if !decodeWrite(w, r, &req) {
		return
	}
	t, err := h.reply.Authorize(r.Context(), sc.actor, sc.hub, sc.item, req.ExpectedTenantID, correlationOf(r))
	if err != nil {
		writeReplyError(w, err)
		return
	}
	if err := h.reply.Transfer(r.Context(), sc.actor, t, req.ToUserID); err != nil {
		writeReplyError(w, err)
		return
	}
	writeJSON(w, map[string]any{"conversation_id": t.Item.ConversationID, "assigned_to": req.ToUserID,
		"tenant": map[string]any{"id": t.Item.TenantID, "name": t.TenantName}})
}

// ReplyItem serves POST /hubs/{hub_id}/inbox/{item_id}/messages: the agent answers. Idempotency-Key is required.
func (h *HTTPHandler) ReplyItem(w http.ResponseWriter, r *http.Request) {
	sc, ok := h.writeScope(w, r)
	if !ok {
		return
	}
	var req replyRequest
	if !decodeWrite(w, r, &req) {
		return
	}
	t, err := h.reply.Authorize(r.Context(), sc.actor, sc.hub, sc.item, req.ExpectedTenantID, correlationOf(r))
	if err != nil {
		writeReplyError(w, err)
		return
	}
	res, err := h.reply.Send(r.Context(), sc.actor, t, req.Text, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeReplyError(w, err)
		return
	}
	status := http.StatusAccepted
	if res.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": res.Message.ID, "conversation_id": res.Message.ConversationID, "direction": "outbound", "body": res.Message.Body,
		"status": res.Message.Status, "created_at": res.Message.CreatedAt.UTC().Format(time.RFC3339),
		"tenant": map[string]any{"id": t.Item.TenantID, "name": t.TenantName},
	})
}
