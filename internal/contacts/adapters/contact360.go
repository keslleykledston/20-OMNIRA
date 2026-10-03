package adapters

import (
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketdomain "github.com/omnira/omnira/internal/tickets/domain"
)

// CONTACT.360-A: read-only read model behind the Contact 360 screen. Only
// facts that already exist in the database are served; nothing is stored,
// invented or editable here. Both routes resolve the contact inside the
// TenantContext tenant first, so a contact of another tenant and an unknown id
// are indistinguishable (404).

const bodyPreviewMaxRunes = 140

// ContactLastMessage is a short preview of the newest message of a
// conversation. body_preview is truncated; a media message with no caption has
// an empty preview and the screen shows its message_type instead.
type ContactLastMessage struct {
	Direction   string    `json:"direction"`
	MessageType string    `json:"message_type"`
	BodyPreview string    `json:"body_preview"`
	CreatedAt   time.Time `json:"created_at"`
}

// ContactConversationItem never carries tenant_id (the session already
// establishes it). channel/provider are null for a conversation that has no
// channel connection.
type ContactConversationItem struct {
	ID               uuid.UUID           `json:"id"`
	Status           string              `json:"status"`
	Title            string              `json:"title"`
	Channel          *string             `json:"channel"`
	Provider         *string             `json:"provider"`
	AssignedToUserID *uuid.UUID          `json:"assigned_to_user_id"`
	MessageCount     int                 `json:"message_count"`
	LastMessage      *ContactLastMessage `json:"last_message"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

// ContactTicketItem is the subset of the canonical ticket the 360 screen
// needs. Same canonical rows as GET /tickets, narrowed to one contact.
type ContactTicketItem struct {
	ID                  uuid.UUID  `json:"id"`
	ConversationID      uuid.UUID  `json:"conversation_id"`
	Subject             string     `json:"subject"`
	Status              string     `json:"status"`
	Priority            string     `json:"priority"`
	AssignedTo          *uuid.UUID `json:"assigned_to"`
	Provider            *string    `json:"provider"`
	ExternalTicketID    *string    `json:"external_ticket_id"`
	ExternalStatusLabel *string    `json:"external_status_label"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

var errTicketPermissionDenied = errors.New("contacts: ticket.read required")

// authorizeTicketRead mirrors internal/tickets/adapters.Handler.authorizeTicketRead
// (the project mirrors this check per module rather than sharing it): the
// permission comes from the role→permission matrix of an ACTIVE membership,
// never from a role-name comparison.
func (h *ContactsAPIHandler) authorizeTicketRead(r *http.Request, tc *tenancydomain.TenantContext) error {
	var ok bool
	err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key='ticket.read')`,
		tc.TenantID, tc.ActorID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errTicketPermissionDenied
	}
	return nil
}

// contactInScope validates contact_id and proves the contact belongs to the
// TenantContext tenant. On any failure it has already written the response.
func (h *ContactsAPIHandler) contactInScope(w http.ResponseWriter, r *http.Request) (tenantID, contactID uuid.UUID, ok bool) {
	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return uuid.Nil, uuid.Nil, false
	}
	contactID, err = uuid.Parse(r.PathValue("contact_id"))
	if err != nil {
		http.Error(w, "invalid contact_id", http.StatusBadRequest)
		return uuid.Nil, uuid.Nil, false
	}
	var exists bool
	if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM contacts WHERE tenant_id = $1 AND id = $2)`, tenantID, contactID).Scan(&exists); err != nil {
		http.Error(w, "failed to read contact", http.StatusInternalServerError)
		return uuid.Nil, uuid.Nil, false
	}
	if !exists {
		http.Error(w, "contact not found", http.StatusNotFound)
		return uuid.Nil, uuid.Nil, false
	}
	return tenantID, contactID, true
}

type subresourcePage struct {
	limit    int
	cursorTS *time.Time
	cursorID *uuid.UUID
}

// parseSubresourcePage reads limit/cursor/sort the same way ListContacts does:
// newest activity first, updated_at:desc is the only sort.
func parseSubresourcePage(w http.ResponseWriter, r *http.Request) (subresourcePage, bool) {
	opts := pagination.ParsePageOptionsFromQuery(r)
	if opts.Sort != "" && opts.Sort != "updated_at:desc" {
		http.Error(w, "unsupported sort", http.StatusBadRequest)
		return subresourcePage{}, false
	}
	cursor, err := pagination.DecodeCursor(opts.Cursor)
	if err != nil {
		http.Error(w, "invalid pagination", http.StatusBadRequest)
		return subresourcePage{}, false
	}
	page := subresourcePage{limit: opts.Limit}
	if cursor != nil {
		id, parseErr := uuid.Parse(cursor.ID)
		if parseErr != nil {
			http.Error(w, "invalid pagination", http.StatusBadRequest)
			return subresourcePage{}, false
		}
		ts := cursor.Timestamp
		page.cursorTS, page.cursorID = &ts, &id
	}
	return page, true
}

func writeSubresourcePage(w http.ResponseWriter, items []any, limit int, hasMore bool, lastID uuid.UUID, lastUpdated time.Time) {
	result := &pagination.PageResult{Items: items, HasMore: hasMore, Count: len(items), Limit: limit}
	if hasMore && len(items) > 0 {
		result.NextCursor = (&pagination.Cursor{ID: lastID.String(), Timestamp: lastUpdated}).Encode()
	}
	pagination.WritePaginationHeaders(w, result)
	writeJSON(w, result)
}

func previewOf(body string) string {
	if utf8.RuneCountInString(body) <= bodyPreviewMaxRunes {
		return body
	}
	runes := []rune(body)
	return string(runes[:bodyPreviewMaxRunes]) + "…"
}

// ListContactConversations returns the contact's conversations, newest
// activity first, each with its channel, message count and last message
// preview. Any active member of the tenant may read it, the same visibility
// the Inbox already gives them.
func (h *ContactsAPIHandler) ListContactConversations(w http.ResponseWriter, r *http.Request) {
	tenantID, contactID, ok := h.contactInScope(w, r)
	if !ok {
		return
	}
	page, ok := parseSubresourcePage(w, r)
	if !ok {
		return
	}

	query := `SELECT cv.id, cv.status, COALESCE(cv.title, ''), cc.channel, cc.provider, cv.assigned_to_user_id,
		       (SELECT count(*) FROM messages mc WHERE mc.tenant_id = cv.tenant_id AND mc.conversation_id = cv.id),
		       lm.direction, lm.message_type, lm.body, lm.created_at, cv.created_at, cv.updated_at
		FROM conversations cv
		LEFT JOIN channel_connections cc ON cc.id = cv.channel_connection_id AND cc.tenant_id = cv.tenant_id
		LEFT JOIN LATERAL (
		    SELECT m.direction, m.message_type, m.body, m.created_at FROM messages m
		     WHERE m.tenant_id = cv.tenant_id AND m.conversation_id = cv.id
		     ORDER BY m.created_at DESC, m.id DESC LIMIT 1) lm ON true
		WHERE cv.tenant_id = $1 AND cv.contact_id = $2`
	args := []any{tenantID, contactID}
	if page.cursorTS != nil {
		query += ` AND (cv.updated_at, cv.id) < ($3, $4)`
		args = append(args, *page.cursorTS, *page.cursorID)
	}
	query += ` ORDER BY cv.updated_at DESC, cv.id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, page.limit+1)

	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "failed to list conversations", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]ContactConversationItem, 0, page.limit)
	for rows.Next() {
		var (
			it                  ContactConversationItem
			lmDir, lmType, lmBy *string
			lmAt                *time.Time
		)
		if err := rows.Scan(&it.ID, &it.Status, &it.Title, &it.Channel, &it.Provider, &it.AssignedToUserID,
			&it.MessageCount, &lmDir, &lmType, &lmBy, &lmAt, &it.CreatedAt, &it.UpdatedAt); err != nil {
			http.Error(w, "failed to read conversations", http.StatusInternalServerError)
			return
		}
		if lmAt != nil && lmDir != nil && lmType != nil {
			body := ""
			if lmBy != nil {
				body = *lmBy
			}
			it.LastMessage = &ContactLastMessage{Direction: *lmDir, MessageType: *lmType, BodyPreview: previewOf(body), CreatedAt: *lmAt}
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read conversations", http.StatusInternalServerError)
		return
	}

	hasMore := len(items) > page.limit
	if hasMore {
		items = items[:page.limit]
	}
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = it
	}
	var lastID uuid.UUID
	var lastUpdated time.Time
	if len(items) > 0 {
		lastID, lastUpdated = items[len(items)-1].ID, items[len(items)-1].UpdatedAt
	}
	writeSubresourcePage(w, out, page.limit, hasMore, lastID, lastUpdated)
}

// ListContactTickets returns the tickets of the contact's conversations,
// newest activity first. Like GET /tickets it is gated on ticket.read: the
// permission is checked before the contact is resolved, so a caller without it
// learns nothing about whether an id exists.
func (h *ContactsAPIHandler) ListContactTickets(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	if err := h.authorizeTicketRead(r, tc); err != nil {
		if errors.Is(err, errTicketPermissionDenied) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "failed to authorize", http.StatusInternalServerError)
		return
	}
	tenantID, contactID, ok := h.contactInScope(w, r)
	if !ok {
		return
	}
	page, ok := parseSubresourcePage(w, r)
	if !ok {
		return
	}

	query := `SELECT t.id, t.conversation_id, t.subject, t.status, t.priority, t.assigned_to,
		       t.provider, t.external_ticket_id, t.external_status_label, t.created_at, t.updated_at
		FROM tickets t
		JOIN conversations cv ON cv.id = t.conversation_id AND cv.tenant_id = t.tenant_id
		WHERE t.tenant_id = $1 AND cv.contact_id = $2 AND ` + ticketdomain.RealTicketSQL("t")
	args := []any{tenantID, contactID}
	if page.cursorTS != nil {
		query += ` AND (t.updated_at, t.id) < ($3, $4)`
		args = append(args, *page.cursorTS, *page.cursorID)
	}
	query += ` ORDER BY t.updated_at DESC, t.id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, page.limit+1)

	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "failed to list tickets", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]ContactTicketItem, 0, page.limit)
	for rows.Next() {
		var it ContactTicketItem
		if err := rows.Scan(&it.ID, &it.ConversationID, &it.Subject, &it.Status, &it.Priority, &it.AssignedTo,
			&it.Provider, &it.ExternalTicketID, &it.ExternalStatusLabel, &it.CreatedAt, &it.UpdatedAt); err != nil {
			http.Error(w, "failed to read tickets", http.StatusInternalServerError)
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read tickets", http.StatusInternalServerError)
		return
	}

	hasMore := len(items) > page.limit
	if hasMore {
		items = items[:page.limit]
	}
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = it
	}
	var lastID uuid.UUID
	var lastUpdated time.Time
	if len(items) > 0 {
		lastID, lastUpdated = items[len(items)-1].ID, items[len(items)-1].UpdatedAt
	}
	writeSubresourcePage(w, out, page.limit, hasMore, lastID, lastUpdated)
}
