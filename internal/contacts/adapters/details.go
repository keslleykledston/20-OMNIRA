package adapters

import (
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Editing a contact (name, e-mail) and the notes that document its context. The phone is the contact's identity and is
// never edited here. Authority is the permission matrix (contact.classify, held by whoever attends); the tenant comes from
// the session. Note TEXT is never written to the audit trail, only the fact that it changed.

const maxNoteRunes = 4000

func (h *ContactsAPIHandler) editor(w http.ResponseWriter, r *http.Request) (*tenancydomain.TenantContext, uuid.UUID, bool) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, uuid.Nil, false
	}
	ok, err := h.hasPermission(r, tc, permClassify)
	if err != nil {
		http.Error(w, "failed to check permission", http.StatusInternalServerError)
		return nil, uuid.Nil, false
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, uuid.Nil, false
	}
	id, err := uuid.Parse(r.PathValue("contact_id"))
	if err != nil {
		http.Error(w, "invalid contact_id", http.StatusBadRequest)
		return nil, uuid.Nil, false
	}
	return tc, id, true
}

func (h *ContactsAPIHandler) auditContact(r *http.Request, tc *tenancydomain.TenantContext, action auditdomain.AuditAction, id uuid.UUID, meta map[string]any) {
	if h.audit == nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, auditdomain.ResourceContact, id, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = h.audit.Store(r.Context(), ev)
}

type updateDetailsRequest struct {
	// Alias is the name the team gives the contact. An empty (or blank) alias CLEARS it and the name declared on WhatsApp
	// is shown again on its own.
	Alias *string `json:"alias"`
	Email *string `json:"email"`
}

// UpdateDetails: PUT /tenants/{tenant_id}/contacts/{contact_id}/details — name and/or e-mail.
func (h *ContactsAPIHandler) UpdateDetails(w http.ResponseWriter, r *http.Request) {
	tc, id, ok := h.editor(w, r)
	if !ok {
		return
	}
	var req updateDetailsRequest
	if !decodeStrict(w, r, &req) || (req.Alias == nil && req.Email == nil) {
		if req.Alias == nil && req.Email == nil {
			http.Error(w, "nothing to change", http.StatusBadRequest)
		}
		return
	}
	var alias, email *string
	if req.Alias != nil {
		n := strings.TrimSpace(*req.Alias)
		if utf8.RuneCountInString(n) > 200 {
			http.Error(w, "invalid alias", http.StatusUnprocessableEntity)
			return
		}
		alias = &n
	}
	if req.Email != nil {
		e := strings.TrimSpace(*req.Email)
		if e != "" {
			if len(e) > 254 {
				http.Error(w, "invalid email", http.StatusUnprocessableEntity)
				return
			}
			if a, err := mail.ParseAddress(e); err != nil || a.Address != e {
				http.Error(w, "invalid email", http.StatusUnprocessableEntity)
				return
			}
		}
		email = &e
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	tag, err := q.Exec(r.Context(), `
		UPDATE contacts SET alias = CASE WHEN $5::boolean THEN NULLIF($3, '') ELSE alias END, email = COALESCE($4, email), updated_at = now()
		WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id, alias, email, alias != nil)
	if err != nil {
		http.Error(w, "failed to save contact", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "contact not found", http.StatusNotFound)
		return
	}
	h.auditContact(r, tc, auditdomain.ActionContactUpdated, id, map[string]any{"alias_changed": alias != nil, "email_changed": email != nil})
	item, err := scanContactItem(q.QueryRow(r.Context(), contactSelect+` WHERE c.tenant_id = $1 AND c.id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "failed to read contact", http.StatusInternalServerError)
		return
	}
	writeJSON(w, item)
}

type noteDTO struct {
	ID        uuid.UUID  `json:"id"`
	Body      string     `json:"body"`
	AuthorID  *uuid.UUID `json:"author_user_id"`
	Author    string     `json:"author_name"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Mine: the caller wrote it (only the author edits or removes a note).
	Mine bool `json:"mine"`
}

const noteSelect = `SELECT n.id, n.body, n.created_by_user_id, COALESCE(NULLIF(u.display_name,''), u.email, ''), n.created_at, n.updated_at
	FROM contact_notes n LEFT JOIN users u ON u.id = n.created_by_user_id`

func (h *ContactsAPIHandler) scanNote(row pgx.Row, actor uuid.UUID) (noteDTO, error) {
	var n noteDTO
	err := row.Scan(&n.ID, &n.Body, &n.AuthorID, &n.Author, &n.CreatedAt, &n.UpdatedAt)
	n.Mine = n.AuthorID != nil && *n.AuthorID == actor
	return n, err
}

// ListNotes: GET .../contacts/{contact_id}/notes (newest first).
func (h *ContactsAPIHandler) ListNotes(w http.ResponseWriter, r *http.Request) {
	tc, id, ok := h.editor(w, r)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var exists bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM contacts WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, id).Scan(&exists); err != nil || !exists {
		if err != nil {
			http.Error(w, "failed to read notes", http.StatusInternalServerError)
			return
		}
		http.Error(w, "contact not found", http.StatusNotFound)
		return
	}
	rows, err := q.Query(r.Context(), noteSelect+` WHERE n.tenant_id=$1 AND n.contact_id=$2 ORDER BY n.created_at DESC, n.id DESC LIMIT 200`, tc.TenantID, id)
	if err != nil {
		http.Error(w, "failed to read notes", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []noteDTO{}
	for rows.Next() {
		n, err := h.scanNote(rows, tc.ActorID)
		if err != nil {
			http.Error(w, "failed to read notes", http.StatusInternalServerError)
			return
		}
		out = append(out, n)
	}
	writeJSON(w, map[string]any{"items": out})
}

type noteRequest struct {
	Body string `json:"body"`
}

func validNote(w http.ResponseWriter, body string) (string, bool) {
	b := strings.TrimSpace(body)
	if b == "" || utf8.RuneCountInString(b) > maxNoteRunes {
		http.Error(w, "invalid note", http.StatusUnprocessableEntity)
		return "", false
	}
	return b, true
}

// AddNote: POST .../contacts/{contact_id}/notes
func (h *ContactsAPIHandler) AddNote(w http.ResponseWriter, r *http.Request) {
	tc, id, ok := h.editor(w, r)
	if !ok {
		return
	}
	var req noteRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	body, ok := validNote(w, req.Body)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var noteID uuid.UUID
	err := q.QueryRow(r.Context(), `
		INSERT INTO contact_notes (tenant_id, contact_id, body, created_by_user_id)
		SELECT $1, c.id, $3, $4 FROM contacts c WHERE c.tenant_id = $1 AND c.id = $2 RETURNING id`, tc.TenantID, id, body, tc.ActorID).Scan(&noteID)
	if err == pgx.ErrNoRows {
		http.Error(w, "contact not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to save note", http.StatusInternalServerError)
		return
	}
	h.auditContact(r, tc, auditdomain.ActionContactNoteChanged, id, map[string]any{"op": "added", "note_id": noteID.String()})
	n, err := h.scanNote(q.QueryRow(r.Context(), noteSelect+` WHERE n.tenant_id=$1 AND n.id=$2`, tc.TenantID, noteID), tc.ActorID)
	if err != nil {
		http.Error(w, "failed to read note", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, n)
}

// EditNote: PUT .../contacts/{contact_id}/notes/{note_id} — only the author.
func (h *ContactsAPIHandler) EditNote(w http.ResponseWriter, r *http.Request) {
	tc, id, ok := h.editor(w, r)
	if !ok {
		return
	}
	noteID, err := uuid.Parse(r.PathValue("note_id"))
	if err != nil {
		http.Error(w, "invalid note_id", http.StatusBadRequest)
		return
	}
	var req noteRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	body, ok := validNote(w, req.Body)
	if !ok {
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	tag, err := q.Exec(r.Context(), `UPDATE contact_notes SET body=$4, updated_at=now() WHERE tenant_id=$1 AND contact_id=$2 AND id=$3 AND created_by_user_id=$5`, tc.TenantID, id, noteID, body, tc.ActorID)
	if err != nil {
		http.Error(w, "failed to save note", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 { // unknown, another tenant's, or someone else's: indistinguishable
		http.Error(w, "note not found", http.StatusNotFound)
		return
	}
	h.auditContact(r, tc, auditdomain.ActionContactNoteChanged, id, map[string]any{"op": "edited", "note_id": noteID.String()})
	n, err := h.scanNote(q.QueryRow(r.Context(), noteSelect+` WHERE n.tenant_id=$1 AND n.id=$2`, tc.TenantID, noteID), tc.ActorID)
	if err != nil {
		http.Error(w, "failed to read note", http.StatusInternalServerError)
		return
	}
	writeJSON(w, n)
}

// DeleteNote: DELETE .../contacts/{contact_id}/notes/{note_id} — only the author.
func (h *ContactsAPIHandler) DeleteNote(w http.ResponseWriter, r *http.Request) {
	tc, id, ok := h.editor(w, r)
	if !ok {
		return
	}
	noteID, err := uuid.Parse(r.PathValue("note_id"))
	if err != nil {
		http.Error(w, "invalid note_id", http.StatusBadRequest)
		return
	}
	tag, err := platformdb.QuerierFromContext(r.Context(), h.pool).Exec(r.Context(), `DELETE FROM contact_notes WHERE tenant_id=$1 AND contact_id=$2 AND id=$3 AND created_by_user_id=$4`, tc.TenantID, id, noteID, tc.ActorID)
	if err != nil {
		http.Error(w, "failed to delete note", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "note not found", http.StatusNotFound)
		return
	}
	h.auditContact(r, tc, auditdomain.ActionContactNoteChanged, id, map[string]any{"op": "deleted", "note_id": noteID.String()})
	w.WriteHeader(http.StatusNoContent)
}
