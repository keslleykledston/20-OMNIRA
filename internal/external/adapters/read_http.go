package adapters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/external/domain"
	mediaports "github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
)

// ReadHandler serves the read side of the external API (ADR-0048 H3). Every handler runs in a system tenant session whose tenant is the one of the
// authenticated installation, builds its WHERE clause from Visibility (one rule for all), and answers 404 — the same document — for anything the
// installation may not see: a missing id, another company's id, a staff conversation, an internal or spam contact, a queue outside its allow-list.
type ReadHandler struct {
	pool  *pgxpool.Pool
	media mediaports.MediaReader
}

func NewReadHandler(pool *pgxpool.Pool, media mediaports.MediaReader) *ReadHandler {
	return &ReadHandler{pool: pool, media: media}
}

const (
	defaultPageSize = 50
	maxPageSize     = 200
	// MaxServedMediaBytes bounds what the media endpoint streams to an application (the ingest pipeline already limits what is stored).
	MaxServedMediaBytes = 64 << 20
)

var errNotVisible = errors.New("not visible")

func problemNotFound(w http.ResponseWriter, r *http.Request) {
	externalMetric.Inc("not_found")
	WriteProblem(w, r, http.StatusNotFound, "not_found", "Not found", "The resource does not exist or is not visible to this installation.")
}

func problemBad(w http.ResponseWriter, r *http.Request, detail string) {
	externalMetric.Inc("bad_request")
	WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "Invalid request", detail)
}

func failRead(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInstallationInactive):
		externalMetric.Inc("unauthenticated")
		WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", "Send Authorization: Bearer <integration token>.")
	case errors.Is(err, errNotVisible), errors.Is(err, pgx.ErrNoRows):
		problemNotFound(w, r)
	default:
		externalMetric.Inc("error")
		w.Header().Set("Retry-After", "5")
		WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Temporarily unavailable", "Try again shortly.")
	}
}

// scoped runs fn in the installation's tenant with its allow-lists loaded. The result of fn is written by the caller AFTER the transaction ends.
func (h *ReadHandler) scoped(r *http.Request, p *domain.Principal, fn func(ctx context.Context, q platformdb.Querier, v Visibility) error) error {
	return scopedRun(h.pool, r, p, fn)
}

func scopedRun(pool *pgxpool.Pool, r *http.Request, p *domain.Principal, fn func(ctx context.Context, q platformdb.Querier, v Visibility) error) error {
	return platformdb.WithSystemTenantSession(r.Context(), pool, p.TenantID, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, pool)
		al, err := LoadAllowlists(ctx, q, p)
		if err != nil {
			return err
		}
		return fn(ctx, q, NewVisibility(p, al))
	})
}

func pageSize(r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultPageSize, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxPageSize {
		return 0, false
	}
	return n, true
}

func cursorOf(r *http.Request) (*pagination.Cursor, bool) {
	c, err := pagination.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		return nil, false
	}
	if c != nil {
		if _, err := uuid.Parse(c.ID); err != nil {
			return nil, false
		}
	}
	return c, true
}

func uuidParam(r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	return id, err == nil
}

// ---------------------------------------------------------------------------------------------------------------------------------------------

const conversationSelect = `
	SELECT c.id, c.status, c.contact_id, c.channel_connection_id, COALESCE(cc.provider, ''), c.queue_id,
	       c.control_kind, c.control_holder_id, c.control_version, c.control_origin, c.control_since,
	       (SELECT max(m.created_at) FROM messages m WHERE m.tenant_id = c.tenant_id AND m.conversation_id = c.id),
	       c.created_at, c.updated_at
	  FROM conversations c
	  LEFT JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id`

func scanConversation(row pgx.Row, p *domain.Principal) (ConversationDTO, error) {
	var d ConversationDTO
	var kind, origin *string
	var holder *uuid.UUID
	err := row.Scan(&d.ID, &d.Status, &d.ContactID, &d.Channel.ConnectionID, &d.Channel.Provider, &d.QueueID,
		&kind, &holder, &d.Control.Version, &origin, &d.Control.Since, &d.LastMessageAt, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return d, err
	}
	d.Control.State = "unassigned"
	if kind != nil {
		d.Control.State = "held"
		d.Control.Origin = origin
		mine := *kind == "crm_application" && holder != nil && *holder == p.InstallationID
		d.Control.Holder = &HolderDTO{Kind: *kind, Mine: mine}
	}
	d.Can = can(p, d.Status, d.Control)
	return d, nil
}

// ListConversations: GET /external/conversations?status=open|closed|all&control=free|held|mine&channel_connection_id=&queue_id=&sort=created|updated&limit=&cursor=
func (h *ReadHandler) ListConversations(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	q := r.URL.Query()
	limit, ok := pageSize(r)
	cur, ok2 := cursorOf(r)
	if !ok || !ok2 {
		problemBad(w, r, "limit must be 1 to 200 and cursor must be one returned by this API.")
		return
	}
	status := q.Get("status")
	if status == "" {
		status = "open"
	}
	if status != "open" && status != "closed" && status != "all" {
		problemBad(w, r, "status must be open, closed or all.")
		return
	}
	sortCol := "c.created_at"
	switch q.Get("sort") {
	case "", "created":
	case "updated":
		sortCol = "c.updated_at"
	default:
		problemBad(w, r, "sort must be created or updated.")
		return
	}
	var channel, queue *uuid.UUID
	for name, dst := range map[string]**uuid.UUID{"channel_connection_id": &channel, "queue_id": &queue} {
		if raw := q.Get(name); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				problemBad(w, r, name+" must be a UUID.")
				return
			}
			*dst = &id
		}
	}
	control := q.Get("control")
	if control != "" && control != "free" && control != "held" && control != "mine" {
		problemBad(w, r, "control must be free, held or mine.")
		return
	}

	var out PageDTO[ConversationDTO]
	err := h.scoped(r, p, func(ctx context.Context, db platformdb.Querier, v Visibility) error {
		var args Args
		where := v.Conversation("c", &args)
		switch status {
		case "open", "closed":
			where += " AND c.status = " + args.Add(status)
		}
		if channel != nil {
			where += " AND c.channel_connection_id = " + args.Add(*channel)
		}
		if queue != nil {
			where += " AND c.queue_id = " + args.Add(*queue)
		}
		switch control {
		case "free":
			where += " AND c.control_kind IS NULL"
		case "held":
			where += " AND c.control_kind IS NOT NULL"
		case "mine":
			where += " AND c.control_kind = 'crm_application' AND c.control_holder_id = " + args.Add(p.InstallationID)
		}
		if cur != nil {
			id, _ := uuid.Parse(cur.ID)
			where += " AND (" + sortCol + ", c.id) < (" + args.Add(cur.Timestamp.UTC()) + ", " + args.Add(id) + ")"
		}
		rows, err := db.Query(ctx, conversationSelect+" WHERE "+where+" ORDER BY "+sortCol+" DESC, c.id DESC LIMIT "+args.Add(limit+1), args.Values()...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanConversation(rows, p)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, d)
		}
		return rows.Err()
	})
	if err != nil {
		failRead(w, r, err)
		return
	}
	if out.Items == nil {
		out.Items = []ConversationDTO{}
	}
	if len(out.Items) > limit {
		out.Items, out.HasMore = out.Items[:limit], true
		last := out.Items[len(out.Items)-1]
		ts := last.CreatedAt
		if sortCol == "c.updated_at" {
			ts = last.UpdatedAt
		}
		out.NextCursor = (&pagination.Cursor{ID: last.ID.String(), Timestamp: ts}).Encode()
	}
	externalMetric.Inc("served")
	WriteJSON(w, r, http.StatusOK, out)
}

// GetConversation: GET /external/conversations/{id}. The ETag is the control version ("control-N"), the value a later command sends back in If-Match.
func (h *ReadHandler) GetConversation(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	id, ok := uuidParam(r, "id")
	if !ok {
		problemNotFound(w, r)
		return
	}
	var d ConversationDTO
	err := h.scoped(r, p, func(ctx context.Context, db platformdb.Querier, v Visibility) error {
		var args Args
		where := v.Conversation("c", &args) + " AND c.id = " + args.Add(id)
		var err error
		d, err = scanConversation(db.QueryRow(ctx, conversationSelect+" WHERE "+where, args.Values()...), p)
		return err
	})
	if err != nil {
		failRead(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"control-%d"`, d.Control.Version))
	externalMetric.Inc("served")
	WriteJSON(w, r, http.StatusOK, d)
}

// ListMessages: GET /external/conversations/{id}/messages?order=asc|desc&limit=&cursor= (stable keyset on created_at, id).
func (h *ReadHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	id, ok := uuidParam(r, "id")
	if !ok {
		problemNotFound(w, r)
		return
	}
	limit, ok := pageSize(r)
	cur, ok2 := cursorOf(r)
	if !ok || !ok2 {
		problemBad(w, r, "limit must be 1 to 200 and cursor must be one returned by this API.")
		return
	}
	order, cmp := "ASC", ">"
	switch r.URL.Query().Get("order") {
	case "", "asc":
	case "desc":
		order, cmp = "DESC", "<"
	default:
		problemBad(w, r, "order must be asc or desc.")
		return
	}
	var out PageDTO[MessageDTO]
	err := h.scoped(r, p, func(ctx context.Context, db platformdb.Querier, v Visibility) error {
		// the conversation must be visible: the whole page is a 404 otherwise
		var args Args
		vis := v.Conversation("c", &args)
		var exists bool
		if err := db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM conversations c WHERE "+vis+" AND c.id = "+args.Add(id)+")", args.Values()...).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errNotVisible
		}
		var args2 Args
		vis2 := v.Conversation("c", &args2)
		where2 := vis2 + " AND m.tenant_id = c.tenant_id AND m.conversation_id = c.id AND c.id = " + args2.Add(id)
		if cur != nil {
			cid, _ := uuid.Parse(cur.ID)
			where2 += " AND (m.created_at, m.id) " + cmp + " (" + args2.Add(cur.Timestamp.UTC()) + ", " + args2.Add(cid) + ")"
		}
		rows, err := db.Query(ctx, `
			SELECT m.id, m.direction, m.message_type, COALESCE(m.body, ''), m.status, m.sent_by_user_id IS NOT NULL, m.sent_by_installation_id, m.created_at,
			       COALESCE(mm.id, om.id), COALESCE(mm.kind, om.kind, ''), COALESCE(mm.mime, om.mime, ''), COALESCE(mm.size_bytes, om.size_bytes, 0),
			       CASE WHEN mm.id IS NOT NULL THEN mm.status WHEN om.id IS NOT NULL THEN CASE WHEN om.file_purged_at IS NULL THEN 'clean' ELSE 'source_gone' END END
			  FROM messages m
			  JOIN conversations c ON c.tenant_id = m.tenant_id AND c.id = m.conversation_id
			  LEFT JOIN message_media mm ON mm.tenant_id = m.tenant_id AND mm.message_id = m.id
			  LEFT JOIN message_outbound_media om ON om.tenant_id = m.tenant_id AND om.message_id = m.id
			 WHERE `+where2+` ORDER BY m.created_at `+order+`, m.id `+order+` LIMIT `+args2.Add(limit+1), args2.Values()...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m MessageDTO
			var byUser bool
			var byApp *uuid.UUID
			var mediaID *uuid.UUID
			var mKind, mMime string
			var mSize int64
			var mStatus *string
			if err := rows.Scan(&m.ID, &m.Direction, &m.Type, &m.Body, &m.Status, &byUser, &byApp, &m.CreatedAt, &mediaID, &mKind, &mMime, &mSize, &mStatus); err != nil {
				return err
			}
			switch {
			case m.Direction == "inbound":
				m.Sender = SenderDTO{Kind: "contact"}
			case byApp != nil:
				m.Sender = SenderDTO{Kind: "crm_application", Mine: *byApp == p.InstallationID}
			case byUser:
				m.Sender = SenderDTO{Kind: "agent"}
			default:
				m.Sender = SenderDTO{Kind: "automation"}
			}
			if mediaID != nil && mStatus != nil {
				m.Media = &MediaDTO{ID: *mediaID, Kind: mKind, Mime: mMime, SizeBytes: mSize, Status: *mStatus}
			}
			out.Items = append(out.Items, m)
		}
		return rows.Err()
	})
	if err != nil {
		failRead(w, r, err)
		return
	}
	if out.Items == nil {
		out.Items = []MessageDTO{}
	}
	if len(out.Items) > limit {
		out.Items, out.HasMore = out.Items[:limit], true
		last := out.Items[len(out.Items)-1]
		out.NextCursor = (&pagination.Cursor{ID: last.ID.String(), Timestamp: last.CreatedAt}).Encode()
	}
	externalMetric.Inc("served")
	WriteJSON(w, r, http.StatusOK, out)
}

// GetContact: GET /external/contacts/{id}. A contact is visible only through a conversation the installation may see (so the allow-lists apply to it as
// well), and never when it is classified internal or spam.
func (h *ReadHandler) GetContact(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	id, ok := uuidParam(r, "id")
	if !ok {
		problemNotFound(w, r)
		return
	}
	var d ContactDTO
	err := h.scoped(r, p, func(ctx context.Context, db platformdb.Querier, v Visibility) error {
		var args Args
		vis := v.Conversation("c", &args)
		return db.QueryRow(ctx, `
			SELECT ct.id, COALESCE(NULLIF(ct.alias, ''), ct.display_name, ''), COALESCE(ct.whatsapp_name, ''), COALESCE(ct.phone_e164, ''), ct.kind, ct.created_at, ct.updated_at
			  FROM contacts ct
			 WHERE ct.tenant_id = $1 AND ct.id = `+args.Add(id)+` AND ct.kind NOT IN ('internal', 'spam')
			   AND EXISTS (SELECT 1 FROM conversations c WHERE c.contact_id = ct.id AND `+vis+`)`, args.Values()...).
			Scan(&d.ID, &d.Name, &d.WhatsAppName, &d.Phone, &d.Kind, &d.CreatedAt, &d.UpdatedAt)
	})
	if err != nil {
		failRead(w, r, err)
		return
	}
	externalMetric.Inc("served")
	WriteJSON(w, r, http.StatusOK, d)
}

// GetMedia: GET /external/media/{id}. {id} is the media id the message listing returned. Authorization is asked at THIS request (the message's
// conversation must be visible now), only cleared files are opened (the pipeline of ADR-0016), the provider's URL is never involved, and the file
// is served with the hostile-content headers of the workspace.
func (h *ReadHandler) GetMedia(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	id, ok := uuidParam(r, "id")
	if !ok || h.media == nil {
		problemNotFound(w, r)
		return
	}
	var served *mediaports.ServedMedia
	var messageID uuid.UUID
	err := h.scoped(r, p, func(ctx context.Context, db platformdb.Querier, v Visibility) error {
		var args Args
		vis := v.Conversation("c", &args)
		if err := db.QueryRow(ctx, `
			SELECT x.message_id FROM (
			    SELECT tenant_id, message_id FROM message_media WHERE tenant_id = $1 AND id = `+args.Add(id)+`
			    UNION ALL
			    SELECT tenant_id, message_id FROM message_outbound_media WHERE tenant_id = $1 AND id = `+args.Add(id)+` AND message_id IS NOT NULL
			) x
			JOIN messages m ON m.tenant_id = x.tenant_id AND m.id = x.message_id
			JOIN conversations c ON c.tenant_id = m.tenant_id AND c.id = m.conversation_id
			WHERE `+vis+` LIMIT 1`, args.Values()...).Scan(&messageID); err != nil {
			return err
		}
		var err error
		served, err = h.media.Open(ctx, p.TenantID, messageID)
		if errors.Is(err, mediaports.ErrMediaNotFound) {
			return errNotVisible
		}
		if err == nil && served.ID != id { // the reader opens by message; authorization was for THIS media id (a message can carry a row in each media table)
			if served.File != nil {
				served.File.Close()
			}
			served = nil
			return errNotVisible
		}
		return err
	})
	if err != nil {
		if served != nil && served.File != nil {
			served.File.Close()
		}
		failRead(w, r, err)
		return
	}
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("X-Media-Status", served.Status)
	if id := RequestIDFrom(r.Context()); id != "" {
		hd.Set("X-Request-ID", id)
	}
	if served.File == nil {
		switch served.Status {
		case "pending", "quarantined":
			WriteProblem(w, r, http.StatusConflict, "media_pending", "Media is being checked", "Try again shortly.")
		case "infected", "rejected", "failed":
			WriteProblem(w, r, http.StatusForbidden, "media_blocked", "Media blocked", "The file was blocked for security reasons.")
		default:
			WriteProblem(w, r, http.StatusGone, "media_gone", "Media no longer available", "")
		}
		return
	}
	defer served.File.Close()
	if served.Size > MaxServedMediaBytes {
		WriteProblem(w, r, http.StatusRequestEntityTooLarge, "media_too_large", "Media too large", "")
		return
	}
	hd.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	hd.Set("Cross-Origin-Resource-Policy", "same-origin")
	if isInlineSafeMime(served.Mime) {
		hd.Set("Content-Type", strings.ToLower(strings.Split(served.Mime, ";")[0]))
		hd.Set("Content-Disposition", "inline")
	} else {
		hd.Set("Content-Type", "application/octet-stream")
		hd.Set("Content-Disposition", `attachment; filename="media_`+id.String()+`.bin"`)
	}
	externalMetric.Inc("served")
	http.ServeContent(w, r, "", time.Time{}, io.ReadSeeker(served.File))
}

func isInlineSafeMime(mime string) bool {
	switch strings.ToLower(strings.Split(mime, ";")[0]) {
	case "image/jpeg", "image/png", "image/webp", "image/gif",
		"audio/ogg", "audio/mpeg", "audio/mp4", "audio/wav", "audio/flac", "audio/amr",
		"video/mp4", "video/webm", "video/quicktime":
		return true
	}
	return false
}

// QueueDTO is a queue (a sector, ADR-0043) the application can name when it transfers a conversation. Id and name only: members, hours and rules stay inside.
type QueueDTO struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// ListQueues: GET /external/queues. The company's queues, narrowed to the installation's queue allowlist when the administrator set one (the same narrowing the
// conversation reads and the transfer apply), so the application can map its own team labels to queue ids without being told about queues it may not use.
func (h *ReadHandler) ListQueues(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	items := []QueueDTO{}
	err := h.scoped(r, p, func(ctx context.Context, db platformdb.Querier, v Visibility) error {
		var args Args
		tenant := args.Add(v.TenantID)
		narrow := ""
		if len(v.QueueIDs) > 0 {
			narrow = " AND q.id = ANY(" + args.Add(v.QueueIDs) + "::uuid[])"
		}
		rows, err := db.Query(ctx, `SELECT q.id, q.name FROM queues q WHERE q.tenant_id = `+tenant+narrow+` ORDER BY lower(q.name), q.id`, args.Values()...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d QueueDTO
			if err := rows.Scan(&d.ID, &d.Name); err != nil {
				return err
			}
			items = append(items, d)
		}
		return rows.Err()
	})
	if err != nil {
		failRead(w, r, err)
		return
	}
	externalMetric.Inc("served")
	WriteJSON(w, r, http.StatusOK, map[string]any{"items": items})
}
