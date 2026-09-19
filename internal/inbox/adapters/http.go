package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type InboxAPIHandler struct{ pool *pgxpool.Pool }

func NewInboxAPIHandler(pool *pgxpool.Pool) *InboxAPIHandler { return &InboxAPIHandler{pool: pool} }

type ConversationItem struct {
	ID                  uuid.UUID  `json:"id"`
	ContactID           uuid.UUID  `json:"contact_id"`
	ChannelConnectionID *uuid.UUID `json:"channel_connection_id,omitempty"`
	Status              string     `json:"status"`
	Title               string     `json:"title"`
	AssignedToUserID    *uuid.UUID `json:"assigned_to_user_id,omitempty"`
	QueueID             *uuid.UUID `json:"queue_id,omitempty"`
	ContactName         string     `json:"contact_name"`
	ContactPhone        string     `json:"contact_phone"`
	TicketStatus        *string    `json:"ticket_status,omitempty"`
	TicketPriority      *string    `json:"ticket_priority,omitempty"`
	CreatedAt           string     `json:"created_at"`
	UpdatedAt           string     `json:"updated_at"`
}

type MessageItem struct {
	ID                  uuid.UUID  `json:"id"`
	ConversationID      uuid.UUID  `json:"conversation_id"`
	ChannelConnectionID *uuid.UUID `json:"channel_connection_id,omitempty"`
	Direction           string     `json:"direction"`
	MessageType         string     `json:"message_type"`
	Body                string     `json:"body,omitempty"`
	MediaRef            string     `json:"media_ref,omitempty"`
	MimeType            string     `json:"mime_type,omitempty"`
	SizeBytes           int64      `json:"size_bytes,omitempty"`
	Status              string     `json:"status"`
	CreatedAt           string     `json:"created_at"`
}

func (h *InboxAPIHandler) ListConversations(w http.ResponseWriter, r *http.Request) {
	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	opts, cursor, err := pageOptions(r)
	if err != nil {
		http.Error(w, "invalid pagination", http.StatusBadRequest)
		return
	}
	args := []any{tenantID, opts.Limit + 1}
	where := "c.tenant_id=$1"
	limitPos := 2
	if cursor != nil {
		where += " AND (c.created_at,c.id) < ($2,$3)"
		args = []any{tenantID, cursor.Timestamp, cursor.ID, opts.Limit + 1}
		limitPos = 4
	}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT c.id,c.contact_id,c.channel_connection_id,c.status,c.title,c.assigned_to_user_id,c.queue_id,
		       co.display_name,co.phone_e164,t.status,t.priority,c.created_at,c.updated_at
		FROM conversations c JOIN contacts co ON co.id=c.contact_id AND co.tenant_id=c.tenant_id
		LEFT JOIN tickets t ON t.conversation_id=c.id AND t.tenant_id=c.tenant_id AND t.status IN ('open','in_progress','waiting')
		WHERE `+where+` ORDER BY c.created_at DESC,c.id DESC LIMIT $`+strconv.Itoa(limitPos), args...)
	if err != nil {
		http.Error(w, "failed to list conversations", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := make([]interface{}, 0, opts.Limit)
	var last ConversationItem
	for rows.Next() && len(items) < opts.Limit {
		last, err = scanConversationItem(rows)
		if err != nil {
			http.Error(w, "failed to read conversations", http.StatusInternalServerError)
			return
		}
		items = append(items, last)
	}
	hasMore := rows.Next()
	result := pagination.NewPageResult(items, opts.Limit, "", hasMore)
	if hasMore {
		result.NextCursor = (&pagination.Cursor{ID: last.ID.String(), Timestamp: parseTime(last.CreatedAt)}).Encode()
	}
	pagination.WritePaginationHeaders(w, result)
	writeJSON(w, result)
}

// GetConversation returns one conversation of the TenantContext tenant. A
// conversation of another tenant and an unknown id are indistinguishable (404).
func (h *InboxAPIHandler) GetConversation(w http.ResponseWriter, r *http.Request) {
	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	item, err := scanConversationItem(platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT c.id,c.contact_id,c.channel_connection_id,c.status,c.title,c.assigned_to_user_id,c.queue_id,
		       co.display_name,co.phone_e164,t.status,t.priority,c.created_at,c.updated_at
		FROM conversations c JOIN contacts co ON co.id=c.contact_id AND co.tenant_id=c.tenant_id
		LEFT JOIN tickets t ON t.conversation_id=c.id AND t.tenant_id=c.tenant_id AND t.status IN ('open','in_progress','waiting')
		WHERE c.tenant_id=$1 AND c.id=$2
		ORDER BY t.created_at DESC NULLS LAST LIMIT 1`, tenantID, conversationID))
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read conversation", http.StatusInternalServerError)
		return
	}
	writeJSON(w, item)
}

func (h *InboxAPIHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	opts, cursor, err := pageOptions(r)
	if err != nil {
		http.Error(w, "invalid pagination", http.StatusBadRequest)
		return
	}
	args := []any{tenantID, conversationID, opts.Limit + 1}
	where := "m.tenant_id=$1 AND m.conversation_id=$2"
	limitPos := 3
	if cursor != nil {
		where += " AND (m.created_at,m.id) < ($3,$4)"
		args = []any{tenantID, conversationID, cursor.Timestamp, cursor.ID, opts.Limit + 1}
		limitPos = 5
	}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT m.id,m.conversation_id,m.channel_connection_id,m.direction,m.message_type,m.body,m.media_ref,m.mime_type,m.size_bytes,m.status,m.created_at
		FROM messages m WHERE `+where+` ORDER BY m.created_at DESC,m.id DESC LIMIT $`+strconv.Itoa(limitPos), args...)
	if err != nil {
		http.Error(w, "failed to list messages", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := make([]interface{}, 0, opts.Limit)
	var last MessageItem
	for rows.Next() && len(items) < opts.Limit {
		last, err = scanMessageItem(rows)
		if err != nil {
			http.Error(w, "failed to read messages", http.StatusInternalServerError)
			return
		}
		items = append(items, last)
	}
	hasMore := rows.Next()
	result := pagination.NewPageResult(items, opts.Limit, "", hasMore)
	if hasMore {
		result.NextCursor = (&pagination.Cursor{ID: last.ID.String(), Timestamp: parseTime(last.CreatedAt)}).Encode()
	}
	pagination.WritePaginationHeaders(w, result)
	writeJSON(w, result)
}

type rowScanner interface{ Scan(...any) error }

func scanConversationItem(row rowScanner) (ConversationItem, error) {
	var item ConversationItem
	var status string
	var ticketStatus, ticketPriority *string
	var created, updated time.Time
	err := row.Scan(&item.ID, &item.ContactID, &item.ChannelConnectionID, &status, &item.Title, &item.AssignedToUserID, &item.QueueID, &item.ContactName, &item.ContactPhone, &ticketStatus, &ticketPriority, &created, &updated)
	item.Status, item.TicketStatus, item.TicketPriority = status, ticketStatus, ticketPriority
	item.CreatedAt, item.UpdatedAt = created.UTC().Format(time.RFC3339Nano), updated.UTC().Format(time.RFC3339Nano)
	return item, err
}

func scanMessageItem(row rowScanner) (MessageItem, error) {
	var item MessageItem
	var direction, status string
	var created time.Time
	err := row.Scan(&item.ID, &item.ConversationID, &item.ChannelConnectionID, &direction, &item.MessageType, &item.Body, &item.MediaRef, &item.MimeType, &item.SizeBytes, &status, &created)
	item.Direction, item.Status, item.CreatedAt = direction, status, created.UTC().Format(time.RFC3339Nano)
	return item, err
}

func requestTenant(r *http.Request) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("tenant context required")
	}
	return tc.TenantID, nil
}

func pageOptions(r *http.Request) (*pagination.PageOptions, *pagination.Cursor, error) {
	opts := pagination.ParsePageOptionsFromQuery(r)
	if opts.Sort != "" && opts.Sort != "created_at:desc" {
		return nil, nil, errors.New("unsupported sort")
	}
	cursor, err := pagination.DecodeCursor(opts.Cursor)
	return opts, cursor, err
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
