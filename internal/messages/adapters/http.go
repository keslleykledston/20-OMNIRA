package adapters

import (
	"encoding/json"
	"errors"
	"github.com/omnira/omnira/internal/entitlements"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/messages/ports"
)

// SendHandler serves outbound text. Mount it behind authn + the tenant-session
// middleware: tenant and sender come only from the TenantContext.
type SendHandler struct {
	svc      *application.Sender
	att      *application.Attachments // nil: outbound media is off
	attStore ports.AttachmentStore
	limiter  *uploadLimiter
	// slots bounds the uploads handled at once: each holds up to 16 MiB in memory (plus the stripped copy), so the budget is
	// maxConcurrentUploads * ~40 MiB however many operators click at the same time.
	slots chan struct{}
}

const maxConcurrentUploads = 4

func NewSendHandler(svc *application.Sender) *SendHandler { return &SendHandler{svc: svc} }

// WithAttachments turns on outbound media (ADR-0024): the upload/remove endpoints and `attachment_id` on send.
func (h *SendHandler) WithAttachments(att *application.Attachments, store ports.AttachmentStore) *SendHandler {
	h.att, h.attStore, h.limiter, h.slots = att, store, newUploadLimiter(20, time.Minute), make(chan struct{}, maxConcurrentUploads)
	return h
}

// SetUploadSlots changes how many uploads are handled at once (default 4). Each slot can hold ~40 MiB: size it against the memory limit.
func (h *SendHandler) SetUploadSlots(n int) {
	if n > 0 {
		h.slots = make(chan struct{}, n)
	}
}

type sendRequest struct {
	Text string `json:"text"`
	// AttachmentID, when present, sends the previously uploaded file; Text is then its optional caption.
	AttachmentID *uuid.UUID `json:"attachment_id,omitempty"`
}

type sendResponse struct {
	ID             uuid.UUID `json:"id"`
	ConversationID uuid.UUID `json:"conversation_id"`
	Direction      string    `json:"direction"`
	Body           string    `json:"body"`
	Status         string    `json:"status"`
	CreatedAt      string    `json:"created_at"`
}

func (h *SendHandler) Send(w http.ResponseWriter, r *http.Request) {
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	var req sendRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil || json.Unmarshal(body, &req) != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var res application.SendResult
	if req.AttachmentID != nil {
		if h.att == nil {
			http.Error(w, "outbound media is not enabled", http.StatusNotImplemented)
			return
		}
		res, err = h.svc.SendMedia(r.Context(), h.attStore, conversationID, *req.AttachmentID, req.Text, r.Header.Get("Idempotency-Key"))
	} else {
		res, err = h.svc.Send(r.Context(), conversationID, req.Text, r.Header.Get("Idempotency-Key"))
	}
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusAccepted
	if res.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(sendResponse{
		ID: res.Message.ID, ConversationID: res.Message.ConversationID, Direction: "outbound",
		Body: res.Message.Body, Status: res.Message.Status, CreatedAt: res.Message.CreatedAt.UTC().Format(time.RFC3339),
	})
}

type templateRequest struct {
	TemplateID uuid.UUID `json:"template_id"`
	Params     []string  `json:"params"`
}

// SendTemplate serves POST /inbox/conversations/{id}/template.
func (h *SendHandler) SendTemplate(w http.ResponseWriter, r *http.Request) {
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	var req templateRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil || json.Unmarshal(body, &req) != nil || req.TemplateID == uuid.Nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	res, err := h.svc.SendTemplate(r.Context(), conversationID, req.TemplateID, req.Params, r.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusAccepted
	if res.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(sendResponse{
		ID: res.Message.ID, ConversationID: res.Message.ConversationID, Direction: "outbound",
		Body: res.Message.Body, Status: res.Message.Status, CreatedAt: res.Message.CreatedAt.UTC().Format(time.RFC3339),
	})
}

func fail(w http.ResponseWriter, err error) {
	var rejected *application.AttachmentRejected
	switch {
	case errors.As(err, &rejected):
		http.Error(w, "file not accepted: "+rejected.Reason, http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrMediaUnsupported):
		http.Error(w, "this channel cannot send media", http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrInvalidCaption):
		http.Error(w, "caption is limited to 1024 characters of plain text", http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrAttachmentUnavailable):
		http.Error(w, "attachment is not available (expired, already sent or not yours)", http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrTooManyAttachments):
		http.Error(w, "too many unsent attachments in this conversation", http.StatusConflict)
	case errors.Is(err, application.ErrAttachmentQuota):
		http.Error(w, "the attachment storage quota was reached: send or remove pending files", http.StatusConflict)
	case errors.Is(err, application.ErrAttachmentRate):
		http.Error(w, "too many uploads, wait a moment", http.StatusTooManyRequests)
	case errors.Is(err, application.ErrAttachmentInfected):
		http.Error(w, "the file was blocked by the antivirus", http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrScannerUnavailable):
		http.Error(w, "the antivirus is unavailable, try again later", http.StatusServiceUnavailable)
	case errors.Is(err, application.ErrTemplateUnsupported), errors.Is(err, application.ErrTemplateNotAllowed), errors.Is(err, application.ErrTemplateParams):
		http.Error(w, err.Error()[len("messages: "):], http.StatusUnprocessableEntity)
	case errors.Is(err, entitlements.ErrDisabled):
		http.Error(w, "this capability is disabled for your company", http.StatusForbidden)
	case errors.Is(err, application.ErrForbidden), errors.Is(err, application.ErrNotAssignedToYou):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, application.ErrNotFound):
		http.Error(w, "conversation not found", http.StatusNotFound)
	case errors.Is(err, application.ErrConversationClosed):
		http.Error(w, "conversation is finalized: the contact's next message starts a new attendance", http.StatusConflict)
	case errors.Is(err, application.ErrUnassigned):
		http.Error(w, "conversation must be assigned before replying", http.StatusConflict)
	case errors.Is(err, application.ErrConversationChanged):
		http.Error(w, "conversation changed, retry", http.StatusConflict)
	case errors.Is(err, application.ErrWindowClosed):
		http.Error(w, "customer service window closed: free text is only accepted within 24 h of the customer's last message", http.StatusConflict)
	case errors.Is(err, application.ErrChannelUnavailable):
		http.Error(w, "conversation has no active text channel", http.StatusConflict)
	case errors.Is(err, application.ErrInvalidKey):
		http.Error(w, err.Error()[len("messages: "):], http.StatusBadRequest)
	case errors.Is(err, application.ErrInvalidText):
		http.Error(w, err.Error()[len("messages: "):], http.StatusUnprocessableEntity)
	case errors.Is(err, application.ErrIdempotencyMismatch):
		http.Error(w, err.Error()[len("messages: "):], http.StatusUnprocessableEntity)
	default:
		log.Printf("messages send: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
