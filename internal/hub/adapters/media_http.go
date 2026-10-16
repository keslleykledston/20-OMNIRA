package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/entitlements"
	mediadomain "github.com/omnira/omnira/internal/media/domain"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapp "github.com/omnira/omnira/internal/messages/application"
	messagesports "github.com/omnira/omnira/internal/messages/ports"
)

// Files for a Hub agent (ADR-0024 x ADR-0040): upload, remove and send an attachment, and the line's capabilities the composer needs. Two doors reach the same Hub
// write path (replying.Service): the Hub's own routes (the "Todas" tab) and the instance routes of a person attending the company through the Hub. Both authorize in
// the caller's own session, then execute in a system tenant session of the item's company that re-proves the delegation under the conversation lock. The body of an
// upload is read BEFORE the session (messagesadapters.SendHandler.BufferUpload), exactly like a member's.

// writeMediaError maps what a file operation can refuse to an HTTP answer, and leaves the rest to writeReplyError.
func writeMediaError(w http.ResponseWriter, err error) {
	var rejected *messagesapp.AttachmentRejected
	switch {
	case errors.As(err, &rejected):
		if rejected.Reason == mediadomain.ReasonTooLarge {
			httpError(w, "file too large (limit 16 MiB)", http.StatusRequestEntityTooLarge)
			return
		}
		httpError(w, "file not accepted: "+rejected.Reason, http.StatusUnprocessableEntity)
	case errors.Is(err, messagesapp.ErrMediaUnsupported):
		httpError(w, "this channel cannot send media", http.StatusUnprocessableEntity)
	case errors.Is(err, messagesapp.ErrInvalidCaption):
		httpError(w, "caption is limited to 1024 characters of plain text", http.StatusUnprocessableEntity)
	case errors.Is(err, messagesapp.ErrAttachmentUnavailable):
		httpError(w, "attachment is not available (expired, already sent or not yours)", http.StatusUnprocessableEntity)
	case errors.Is(err, messagesapp.ErrTooManyAttachments):
		httpError(w, "too many unsent attachments in this conversation", http.StatusConflict)
	case errors.Is(err, messagesapp.ErrAttachmentQuota):
		httpError(w, "the attachment storage quota was reached: send or remove pending files", http.StatusConflict)
	case errors.Is(err, messagesapp.ErrAttachmentRate):
		httpError(w, "too many uploads, wait a moment", http.StatusTooManyRequests)
	case errors.Is(err, messagesapp.ErrAttachmentInfected):
		httpError(w, "the file was blocked by the antivirus", http.StatusUnprocessableEntity)
	case errors.Is(err, messagesapp.ErrScannerUnavailable):
		httpError(w, "the antivirus is unavailable, try again later", http.StatusServiceUnavailable)
	case errors.Is(err, entitlements.ErrDisabled):
		httpError(w, "this capability is disabled for your company", http.StatusForbidden)
	default:
		writeReplyError(w, err)
	}
}

type uploadedAttachmentJSON struct {
	ID        uuid.UUID `json:"id"`
	Kind      string    `json:"kind"`
	Mime      string    `json:"mime"`
	SizeBytes int64     `json:"size_bytes"`
	FileName  string    `json:"file_name"`
	ExpiresAt string    `json:"expires_at"`
}

func writeUploaded(w http.ResponseWriter, att *messagesports.OutboundAttachment) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(uploadedAttachmentJSON{ID: att.ID, Kind: att.Kind, Mime: att.Mime, SizeBytes: att.SizeBytes, FileName: att.FileName, ExpiresAt: att.ExpiresAt.UTC().Format(time.RFC3339)})
}

func writeMediaMessage(w http.ResponseWriter, res messagesapp.SendResult, extra map[string]any) {
	status := http.StatusAccepted
	if res.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	out := map[string]any{
		"id": res.Message.ID, "conversation_id": res.Message.ConversationID, "direction": "outbound", "body": res.Message.Body,
		"status": res.Message.Status, "created_at": res.Message.CreatedAt.UTC().Format(time.RFC3339),
	}
	for k, v := range extra {
		out[k] = v
	}
	_ = json.NewEncoder(w).Encode(out)
}

// ---- the Hub's own routes

// WithAttachments gives the Hub's own routes the outbound attachments service (nil: the Hub's composer offers no files).
func (h *HTTPHandler) WithAttachments(a *messagesapp.Attachments) *HTTPHandler {
	h.reply.WithAttachments(a)
	return h
}

func expectedTenantOf(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.URL.Query().Get("expected_tenant_id"))
	return id, err == nil
}

// UploadAttachment serves POST /hubs/{hub_id}/inbox/{item_id}/attachments?expected_tenant_id= for a body already read by BufferUpload.
func (h *HTTPHandler) UploadAttachment(w http.ResponseWriter, r *http.Request) {
	sc, ok := h.writeScope(w, r)
	if !ok {
		return
	}
	expected, ok := expectedTenantOf(r)
	if !ok {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	name, mime, data, ok := messagesadapters.UploadedFileFromContext(r.Context())
	if !ok {
		httpError(w, "a \"file\" part is required", http.StatusBadRequest)
		return
	}
	t, err := h.reply.Authorize(r.Context(), sc.actor, sc.hub, sc.item, expected, correlationOf(r))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	att, err := h.reply.UploadAttachment(r.Context(), sc.actor, t, name, mime, data)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeUploaded(w, att)
}

// RemoveAttachment serves DELETE /hubs/{hub_id}/inbox/{item_id}/attachments/{attachment_id}?expected_tenant_id=.
func (h *HTTPHandler) RemoveAttachment(w http.ResponseWriter, r *http.Request) {
	sc, ok := h.writeScope(w, r)
	if !ok {
		return
	}
	expected, ok := expectedTenantOf(r)
	attachmentID, aerr := uuid.Parse(r.PathValue("attachment_id"))
	if !ok || aerr != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	t, err := h.reply.Authorize(r.Context(), sc.actor, sc.hub, sc.item, expected, correlationOf(r))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	if err := h.reply.RemoveAttachment(r.Context(), sc.actor, t, attachmentID); err != nil {
		writeMediaError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ChannelItem serves GET /hubs/{hub_id}/inbox/{item_id}/channel?expected_tenant_id=: what the composer may offer on this item's line.
func (h *HTTPHandler) ChannelItem(w http.ResponseWriter, r *http.Request) {
	sc, ok := h.writeScope(w, r)
	if !ok {
		return
	}
	expected, ok := expectedTenantOf(r)
	if !ok {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	t, err := h.reply.Authorize(r.Context(), sc.actor, sc.hub, sc.item, expected, correlationOf(r))
	if err != nil {
		writeReplyError(w, err)
		return
	}
	info, err := h.reply.ChannelInfo(r.Context(), t)
	if err != nil {
		writeReplyError(w, err)
		return
	}
	writeJSON(w, info)
}

// ---- the instance routes of a person attending the company through the Hub

// WithAttachments gives the delegated instance routes the outbound attachments service (nil: no files for a delegate).
func (d *DelegatedWrites) WithAttachments(a *messagesapp.Attachments) *DelegatedWrites {
	d.reply.WithAttachments(a)
	return d
}

// Upload wraps POST /tenants/{tenant_id}/inbox/conversations/{conversation_id}/attachments (behind BufferUpload): a delegated request goes to the Hub's write path.
func (d *DelegatedWrites) Upload(original http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, delegated := delegatedContext(r)
		if !delegated {
			original.ServeHTTP(w, r)
			return
		}
		name, mime, data, ok := messagesadapters.UploadedFileFromContext(r.Context())
		if !ok {
			httpError(w, "a \"file\" part is required", http.StatusBadRequest)
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		att, err := d.reply.UploadAttachment(r.Context(), tc.ActorID, t, name, mime, data)
		if err != nil {
			writeMediaError(w, err)
			return
		}
		writeUploaded(w, att)
	})
}

// RemoveAttachment wraps DELETE /tenants/{tenant_id}/inbox/conversations/{conversation_id}/attachments/{attachment_id}.
func (d *DelegatedWrites) RemoveAttachment(original http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, delegated := delegatedContext(r)
		if !delegated {
			original.ServeHTTP(w, r)
			return
		}
		attachmentID, err := uuid.Parse(r.PathValue("attachment_id"))
		if err != nil {
			httpError(w, "invalid request", http.StatusBadRequest)
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		if err := d.reply.RemoveAttachment(r.Context(), tc.ActorID, t, attachmentID); err != nil {
			writeMediaError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// Channel wraps GET /tenants/{tenant_id}/inbox/conversations/{conversation_id}/channel: a Hub agent cannot read channel_connections, so the answer is built by the
// Hub's write path in a system session of the item's company.
func (d *DelegatedWrites) Channel(original http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, delegated := delegatedContext(r)
		if !delegated {
			original.ServeHTTP(w, r)
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		info, err := d.reply.ChannelInfo(r.Context(), t)
		if err != nil {
			writeReplyError(w, err)
			return
		}
		writeJSON(w, info)
	})
}
