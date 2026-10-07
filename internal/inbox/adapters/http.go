package adapters

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	mediaports "github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type InboxAPIHandler struct {
	pool        *pgxpool.Pool
	media       *MediaRetriever
	mediaReader mediaports.MediaReader
}

func NewInboxAPIHandler(pool *pgxpool.Pool) *InboxAPIHandler { return &InboxAPIHandler{pool: pool} }

func (h *InboxAPIHandler) WithMediaRetriever(m *MediaRetriever) *InboxAPIHandler {
	h.media = m
	return h
}

// WithMediaReader serves media from the quarantine-and-scan pipeline (ADR-0016). When set it replaces the
// live fetch from WAHA, which only worked for the few minutes WAHA kept the file.
func (h *InboxAPIHandler) WithMediaReader(r mediaports.MediaReader) *InboxAPIHandler {
	h.mediaReader = r
	return h
}

// displayNameSQL is the name shown for the other side of a conversation: the contact's, or the staff member's for an
// internal conversation (never a made-up value).
const displayNameSQL = "COALESCE(co.display_name, NULLIF(iu.display_name,''), iu.email, '')"

type ConversationItem struct {
	ID uuid.UUID `json:"id"`
	// ContactID is absent for an INTERNAL conversation (a staff member, ADR-0018): staff are never contacts.
	ContactID      *uuid.UUID `json:"contact_id,omitempty"`
	InternalUserID *uuid.UUID `json:"internal_user_id,omitempty"`
	// ConversationKind: internal | customer_service | external_other | unclassified (ADR-0018). There is no "mixed":
	// HasUnclassifiedParticipants flags a conversation that also has someone not classified yet.
	ConversationKind            string     `json:"conversation_kind"`
	HasUnclassifiedParticipants bool       `json:"has_unclassified_participants"`
	ChannelConnectionID         *uuid.UUID `json:"channel_connection_id,omitempty"`
	Status                      string     `json:"status"`
	Title                       string     `json:"title"`
	AssignedToUserID            *uuid.UUID `json:"assigned_to_user_id,omitempty"`
	QueueID                     *uuid.UUID `json:"queue_id,omitempty"`
	ContactName                 string     `json:"contact_name"`
	ContactPhone                string     `json:"contact_phone"`
	// ContactWhatsAppName is the name the person declared on WhatsApp; the UI shows it smaller below ContactName (the
	// principal name: the team\'s alias when there is one) when they differ.
	ContactWhatsAppName string `json:"contact_whatsapp_name,omitempty"`
	// ContactKind is the contact's classification (ADR-0014/0018): unclassified | customer | other | spam; empty for an internal conversation.
	ContactKind    string     `json:"contact_kind"`
	TicketStatus   *string    `json:"ticket_status,omitempty"`
	TicketPriority *string    `json:"ticket_priority,omitempty"`
	CRMContactID   *uuid.UUID `json:"crm_contact_id,omitempty"`
	CreatedAt      string     `json:"created_at"`
	UpdatedAt      string     `json:"updated_at"`
	// MessageCount is filled only by GET /conversations/{id} (one extra count, not
	// worth paying on every row of the list); omitted elsewhere rather than sent as a false 0.
	MessageCount *int `json:"message_count,omitempty"`
	// List-only (GET /inbox/conversations): the last real message, so the Inbox can sort by
	// activity and show a WhatsApp-like preview without a request per row. WaitingSince is
	// when the customer started waiting for an answer (earliest inbound after the last
	// non-failed outbound); omitted when the last message is ours.
	LastMessageAt        *string `json:"last_message_at,omitempty"`
	LastMessageDirection string  `json:"last_message_direction,omitempty"`
	LastMessageType      string  `json:"last_message_type,omitempty"`
	LastMessagePreview   string  `json:"last_message_preview,omitempty"`
	WaitingSince         *string `json:"waiting_since,omitempty"`
}

type MessageItem struct {
	ID                  uuid.UUID  `json:"id"`
	ConversationID      uuid.UUID  `json:"conversation_id"`
	ChannelConnectionID *uuid.UUID `json:"channel_connection_id,omitempty"`
	Direction           string     `json:"direction"`
	MessageType         string     `json:"message_type"`
	Body                string     `json:"body,omitempty"`
	MimeType            string     `json:"mime_type,omitempty"`
	SizeBytes           int64      `json:"size_bytes,omitempty"`
	Status              string     `json:"status"`
	CreatedAt           string     `json:"created_at"`
	// MediaStatus is the state of the attachment pipeline (pending|quarantined|clean|infected|rejected|
	// source_gone|failed); empty for messages without media.
	MediaStatus string `json:"media_status,omitempty"`
	// MediaText is text derived from the attachment (today: the audio transcript), pending|done|empty|failed in
	// MediaTextStatus. It is untrusted data: clients show it as plain text only. MediaTextSuspicious marks text
	// that appears to be addressed to an AI.
	MediaText           string `json:"media_text,omitempty"`
	MediaTextStatus     string `json:"media_text_status,omitempty"`
	MediaTextSuspicious bool   `json:"media_text_suspicious,omitempty"`
	// FailureReason says why a message failed or is uncertain (a short class such as "rejected", or the provider's own
	// code and title for a failure reported after it accepted the message). Never message content.
	FailureReason string `json:"failure_reason,omitempty"`
}

// ListConversations pages the tenant's conversations by LAST ACTIVITY (last message, else
// creation), newest first, with no cap on how far a client can page (cursor on
// (last_activity, id)). Optional filters: q (contact name or phone), assigned=me, waiting=true
// (the last message is from the customer).
func (h *InboxAPIHandler) ListConversations(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	opts, cursor, err := pageOptionsFor(r, "last_activity:desc")
	if err != nil {
		http.Error(w, "invalid pagination", http.StatusBadRequest)
		return
	}
	args := []any{tc.TenantID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := "c.tenant_id=$1"
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		if len(q) > 100 {
			http.Error(w, "invalid search", http.StatusBadRequest)
			return
		}
		like := arg("%" + escapeLike(q) + "%")
		clause := displayNameSQL + " ILIKE " + like + " OR co.whatsapp_name ILIKE " + like
		if digits, ok := phoneDigits(q); ok {
			clause += " OR co.phone_e164 LIKE " + arg("%"+digits+"%")
		}
		where += " AND (" + clause + ")"
	}
	switch r.URL.Query().Get("assigned") {
	case "":
	case "me":
		if tc.ActorID == uuid.Nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		where += " AND c.assigned_to_user_id=" + arg(tc.ActorID)
	default:
		http.Error(w, "invalid assigned filter", http.StatusBadRequest)
		return
	}
	// Spam is kept (nothing is silently dropped) but out of the default view; ask for it explicitly.
	switch kind := r.URL.Query().Get("kind"); kind {
	case "":
		where += " AND (co.kind IS NULL OR co.kind <> 'spam')"
	case "unclassified", "customer", "other", "spam":
		where += " AND co.kind=" + arg(kind)
	default:
		http.Error(w, "invalid kind filter", http.StatusBadRequest)
		return
	}
	// Conversations with staff (internal) are kept but, like spam, out of the default view: they are not attendance.
	// Ask for them (or any other kind) explicitly with conversation_kind.
	switch ck := r.URL.Query().Get("conversation_kind"); ck {
	case "":
		where += " AND c.conversation_kind <> 'internal'"
	case "internal", "customer_service", "external_other", "unclassified":
		where += " AND c.conversation_kind=" + arg(ck)
	default:
		http.Error(w, "invalid conversation_kind filter", http.StatusBadRequest)
		return
	}
	// ADR-0020: finalized attendances are out of the default view (the work queue), like spam and staff conversations; ask for
	// them with status=closed, or for everything with status=all.
	switch st := r.URL.Query().Get("status"); st {
	case "", "open":
		where += " AND c.status='open'"
	case "closed":
		where += " AND c.status='closed'"
	case "all":
	default:
		http.Error(w, "invalid status filter", http.StatusBadRequest)
		return
	}
	if ch := r.URL.Query().Get("channel_connection_id"); ch != "" {
		chID, perr := uuid.Parse(ch)
		if perr != nil {
			http.Error(w, "invalid channel_connection_id filter", http.StatusBadRequest)
			return
		}
		where += " AND c.channel_connection_id=" + arg(chID)
	}
	if r.URL.Query().Get("waiting") == "true" {
		where += " AND lm.direction='inbound'"
	}
	const activity = "COALESCE(lm.created_at,c.created_at)"
	if cursor != nil {
		where += " AND (" + activity + ",c.id) < (" + arg(cursor.Timestamp) + "," + arg(cursor.ID) + ")"
	}
	limit := arg(opts.Limit + 1)
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT c.id,c.contact_id,c.channel_connection_id,c.status,c.title,c.assigned_to_user_id,c.queue_id,
		       `+displayNameSQL+`,coalesce(co.phone_e164,''),t.status,t.priority,c.crm_contact_id,c.created_at,c.updated_at,
		       `+activity+`,lm.created_at,coalesce(lm.direction,''),coalesce(lm.message_type,''),left(coalesce(lm.body,''),160),w.since,coalesce(co.kind,''),
		       c.conversation_kind,c.has_unclassified_participants,c.internal_user_id,coalesce(co.whatsapp_name,'')
		FROM conversations c LEFT JOIN contacts co ON co.id=c.contact_id AND co.tenant_id=c.tenant_id
		LEFT JOIN users iu ON iu.id=c.internal_user_id
		LEFT JOIN LATERAL (SELECT tk.status,tk.priority FROM tickets tk
			WHERE tk.conversation_id=c.id AND tk.tenant_id=c.tenant_id AND tk.status IN ('open','in_progress','waiting')
			ORDER BY tk.topic_scoped ASC, tk.created_at DESC LIMIT 1) t ON true
		LEFT JOIN LATERAL (SELECT m.direction,m.message_type,m.body,m.created_at FROM messages m
			WHERE m.tenant_id=c.tenant_id AND m.conversation_id=c.id
			ORDER BY m.created_at DESC,m.id DESC LIMIT 1) lm ON true
		LEFT JOIN LATERAL (SELECT min(m.created_at) AS since FROM messages m
			WHERE lm.direction='inbound' AND m.tenant_id=c.tenant_id AND m.conversation_id=c.id AND m.direction='inbound'
			  AND m.created_at > COALESCE((SELECT max(o.created_at) FROM messages o
			        WHERE o.tenant_id=c.tenant_id AND o.conversation_id=c.id AND o.direction='outbound' AND o.status<>'failed'),'-infinity'::timestamptz)) w ON true
		WHERE `+where+` ORDER BY `+activity+` DESC,c.id DESC LIMIT `+limit, args...)
	if err != nil {
		http.Error(w, "failed to list conversations", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := make([]interface{}, 0, opts.Limit)
	var last ConversationItem
	var lastActivity time.Time
	// Check the limit BEFORE rows.Next(): calling Next first consumed the lookahead row (LIMIT n+1),
	// so the hasMore probe below always saw nothing and has_more was never true.
	for len(items) < opts.Limit && rows.Next() {
		last, lastActivity, err = scanConversationListItem(rows)
		if err != nil {
			http.Error(w, "failed to read conversations", http.StatusInternalServerError)
			return
		}
		items = append(items, last)
	}
	hasMore := rows.Next()
	result := pagination.NewPageResult(items, opts.Limit, "", hasMore)
	if hasMore {
		result.NextCursor = (&pagination.Cursor{ID: last.ID.String(), Timestamp: lastActivity}).Encode()
	}
	pagination.WritePaginationHeaders(w, result)
	writeJSON(w, result)
}

// escapeLike makes user text literal inside a LIKE/ILIKE pattern.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// phoneDigits returns the digits of q when q LOOKS like a phone number (digits plus the usual
// formatting: space, +, -, ., parentheses) and has at least 3 of them. Text such as "100%" is a
// name search and must not also match phone numbers by its digits.
func phoneDigits(q string) (string, bool) {
	var b strings.Builder
	for _, r := range q {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '+' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return "", false
		}
	}
	return b.String(), b.Len() >= 3
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
		       `+displayNameSQL+`,coalesce(co.phone_e164,''),t.status,t.priority,c.crm_contact_id,c.created_at,c.updated_at,coalesce(co.kind,''),
		       c.conversation_kind,c.has_unclassified_participants,c.internal_user_id,coalesce(co.whatsapp_name,'')
		FROM conversations c LEFT JOIN contacts co ON co.id=c.contact_id AND co.tenant_id=c.tenant_id
		LEFT JOIN users iu ON iu.id=c.internal_user_id
		LEFT JOIN tickets t ON t.conversation_id=c.id AND t.tenant_id=c.tenant_id AND t.status IN ('open','in_progress','waiting')
		WHERE c.tenant_id=$1 AND c.id=$2
		ORDER BY t.topic_scoped ASC NULLS LAST, t.created_at DESC NULLS LAST LIMIT 1`, tenantID, conversationID))
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read conversation", http.StatusInternalServerError)
		return
	}
	var messageCount int
	if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(),
		`SELECT count(*) FROM messages WHERE tenant_id=$1 AND conversation_id=$2`, tenantID, conversationID).Scan(&messageCount); err != nil {
		http.Error(w, "failed to read conversation", http.StatusInternalServerError)
		return
	}
	item.MessageCount = &messageCount
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
		SELECT m.id,m.conversation_id,m.channel_connection_id,m.direction,m.message_type,m.body,m.media_ref,m.mime_type,m.size_bytes,m.status,m.created_at,
		       COALESCE(mm.status,''),
		       CASE WHEN ma.status='done' THEN ma.body ELSE '' END, COALESCE(ma.status,''), COALESCE(ma.suspicious,false),
		       COALESCE(m.failure_reason,'')
		FROM messages m
		LEFT JOIN message_media mm ON mm.tenant_id=m.tenant_id AND mm.message_id=m.id
		LEFT JOIN message_media_analysis ma ON ma.tenant_id=m.tenant_id AND ma.message_id=m.id AND ma.kind='transcript'
		WHERE `+where+` ORDER BY m.created_at DESC,m.id DESC LIMIT $`+strconv.Itoa(limitPos), args...)
	if err != nil {
		http.Error(w, "failed to list messages", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := make([]interface{}, 0, opts.Limit)
	var last MessageItem
	// Check the limit BEFORE rows.Next(): calling Next first consumed the lookahead row (LIMIT n+1),
	// so the hasMore probe below always saw nothing and has_more was never true.
	for len(items) < opts.Limit && rows.Next() {
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
	err := row.Scan(&item.ID, &item.ContactID, &item.ChannelConnectionID, &status, &item.Title, &item.AssignedToUserID, &item.QueueID, &item.ContactName, &item.ContactPhone, &ticketStatus, &ticketPriority, &item.CRMContactID, &created, &updated, &item.ContactKind,
		&item.ConversationKind, &item.HasUnclassifiedParticipants, &item.InternalUserID, &item.ContactWhatsAppName)
	item.Status, item.TicketStatus, item.TicketPriority = status, ticketStatus, ticketPriority
	item.CreatedAt, item.UpdatedAt = created.UTC().Format(time.RFC3339Nano), updated.UTC().Format(time.RFC3339Nano)
	return item, err
}

func scanConversationListItem(row rowScanner) (ConversationItem, time.Time, error) {
	var item ConversationItem
	var status string
	var ticketStatus, ticketPriority *string
	var created, updated, activity time.Time
	var lastAt, waitingSince *time.Time
	err := row.Scan(&item.ID, &item.ContactID, &item.ChannelConnectionID, &status, &item.Title, &item.AssignedToUserID, &item.QueueID, &item.ContactName, &item.ContactPhone, &ticketStatus, &ticketPriority, &item.CRMContactID, &created, &updated,
		&activity, &lastAt, &item.LastMessageDirection, &item.LastMessageType, &item.LastMessagePreview, &waitingSince, &item.ContactKind,
		&item.ConversationKind, &item.HasUnclassifiedParticipants, &item.InternalUserID, &item.ContactWhatsAppName)
	item.Status, item.TicketStatus, item.TicketPriority = status, ticketStatus, ticketPriority
	item.CreatedAt, item.UpdatedAt = created.UTC().Format(time.RFC3339Nano), updated.UTC().Format(time.RFC3339Nano)
	if lastAt != nil {
		v := lastAt.UTC().Format(time.RFC3339Nano)
		item.LastMessageAt = &v
	}
	if waitingSince != nil {
		v := waitingSince.UTC().Format(time.RFC3339Nano)
		item.WaitingSince = &v
	}
	return item, activity, err
}

func scanMessageItem(row rowScanner) (MessageItem, error) {
	var item MessageItem
	var direction, status string
	var created time.Time
	var mediaRef interface{} // discard media_ref from DB row; never expose to public DTO
	err := row.Scan(&item.ID, &item.ConversationID, &item.ChannelConnectionID, &direction, &item.MessageType, &item.Body, &mediaRef, &item.MimeType, &item.SizeBytes, &status, &created, &item.MediaStatus, &item.MediaText, &item.MediaTextStatus, &item.MediaTextSuspicious, &item.FailureReason)
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
	return pageOptionsFor(r, "created_at:desc")
}

// pageOptionsFor accepts only the endpoint's own sort (or none): the cursor's timestamp
// means a different column per endpoint, so a foreign sort must be rejected, not guessed.
func pageOptionsFor(r *http.Request, sort string) (*pagination.PageOptions, *pagination.Cursor, error) {
	opts := pagination.ParsePageOptionsFromQuery(r)
	if opts.Sort != "" && opts.Sort != sort {
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

// GetMedia returns media bytes for a message with security constraints:
// - Message must be readable by tenant (RLS)
// - Origin must match configured trusted WAHA URL exactly
// - Content-Type is sniffed, not trusted
// - Active content (HTML, SVG, etc.) returns 415, bytes not returned
// - Safe inline: raster images only (jpeg, png, webp, gif)
// - Everything else: attachment with safe filename
func (h *InboxAPIHandler) GetMedia(w http.ResponseWriter, r *http.Request) {
	if h.mediaReader != nil {
		h.serveStoredMedia(w, r)
		return
	}
	if h.media == nil {
		http.Error(w, "media retrieval not configured", http.StatusInternalServerError)
		return
	}

	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	messageID, err := uuid.Parse(r.PathValue("message_id"))
	if err != nil {
		http.Error(w, "invalid message_id", http.StatusBadRequest)
		return
	}

	body, mimeType, err := h.media.Retrieve(r.Context(), tenantID, messageID)
	if err != nil {
		// Log the error for observability, but return generic response to client.
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "media not found", http.StatusNotFound)
		} else if strings.Contains(err.Error(), "not allowed") || strings.Contains(err.Error(), "type not allowed") {
			http.Error(w, "media type not allowed", http.StatusUnsupportedMediaType)
		} else if strings.Contains(err.Error(), "too large") {
			http.Error(w, "media too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "failed to retrieve media", http.StatusInternalServerError)
		}
		return
	}

	// Set security headers.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")

	// Determine if inline or attachment.
	isInline := isInlineImage(mimeType)
	if isInline {
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("Content-Disposition", "inline")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		filename := sanitizeFilename("media_" + messageID.String() + extensionForMime(mimeType))
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	}

	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(body)), 10))
	_, _ = w.Write(body)
}

func isInlineImage(mimeType string) bool {
	normalized := strings.ToLower(strings.Split(mimeType, ";")[0])
	switch normalized {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	}
	return false
}

func extensionForMime(mimeType string) string {
	normalized := strings.ToLower(strings.Split(mimeType, ";")[0])
	switch normalized {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "audio/ogg":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "audio/wav":
		return ".wav"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	default:
		return ".bin"
	}
}

func sanitizeFilename(name string) string {
	// Remove path separators and null bytes.
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "\x00", "")
	// Remove control characters.
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return '_'
		}
		return r
	}, name)
}

// serveStoredMedia answers with a file the antivirus cleared, and with an explicit status otherwise, so the UI
// can say what is going on instead of showing a broken image. Nothing but the clean area is ever opened.
func (h *InboxAPIHandler) serveStoredMedia(w http.ResponseWriter, r *http.Request) {
	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	messageID, err := uuid.Parse(r.PathValue("message_id"))
	if err != nil {
		http.Error(w, "invalid message_id", http.StatusBadRequest)
		return
	}
	media, err := h.mediaReader.Open(r.Context(), tenantID, messageID)
	if errors.Is(err, mediaports.ErrMediaNotFound) {
		http.Error(w, "media not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to retrieve media", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Media-Status", media.Status)
	if media.File == nil {
		switch media.Status {
		case "pending", "quarantined":
			http.Error(w, "media is being checked", http.StatusConflict)
		case "infected", "rejected", "failed":
			http.Error(w, "media blocked for security reasons", http.StatusForbidden)
		default: // source_gone, purged
			http.Error(w, "media no longer available", http.StatusGone)
		}
		return
	}
	defer media.File.Close()

	// Even cleared files are treated as hostile in the browser: no scripts, no framing, no sniffing.
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if isInlineSafeMime(media.Mime) {
		w.Header().Set("Content-Type", media.Mime)
		w.Header().Set("Content-Disposition", "inline")
		// A cleared file is immutable, so images and audio may sit in the operator's own browser cache for a
		// day (no re-download on every reopen) and are revalidated by ETag after that. `private` keeps shared
		// caches out. Video and documents stay no-store.
		if isCacheableMime(media.Mime) && media.SHA256 != "" {
			w.Header().Set("Cache-Control", "private, max-age=86400")
			w.Header().Set("ETag", `"`+media.SHA256+`"`)
		}
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		name := sanitizeFilename("media_" + messageID.String() + extensionForMime(media.Mime))
		w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	}
	// ServeContent adds Range support, which the audio player needs to seek.
	http.ServeContent(w, r, "", time.Time{}, io.ReadSeeker(media.File))
}

// isCacheableMime: images and audio only.
func isCacheableMime(mime string) bool {
	m := strings.ToLower(strings.Split(mime, ";")[0])
	return strings.HasPrefix(m, "image/") || strings.HasPrefix(m, "audio/")
}

// isInlineSafeMime lists what the browser may render itself: raster images and audio/video that passed the
// allow-list. Documents (PDF, text) are always downloads.
func isInlineSafeMime(mime string) bool {
	switch strings.ToLower(strings.Split(mime, ";")[0]) {
	case "image/jpeg", "image/png", "image/webp", "image/gif",
		"audio/ogg", "audio/mpeg", "audio/mp4", "audio/wav", "audio/flac", "audio/amr",
		"video/mp4", "video/webm", "video/quicktime":
		return true
	}
	return false
}
