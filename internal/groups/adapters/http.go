package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Directory lists the groups of the tenant's WhatsApp account, so an administrator can choose which
// ones to read. It also names the connection the groups belong to.
type Directory interface {
	List(ctx context.Context, tenantID uuid.UUID) (connectionID uuid.UUID, groups []channeldomain.ProviderGroup, err error)
}

// Errors a Directory may return; the handler maps them to HTTP statuses.
var (
	ErrNoConnection        = errors.New("groups: no active WhatsApp connection")
	ErrAmbiguousConnection = errors.New("groups: more than one active WhatsApp connection")
)

var providerGroupPattern = regexp.MustCompile(`^[0-9]{5,20}(-[0-9]{1,20})?@g\.us$`)

const (
	permRead   = "group.read"
	permManage = "group.manage"

	directoryTTL     = 60 * time.Second
	availableMax     = 200
	availableDefault = 50
	enabledListCap   = 500
)

// Handler serves the group APIs. Authorization is the role->permission matrix of an ACTIVE
// membership (never a role name) on top of RLS, which scopes every row to the tenant.
type Handler struct {
	pool  *pgxpool.Pool
	audit auditports.AuditEventRepository
	dir   Directory

	mu    sync.Mutex
	cache map[uuid.UUID]directoryEntry
	now   func() time.Time
}

type directoryEntry struct {
	at     time.Time
	conn   uuid.UUID
	groups []channeldomain.ProviderGroup
}

func NewHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository, dir Directory) *Handler {
	return &Handler{pool: pool, audit: audit, dir: dir, cache: map[uuid.UUID]directoryEntry{}, now: time.Now}
}

// ---- shapes

type lastMessage struct {
	AuthorName  string `json:"author_name"`
	FromMe      bool   `json:"from_me"`
	MessageType string `json:"message_type"`
	Preview     string `json:"preview"`
}

type groupItem struct {
	ID            uuid.UUID    `json:"id"`
	Name          string       `json:"name"`
	Enabled       bool         `json:"enabled"`
	LastMessageAt *time.Time   `json:"last_message_at"`
	LastMessage   *lastMessage `json:"last_message"`
}

type availableItem struct {
	ProviderGroupID  string     `json:"provider_group_id"`
	Name             string     `json:"name"`
	ParticipantCount int        `json:"participant_count"`
	Enabled          bool       `json:"enabled"`
	GroupID          *uuid.UUID `json:"group_id"`
}

type messageItem struct {
	ID          uuid.UUID `json:"id"`
	AuthorName  string    `json:"author_name"`
	FromMe      bool      `json:"from_me"`
	MessageType string    `json:"message_type"`
	Body        string    `json:"body"`
	SentAt      time.Time `json:"sent_at"`
}

// ---- helpers

func (h *Handler) session(w http.ResponseWriter, r *http.Request, permission string) (*tenancydomain.TenantContext, bool) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	var ok bool
	if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tc.TenantID, tc.ActorID, permission).Scan(&ok); err != nil {
		http.Error(w, "failed to check permission", http.StatusInternalServerError)
		return nil, false
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return tc, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func groupID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("group_id"))
	if err != nil {
		http.Error(w, "invalid group_id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) record(r *http.Request, tc *tenancydomain.TenantContext, id uuid.UUID, action auditdomain.AuditAction, meta map[string]any) {
	if h.audit == nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, auditdomain.ResourceGroup, id, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = h.audit.Store(r.Context(), ev)
}

// directory returns the WhatsApp account's groups, cached per tenant for a minute: the provider's
// answer lists every participant of every group and is heavy.
func (h *Handler) directory(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, []channeldomain.ProviderGroup, error) {
	h.mu.Lock()
	if e, ok := h.cache[tenantID]; ok && h.now().Sub(e.at) < directoryTTL {
		h.mu.Unlock()
		return e.conn, e.groups, nil
	}
	h.mu.Unlock()
	if h.dir == nil {
		return uuid.Nil, nil, ErrNoConnection
	}
	conn, groups, err := h.dir.List(ctx, tenantID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	h.mu.Lock()
	h.cache[tenantID] = directoryEntry{at: h.now(), conn: conn, groups: groups}
	h.mu.Unlock()
	return conn, groups, nil
}

func directoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoConnection):
		http.Error(w, "no active WhatsApp connection", http.StatusConflict)
	case errors.Is(err, ErrAmbiguousConnection):
		http.Error(w, "more than one active WhatsApp connection", http.StatusConflict)
	default:
		http.Error(w, "WhatsApp is unavailable", http.StatusBadGateway)
	}
}

const groupSelect = `
	SELECT g.id, g.name, g.enabled, g.last_message_at, lm.author_name, lm.from_me, lm.message_type, left(lm.body, 160)
	FROM wa_groups g
	LEFT JOIN LATERAL (
	  -- the author shows under the team's alias when the participant is a known contact
	  SELECT COALESCE(NULLIF(ct.alias, ''), m.author_name) AS author_name, m.from_me, m.message_type, m.body FROM wa_group_messages m
	  LEFT JOIN channel_participants cp ON cp.tenant_id = m.tenant_id AND cp.id = m.sender_channel_participant_id
	  LEFT JOIN contacts ct ON ct.tenant_id = cp.tenant_id AND ct.id = cp.contact_id
	  WHERE m.tenant_id = g.tenant_id AND m.group_id = g.id
	  ORDER BY m.sent_at DESC, m.id DESC LIMIT 1) lm ON true`

func scanGroup(row pgx.Row) (groupItem, error) {
	var it groupItem
	var author, mtype, preview *string
	var fromMe *bool
	if err := row.Scan(&it.ID, &it.Name, &it.Enabled, &it.LastMessageAt, &author, &fromMe, &mtype, &preview); err != nil {
		return it, err
	}
	if author != nil && fromMe != nil && mtype != nil && preview != nil {
		it.LastMessage = &lastMessage{AuthorName: *author, FromMe: *fromMe, MessageType: *mtype, Preview: *preview}
	}
	return it, nil
}

// ---- handlers

// List returns the groups an administrator enabled, most recently active first.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permRead)
	if !ok {
		return
	}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(),
		groupSelect+` WHERE g.tenant_id = $1 AND g.enabled
		ORDER BY g.last_message_at DESC NULLS LAST, lower(g.name), g.id LIMIT `+strconv.Itoa(enabledListCap), tc.TenantID)
	if err != nil {
		http.Error(w, "failed to list groups", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []groupItem{}
	for rows.Next() {
		it, err := scanGroup(rows)
		if err != nil {
			http.Error(w, "failed to read groups", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
	}
	if rows.Err() != nil {
		http.Error(w, "failed to read groups", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// Available lists the WhatsApp account's groups (name search, capped) with whether each is enabled.
func (h *Handler) Available(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	limit := availableDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = min(n, availableMax)
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if len(q) > 100 {
		http.Error(w, "invalid search", http.StatusBadRequest)
		return
	}
	conn, groups, err := h.directory(r.Context(), tc.TenantID)
	if err != nil {
		directoryError(w, err)
		return
	}
	stored := map[string]struct {
		id      uuid.UUID
		enabled bool
	}{}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(),
		`SELECT provider_group_id, id, enabled FROM wa_groups WHERE tenant_id = $1 AND channel_connection_id = $2`, tc.TenantID, conn)
	if err != nil {
		http.Error(w, "failed to list groups", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var pid string
		var s struct {
			id      uuid.UUID
			enabled bool
		}
		if err := rows.Scan(&pid, &s.id, &s.enabled); err != nil {
			http.Error(w, "failed to read groups", http.StatusInternalServerError)
			return
		}
		stored[pid] = s
	}
	matches := make([]availableItem, 0, len(groups))
	for _, g := range groups {
		if !providerGroupPattern.MatchString(g.JID) || (q != "" && !strings.Contains(strings.ToLower(g.Name), q)) {
			continue
		}
		it := availableItem{ProviderGroupID: g.JID, Name: g.Name, ParticipantCount: g.ParticipantCount}
		if s, ok := stored[g.JID]; ok {
			id := s.id
			it.Enabled, it.GroupID = s.enabled, &id
		}
		matches = append(matches, it)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Enabled != matches[j].Enabled {
			return matches[i].Enabled
		}
		return strings.ToLower(matches[i].Name) < strings.ToLower(matches[j].Name)
	})
	total := len(matches)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": matches, "count": len(matches), "total": total})
}

// Enable starts reading a group. The group must exist in the tenant's own WhatsApp account, which
// also gives its name; enabling an already known group keeps its history.
func (h *Handler) Enable(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	var req struct {
		ProviderGroupID *string `json:"provider_group_id"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.ProviderGroupID == nil || !providerGroupPattern.MatchString(*req.ProviderGroupID) {
		http.Error(w, "provider_group_id must be a WhatsApp group id", http.StatusBadRequest)
		return
	}
	conn, groups, err := h.directory(r.Context(), tc.TenantID)
	if err != nil {
		directoryError(w, err)
		return
	}
	var name string
	found := false
	for _, g := range groups {
		if g.JID == *req.ProviderGroupID {
			name, found = g.Name, true
			break
		}
	}
	if !found {
		http.Error(w, "group not found in this WhatsApp account", http.StatusNotFound)
		return
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[:200])
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var id uuid.UUID
	var created bool
	err = q.QueryRow(r.Context(), `
		INSERT INTO wa_groups (tenant_id, channel_connection_id, provider_group_id, name, enabled, enabled_by, enabled_at)
		VALUES ($1,$2,$3,$4,true,$5,now())
		ON CONFLICT (tenant_id, channel_connection_id, provider_group_id)
		DO UPDATE SET enabled = true, name = EXCLUDED.name, enabled_by = EXCLUDED.enabled_by, enabled_at = now(), updated_at = now()
		RETURNING id, (xmax = 0)`, tc.TenantID, conn, *req.ProviderGroupID, name, tc.ActorID).Scan(&id, &created)
	if err != nil {
		http.Error(w, "failed to enable group", http.StatusInternalServerError)
		return
	}
	h.record(r, tc, id, auditdomain.ActionGroupEnabled, map[string]any{"created": created})
	it, err := scanGroup(q.QueryRow(r.Context(), groupSelect+` WHERE g.tenant_id = $1 AND g.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, it)
}

// SetEnabled turns reading on or off for a known group. Off stops storing new messages; the history
// stays (deleting it is a separate, explicit action).
func (h *Handler) SetEnabled(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Enabled == nil {
		http.Error(w, "enabled must be true or false", http.StatusBadRequest)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var prev bool
	err := q.QueryRow(r.Context(), `SELECT enabled FROM wa_groups WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tc.TenantID, id).Scan(&prev)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "group not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if prev != *req.Enabled {
		if _, err := q.Exec(r.Context(), `
			UPDATE wa_groups SET enabled = $3, updated_at = now(),
			  enabled_by = CASE WHEN $3 THEN $4::uuid ELSE enabled_by END,
			  enabled_at = CASE WHEN $3 THEN now() ELSE enabled_at END
			WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id, *req.Enabled, tc.ActorID); err != nil {
			http.Error(w, "failed to save group", http.StatusInternalServerError)
			return
		}
		action := auditdomain.ActionGroupDisabled
		if *req.Enabled {
			action = auditdomain.ActionGroupEnabled
		}
		h.record(r, tc, id, action, map[string]any{"created": false})
	}
	it, err := scanGroup(q.QueryRow(r.Context(), groupSelect+` WHERE g.tenant_id = $1 AND g.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// ListMessages pages a group's stored messages, newest first (the client shows the newest last).
// A disabled group's history is still readable until someone deletes it.
func (h *Handler) ListMessages(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permRead)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	opts := pagination.ParsePageOptionsFromQuery(r)
	if opts.Sort != "" && opts.Sort != "sent_at:desc" {
		http.Error(w, "unsupported sort", http.StatusBadRequest)
		return
	}
	cursor, err := pagination.DecodeCursor(opts.Cursor)
	if err != nil {
		http.Error(w, "invalid pagination", http.StatusBadRequest)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var exists bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM wa_groups WHERE tenant_id = $1 AND id = $2)`, tc.TenantID, id).Scan(&exists); err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "group not found", http.StatusNotFound)
		return
	}
	args := []any{tc.TenantID, id}
	where := "m.tenant_id = $1 AND m.group_id = $2"
	if cursor != nil {
		cursorID, perr := uuid.Parse(cursor.ID)
		if perr != nil {
			http.Error(w, "invalid pagination", http.StatusBadRequest)
			return
		}
		args = append(args, cursor.Timestamp, cursorID)
		where += " AND (m.sent_at, m.id) < ($3, $4)"
	}
	args = append(args, opts.Limit+1)
	rows, err := q.Query(r.Context(), `
		SELECT m.id, COALESCE(NULLIF(ct.alias, ''), m.author_name), m.from_me, m.message_type, m.body, m.sent_at FROM wa_group_messages m
		LEFT JOIN channel_participants cp ON cp.tenant_id = m.tenant_id AND cp.id = m.sender_channel_participant_id
		LEFT JOIN contacts ct ON ct.tenant_id = cp.tenant_id AND ct.id = cp.contact_id
		WHERE `+where+` ORDER BY m.sent_at DESC, m.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		http.Error(w, "failed to list messages", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := make([]interface{}, 0, opts.Limit)
	var last messageItem
	// Check the limit BEFORE rows.Next(): the extra row (LIMIT n+1) must stay for the hasMore probe.
	for len(items) < opts.Limit && rows.Next() {
		if err := rows.Scan(&last.ID, &last.AuthorName, &last.FromMe, &last.MessageType, &last.Body, &last.SentAt); err != nil {
			http.Error(w, "failed to read messages", http.StatusInternalServerError)
			return
		}
		items = append(items, last)
	}
	hasMore := rows.Next()
	result := pagination.NewPageResult(items, opts.Limit, "", hasMore)
	if hasMore {
		result.NextCursor = (&pagination.Cursor{ID: last.ID.String(), Timestamp: last.SentAt}).Encode()
	}
	pagination.WritePaginationHeaders(w, result)
	writeJSON(w, http.StatusOK, result)
}

// DeleteHistory removes every stored message of a group (an explicit, audited action, separate from
// disabling) and requests the removal of its archived files. The group keeps its enabled state. Backups
// already taken still contain the messages until they expire.
func (h *Handler) DeleteHistory(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var exists bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM wa_groups WHERE tenant_id = $1 AND id = $2)`, tc.TenantID, id).Scan(&exists); err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "group not found", http.StatusNotFound)
		return
	}
	tag, err := q.Exec(r.Context(), `DELETE FROM wa_group_messages WHERE tenant_id = $1 AND group_id = $2`, tc.TenantID, id)
	if err != nil {
		http.Error(w, "failed to delete history", http.StatusInternalServerError)
		return
	}
	// Also ask the archive job (it alone can reach the external disk) to remove this group's cold files.
	if _, err := q.Exec(r.Context(), `UPDATE wa_groups SET last_message_at = NULL, archive_purge_requested_at = now(), updated_at = now() WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id); err != nil {
		http.Error(w, "failed to delete history", http.StatusInternalServerError)
		return
	}
	h.record(r, tc, id, auditdomain.ActionGroupHistoryDeleted, map[string]any{"deleted": tag.RowsAffected()})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": tag.RowsAffected()})
}
