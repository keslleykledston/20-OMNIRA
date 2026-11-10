package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Directory lists the groups of ONE of the tenant's WhatsApp connections, so an administrator can choose
// which ones to read. A tenant may run more than one WhatsApp instance (several numbers/instâncias) at
// once; their groups are never merged into a single pool. connectionID uuid.Nil asks the directory to pick
// the tenant's single active connection (ErrAmbiguousConnection if there is more than one); a non-nil id
// asks for exactly that connection (ErrNoConnection if it is not an active connection of THIS tenant).
type Directory interface {
	List(ctx context.Context, tenantID, connectionID uuid.UUID) (resolvedConnectionID uuid.UUID, groups []channeldomain.ProviderGroup, err error)
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

// groupPurposes is the closed list an administrator picks from (migration 000176). Never guessed from
// participants — that is conversation_kind's job; purpose is always an explicit choice.
var groupPurposes = map[string]bool{
	"client_management":   true,
	"client_support":      true,
	"internal_management": true,
	"internal_team":       true,
	"other":               true,
}

// Handler serves the group APIs. Authorization is the role->permission matrix of an ACTIVE
// membership (never a role name) on top of RLS, which scopes every row to the tenant.
type Handler struct {
	pool  *pgxpool.Pool
	audit auditports.AuditEventRepository
	dir   Directory

	mu    sync.Mutex
	cache map[directoryCacheKey]directoryEntry
	now   func() time.Time
}

// directoryCacheKey separates the cache by WHICH connection was asked for: two WhatsApp instances of the
// same tenant have different group lists and must never share a cache entry.
type directoryCacheKey struct {
	tenant uuid.UUID
	hint   uuid.UUID
}

type directoryEntry struct {
	at     time.Time
	conn   uuid.UUID
	groups []channeldomain.ProviderGroup
}

func NewHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository, dir Directory) *Handler {
	return &Handler{pool: pool, audit: audit, dir: dir, cache: map[directoryCacheKey]directoryEntry{}, now: time.Now}
}

// ---- shapes

type lastMessage struct {
	AuthorName  string `json:"author_name"`
	FromMe      bool   `json:"from_me"`
	MessageType string `json:"message_type"`
	Preview     string `json:"preview"`
}

type groupItem struct {
	ID                uuid.UUID    `json:"id"`
	Name              string       `json:"name"`
	Enabled           bool         `json:"enabled"`
	Purpose           string       `json:"purpose"`
	AccountID         *uuid.UUID   `json:"account_id"`
	AccountName       *string      `json:"account_name"`
	ConnectionID      uuid.UUID    `json:"connection_id"`
	ConnectionAccount string       `json:"connection_account"`
	AIObserverEnabled bool         `json:"ai_observer_enabled"`
	LastMessageAt     *time.Time   `json:"last_message_at"`
	LastMessage       *lastMessage `json:"last_message"`
}

// suggestionItem is the AI observer's suggestion for one message (ADR-0055) — never anything acted on, just
// shown to whoever already has group.read so a human can review it.
type suggestionItem struct {
	Category        string  `json:"category"`
	Summary         string  `json:"summary"`
	SuggestedAction string  `json:"suggested_action"`
	Confidence      float64 `json:"confidence"`
}

type availableItem struct {
	ProviderGroupID  string     `json:"provider_group_id"`
	Name             string     `json:"name"`
	ParticipantCount int        `json:"participant_count"`
	Enabled          bool       `json:"enabled"`
	GroupID          *uuid.UUID `json:"group_id"`
}

type messageItem struct {
	ID          uuid.UUID       `json:"id"`
	AuthorName  string          `json:"author_name"`
	FromMe      bool            `json:"from_me"`
	MessageType string          `json:"message_type"`
	Body        string          `json:"body"`
	SentAt      time.Time       `json:"sent_at"`
	Suggestion  *suggestionItem `json:"suggestion"`
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

// directory returns one connection's WhatsApp groups, cached for a minute per (tenant, hint): the
// provider's answer lists every participant of every group and is heavy. hint is uuid.Nil to let the
// directory pick the tenant's single active connection, or a specific connection id to disambiguate when
// the tenant runs more than one WhatsApp instance at once.
func (h *Handler) directory(ctx context.Context, tenantID, hint uuid.UUID) (uuid.UUID, []channeldomain.ProviderGroup, error) {
	key := directoryCacheKey{tenant: tenantID, hint: hint}
	h.mu.Lock()
	if e, ok := h.cache[key]; ok && h.now().Sub(e.at) < directoryTTL {
		h.mu.Unlock()
		return e.conn, e.groups, nil
	}
	h.mu.Unlock()
	if h.dir == nil {
		return uuid.Nil, nil, ErrNoConnection
	}
	conn, groups, err := h.dir.List(ctx, tenantID, hint)
	if err != nil {
		return uuid.Nil, nil, err
	}
	h.mu.Lock()
	h.cache[key] = directoryEntry{at: h.now(), conn: conn, groups: groups}
	h.mu.Unlock()
	return conn, groups, nil
}

// connectionHint reads an optional connection_id query/body value, used to pick which WhatsApp instance's
// groups to work with when a tenant runs more than one at once. Empty means "let the directory decide"
// (uuid.Nil); an invalid id is reported to the caller rather than silently ignored.
func connectionHint(raw string) (uuid.UUID, bool) {
	if raw == "" {
		return uuid.Nil, true
	}
	id, err := uuid.Parse(raw)
	return id, err == nil
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
	SELECT g.id, g.name, g.enabled, g.purpose, g.account_id, ca.name, g.channel_connection_id, cc.external_account_id,
	       g.ai_observer_enabled, g.last_message_at, lm.author_name, lm.from_me, lm.message_type, left(lm.body, 160)
	FROM wa_groups g
	LEFT JOIN customer_accounts ca ON ca.tenant_id = g.tenant_id AND ca.id = g.account_id
	LEFT JOIN channel_connections cc ON cc.tenant_id = g.tenant_id AND cc.id = g.channel_connection_id
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
	if err := row.Scan(&it.ID, &it.Name, &it.Enabled, &it.Purpose, &it.AccountID, &it.AccountName, &it.ConnectionID, &it.ConnectionAccount,
		&it.AIObserverEnabled, &it.LastMessageAt, &author, &fromMe, &mtype, &preview); err != nil {
		return it, err
	}
	if author != nil && fromMe != nil && mtype != nil && preview != nil {
		it.LastMessage = &lastMessage{AuthorName: *author, FromMe: *fromMe, MessageType: *mtype, Preview: *preview}
	}
	return it, nil
}

// ---- handlers

// List returns the groups an administrator enabled, most recently active first. account_id=<uuid> narrows
// to one client's groups; account_id=none finds the ones nobody has linked to a client yet. connection_id
// narrows to one WhatsApp instance, for a tenant running more than one at once — their groups are never
// merged into a single pool.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permRead)
	if !ok {
		return
	}
	args := []any{tc.TenantID}
	where := "g.tenant_id = $1 AND g.enabled"
	if v := r.URL.Query().Get("account_id"); v != "" {
		if v == "none" {
			where += " AND g.account_id IS NULL"
		} else if id, err := uuid.Parse(v); err == nil {
			args = append(args, id)
			where += fmt.Sprintf(" AND g.account_id = $%d", len(args))
		} else {
			http.Error(w, "invalid account_id", http.StatusBadRequest)
			return
		}
	}
	if v := r.URL.Query().Get("connection_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			http.Error(w, "invalid connection_id", http.StatusBadRequest)
			return
		}
		args = append(args, id)
		where += fmt.Sprintf(" AND g.channel_connection_id = $%d", len(args))
	}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(),
		groupSelect+` WHERE `+where+`
		ORDER BY g.last_message_at DESC NULLS LAST, lower(g.name), g.id LIMIT `+strconv.Itoa(enabledListCap), args...)
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

// Available lists ONE WhatsApp instance's groups (name search, capped) with whether each is enabled.
// connection_id picks which instance when the tenant has more than one active; omitted, it works as before
// (the tenant's single active connection, or 409 if there is more than one and no connection_id was given).
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
	hint, ok := connectionHint(r.URL.Query().Get("connection_id"))
	if !ok {
		http.Error(w, "invalid connection_id", http.StatusBadRequest)
		return
	}
	conn, groups, err := h.directory(r.Context(), tc.TenantID, hint)
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
	writeJSON(w, http.StatusOK, map[string]any{"items": matches, "count": len(matches), "total": total, "connection_id": conn})
}

// Enable starts reading a group. The group must exist in the tenant's own WhatsApp account, which
// also gives its name; enabling an already known group keeps its history. connection_id picks which
// instance when the tenant has more than one active WhatsApp connection (same rule as Available).
func (h *Handler) Enable(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	var req struct {
		ProviderGroupID *string `json:"provider_group_id"`
		ConnectionID    *string `json:"connection_id"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.ProviderGroupID == nil || !providerGroupPattern.MatchString(*req.ProviderGroupID) {
		http.Error(w, "provider_group_id must be a WhatsApp group id", http.StatusBadRequest)
		return
	}
	hint := uuid.Nil
	if req.ConnectionID != nil {
		id, err := uuid.Parse(*req.ConnectionID)
		if err != nil {
			http.Error(w, "connection_id must be a valid id", http.StatusBadRequest)
			return
		}
		hint = id
	}
	conn, groups, err := h.directory(r.Context(), tc.TenantID, hint)
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

// SetEnabled turns reading on or off for a known group, changes its manual purpose tag, and/or turns the AI
// observer (ADR-0055) on or off — any subset of the three fields at once. Turning reading off stops storing
// new messages; the history stays (deleting it is a separate, explicit action). purpose is never guessed: an
// administrator picks it from the closed list. The AI observer can never be turned on for a group whose
// reading ends up off in the same request — there would be nothing left to observe.
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
		Enabled           *bool   `json:"enabled"`
		Purpose           *string `json:"purpose"`
		AIObserverEnabled *bool   `json:"ai_observer_enabled"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || (req.Enabled == nil && req.Purpose == nil && req.AIObserverEnabled == nil) {
		http.Error(w, "enabled, purpose and/or ai_observer_enabled must be set", http.StatusBadRequest)
		return
	}
	if req.Purpose != nil && !groupPurposes[*req.Purpose] {
		http.Error(w, "invalid purpose", http.StatusBadRequest)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var prevEnabled, prevAIObserver bool
	var prevPurpose string
	err := q.QueryRow(r.Context(), `SELECT enabled, purpose, ai_observer_enabled FROM wa_groups WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tc.TenantID, id).
		Scan(&prevEnabled, &prevPurpose, &prevAIObserver)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "group not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	newEnabled, newPurpose, newAIObserver := prevEnabled, prevPurpose, prevAIObserver
	if req.Enabled != nil {
		newEnabled = *req.Enabled
	}
	if req.Purpose != nil {
		newPurpose = *req.Purpose
	}
	if req.AIObserverEnabled != nil {
		newAIObserver = *req.AIObserverEnabled
	}
	if newAIObserver && !newEnabled {
		http.Error(w, "ai_observer_enabled requires the group to be reading (enabled)", http.StatusBadRequest)
		return
	}
	enabledChanged, purposeChanged, aiObserverChanged := newEnabled != prevEnabled, newPurpose != prevPurpose, newAIObserver != prevAIObserver
	if enabledChanged || purposeChanged || aiObserverChanged {
		if _, err := q.Exec(r.Context(), `
			UPDATE wa_groups SET enabled = $3, purpose = $4, ai_observer_enabled = $8, updated_at = now(),
			  enabled_by = CASE WHEN $3 AND NOT $5 THEN $6::uuid ELSE enabled_by END,
			  enabled_at = CASE WHEN $3 AND NOT $5 THEN now() ELSE enabled_at END,
			  purpose_set_by = CASE WHEN $7 THEN $6::uuid ELSE purpose_set_by END,
			  purpose_set_at = CASE WHEN $7 THEN now() ELSE purpose_set_at END,
			  ai_observer_set_by = CASE WHEN $9 THEN $6::uuid ELSE ai_observer_set_by END,
			  ai_observer_set_at = CASE WHEN $9 THEN now() ELSE ai_observer_set_at END
			WHERE tenant_id = $1 AND id = $2`,
			tc.TenantID, id, newEnabled, newPurpose, prevEnabled, tc.ActorID, purposeChanged, newAIObserver, aiObserverChanged); err != nil {
			http.Error(w, "failed to save group", http.StatusInternalServerError)
			return
		}
		if enabledChanged {
			action := auditdomain.ActionGroupDisabled
			if newEnabled {
				action = auditdomain.ActionGroupEnabled
			}
			h.record(r, tc, id, action, map[string]any{"created": false})
		}
		if purposeChanged {
			h.record(r, tc, id, auditdomain.ActionGroupPurposeSet, map[string]any{"from": prevPurpose, "to": newPurpose})
		}
		if aiObserverChanged {
			action := auditdomain.ActionGroupAIObserverDisabled
			if newAIObserver {
				action = auditdomain.ActionGroupAIObserverEnabled
			}
			h.record(r, tc, id, action, map[string]any{})
		}
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
		SELECT m.id, COALESCE(NULLIF(ct.alias, ''), m.author_name), m.from_me, m.message_type, m.body, m.sent_at,
		       s.category, s.summary, s.suggested_action, s.confidence
		FROM wa_group_messages m
		LEFT JOIN channel_participants cp ON cp.tenant_id = m.tenant_id AND cp.id = m.sender_channel_participant_id
		LEFT JOIN contacts ct ON ct.tenant_id = cp.tenant_id AND ct.id = cp.contact_id
		LEFT JOIN wa_group_message_suggestions s ON s.tenant_id = m.tenant_id AND s.group_message_id = m.id
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
		var category, summary, suggestedAction *string
		var confidence *float64
		last.Suggestion = nil
		if err := rows.Scan(&last.ID, &last.AuthorName, &last.FromMe, &last.MessageType, &last.Body, &last.SentAt, &category, &summary, &suggestedAction, &confidence); err != nil {
			http.Error(w, "failed to read messages", http.StatusInternalServerError)
			return
		}
		if category != nil {
			action := ""
			if suggestedAction != nil {
				action = *suggestedAction
			}
			last.Suggestion = &suggestionItem{Category: *category, Summary: *summary, SuggestedAction: action, Confidence: *confidence}
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

// LinkAccount sets which "cliente final" (customer_accounts, ADR-0018) a group belongs to, reusing the
// company registry that already exists instead of letting a group carry its own copy of the name. The
// account must exist in the SAME tenant; a group may belong to at most one account at a time (linking again
// replaces the previous one). Idempotent: linking the account already set changes nothing and is not re-audited.
func (h *Handler) LinkAccount(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	var req struct {
		AccountID *string `json:"account_id"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	accountID, err := func() (uuid.UUID, error) {
		if err := dec.Decode(&req); err != nil || req.AccountID == nil {
			return uuid.Nil, errors.New("account_id is required")
		}
		return uuid.Parse(*req.AccountID)
	}()
	if err != nil {
		http.Error(w, "account_id must be a valid id", http.StatusBadRequest)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var prev *uuid.UUID
	if err := q.QueryRow(r.Context(), `SELECT account_id FROM wa_groups WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tc.TenantID, id).Scan(&prev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "group not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	var accountExists bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM customer_accounts WHERE tenant_id = $1 AND id = $2)`, tc.TenantID, accountID).Scan(&accountExists); err != nil {
		http.Error(w, "failed to read account", http.StatusInternalServerError)
		return
	}
	if !accountExists {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	if prev == nil || *prev != accountID {
		if _, err := q.Exec(r.Context(), `
			UPDATE wa_groups SET account_id = $3, account_set_by = $4, account_set_at = now(), updated_at = now()
			WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id, accountID, tc.ActorID); err != nil {
			http.Error(w, "failed to save group", http.StatusInternalServerError)
			return
		}
		h.record(r, tc, id, auditdomain.ActionGroupAccountLinked, map[string]any{"account_id": accountID})
	}
	it, err := scanGroup(q.QueryRow(r.Context(), groupSelect+` WHERE g.tenant_id = $1 AND g.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// UnlinkAccount removes a group's "cliente final" link, leaving it unlinked (found by List's
// account_id=none filter) until someone links it again. Idempotent; a no-op is not audited.
func (h *Handler) UnlinkAccount(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.session(w, r, permManage)
	if !ok {
		return
	}
	id, ok := groupID(w, r)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var prev *uuid.UUID
	if err := q.QueryRow(r.Context(), `SELECT account_id FROM wa_groups WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tc.TenantID, id).Scan(&prev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "group not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	if prev != nil {
		if _, err := q.Exec(r.Context(), `
			UPDATE wa_groups SET account_id = NULL, updated_at = now() WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id); err != nil {
			http.Error(w, "failed to save group", http.StatusInternalServerError)
			return
		}
		h.record(r, tc, id, auditdomain.ActionGroupAccountUnlinked, map[string]any{"account_id": *prev})
	}
	it, err := scanGroup(q.QueryRow(r.Context(), groupSelect+` WHERE g.tenant_id = $1 AND g.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read group", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, it)
}
